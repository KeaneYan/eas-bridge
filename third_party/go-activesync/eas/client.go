// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/hstern/go-activesync/wbxml"
)

// Config holds the inputs needed to build a Client. Fields without
// defaults are marked "required"; the rest fall back to sane EAS 14.1
// values.
type Config struct {
	// ServerURL is the EAS endpoint, e.g. "https://mail/Microsoft-Server-ActiveSync".
	// Required.
	ServerURL string
	// Username + Password are sent as HTTP Basic auth. Both required.
	Username string
	Password string
	// DeviceID is a 32-hex-char client identifier. Required; create once
	// per account and persist (the server treats a new ID as a new device).
	DeviceID string
	// DeviceType is sent in the URL query and policy doc. Default
	// "GoActiveSync". Some servers log this string in admin tools;
	// callers usually want to override with their app's name.
	DeviceType string
	// ASVersion is the protocol version sent in MS-ASProtocolVersion.
	// Default "14.1".
	ASVersion string
	// UserAgent is the HTTP User-Agent. Default "go-activesync/0.1".
	UserAgent string
	// HTTPClient is the transport. Default http.DefaultClient.
	HTTPClient *http.Client
	// Logger receives debug events. Default slog.Default().
	Logger *slog.Logger
	// State is required for stateful commands (Provision, Sync, FolderSync).
	State StateStore
	// Registry is the WBXML codepage registry. Default wbxml.DefaultRegistry().
	Registry *wbxml.Registry
	// AuthHeader, when set, overrides Username+Password for the
	// Authorization header. Called per request so callers can refresh
	// short-lived tokens (e.g. OAuth bearer) lazily. Return the full
	// header value, e.g. "Bearer eyJhbGc...". When unset, the client
	// uses HTTP Basic with Username:Password.
	AuthHeader func(ctx context.Context) (string, error)
	// RetryOn401 enables one transparent retry on a 401 Unauthorized
	// response, after a fresh AuthHeader call. Useful for OAuth bearer
	// flows where the token may have expired between requests.
	RetryOn401 bool
	// GzipRequests, when true, compresses request bodies above
	// GzipMinBytes with gzip and sets Content-Encoding accordingly.
	// Most EAS servers (Z-Push 2.5+, Exchange 2010+) accept this.
	GzipRequests bool
	// GzipMinBytes is the minimum body size that triggers compression
	// when GzipRequests is true. Default 1 KiB.
	GzipMinBytes int
	// Base64URL, when true, packs Cmd/User/DeviceId/DeviceType/PolicyKey
	// into a single base64-encoded query parameter per MS-ASHTTP §2.2.1.1.2.
	// Smaller request URLs and slightly faster server-side parsing on
	// some Exchange deployments. Off by default since Z-Push and SOGo
	// are happier with plain query strings.
	Base64URL bool
}

// httpClient is the production [Client] implementation. Construct via
// [NewClient]; callers receive the [Client] interface and never name
// this type directly.
type httpClient struct {
	cfg     Config
	auth    string // pre-built "Basic <base64>" header value
	baseURL *url.URL

	policyMu   sync.Mutex
	lastPolicy *Policy
}

// NewClient validates the Config, applies defaults, and returns a ready
// Client. The returned client makes no network calls until a command
// method is invoked.
//
// The returned value satisfies the [Client] interface; the concrete
// type is unexported. For unit tests, see the easmock subpackage which
// provides hand-written test doubles.
func NewClient(cfg Config) (Client, error) {
	if cfg.ServerURL == "" {
		return nil, errors.New("eas: Config.ServerURL is required")
	}
	if cfg.Username == "" {
		return nil, errors.New("eas: Config.Username is required")
	}
	// Password is only required for the default Basic-auth path. When
	// AuthHeader is supplied (Bearer/NTLM/etc.), the password is irrelevant.
	if cfg.AuthHeader == nil && cfg.Password == "" {
		return nil, errors.New("eas: Config.Password is required (or set AuthHeader)")
	}
	if cfg.DeviceID == "" {
		return nil, errors.New("eas: Config.DeviceID is required")
	}
	if cfg.State == nil {
		return nil, errors.New("eas: Config.State is required")
	}
	u, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("eas: parse ServerURL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("eas: ServerURL must be http or https, got %q", u.Scheme)
	}
	if cfg.DeviceType == "" {
		cfg.DeviceType = "GoActiveSync"
	}
	if cfg.ASVersion == "" {
		cfg.ASVersion = "14.1"
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "go-activesync/0.1"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Registry == nil {
		cfg.Registry = wbxml.DefaultRegistry()
	}
	c := &httpClient{cfg: cfg, baseURL: u}
	if cfg.AuthHeader == nil {
		// Pre-build the Basic header once; nothing changes between requests.
		val := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.Password))
		c.auth = "Basic " + val
	}
	return c, nil
}

// commandURL builds the EAS POST URL for the given command. By default
// the User/DeviceId/DeviceType/Cmd parameters are sent as a normal query
// string (Z-Push and SOGo expect this); set Config.Base64URL to switch
// to the alternative single-parameter base64 encoding from MS-ASHTTP.
func (c *httpClient) commandURL(cmd string) string {
	if c.cfg.Base64URL {
		if encoded, err := c.applyB64ToURL(cmd, c.baseURL.String()); err == nil {
			return encoded
		}
		// Fall through to plain query on encoding error (won't happen
		// for commands in our enum but defensive).
	}
	q := url.Values{}
	q.Set("Cmd", cmd)
	q.Set("User", c.cfg.Username)
	q.Set("DeviceId", c.cfg.DeviceID)
	q.Set("DeviceType", c.cfg.DeviceType)
	u := *c.baseURL
	u.RawQuery = q.Encode()
	return u.String()
}

// post issues a WBXML POST for the given command and returns the parsed
// response document. Sets all standard headers; reads the policy key from
// the StateStore if one has been persisted.
func (c *httpClient) post(ctx context.Context, cmd string, body *wbxml.Document) (*wbxml.Document, error) {
	bodyBytes, err := wbxml.Marshal(body, c.cfg.Registry)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: marshal: %w", cmd, err)
	}
	respBytes, err := c.postRaw(ctx, cmd, bodyBytes)
	if err != nil {
		return nil, err
	}
	if len(respBytes) == 0 {
		// Some servers reply 200 with an empty body to acknowledge a
		// stateless command. Callers must tolerate a nil document.
		return nil, nil
	}
	doc, err := wbxml.Unmarshal(respBytes, c.cfg.Registry)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: decode response: %w", cmd, err)
	}
	return doc, nil
}

// postRaw POSTs raw bytes for the given command. Used by post (which adds
// WBXML codec) and by debugging tools that want to capture the wire bytes.
//
// Two transparent retries are wired in:
//
//   - HTTP 401 + RetryOn401 + AuthHeader: refresh the bearer token and
//     retry once (OAuth flows where tokens may have expired).
//   - HTTP 449 (Retry With, Microsoft IIS extension; MS-ASPROV §3.1.5.2):
//     re-Provision and retry once. EAS servers respond with 449 when
//     the X-MS-PolicyKey is missing or stale; the spec mandates this
//     recovery and we always do it. Skipped for the Provision command
//     itself to avoid a recursive loop.
func (c *httpClient) postRaw(ctx context.Context, cmd string, body []byte) ([]byte, error) {
	respBody, err := c.postRawOnce(ctx, cmd, body)
	if err != nil {
		switch {
		case c.cfg.RetryOn401 && c.cfg.AuthHeader != nil && IsHTTPStatus(err, http.StatusUnauthorized):
			c.cfg.Logger.Debug("eas: 401 — refreshing auth and retrying", "cmd", cmd)
			respBody, err = c.postRawOnce(ctx, cmd, body)
		case IsHTTPStatus(err, 449) && cmd != "Provision":
			c.cfg.Logger.Debug("eas: 449 — re-provisioning and retrying", "cmd", cmd)
			if perr := c.Provision(ctx); perr != nil {
				return nil, fmt.Errorf("eas: %s: re-provision after 449: %w", cmd, perr)
			}
			respBody, err = c.postRawOnce(ctx, cmd, body)
		}
	}
	return respBody, err
}

func (c *httpClient) postRawOnce(ctx context.Context, cmd string, body []byte) ([]byte, error) {
	target := c.commandURL(cmd)

	var (
		reqBody    io.Reader = bytes.NewReader(body)
		contentLen           = int64(len(body))
		bodyEnc    string
	)
	gzipMin := c.cfg.GzipMinBytes
	if gzipMin <= 0 {
		gzipMin = 1024
	}
	if c.cfg.GzipRequests && len(body) >= gzipMin {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(body); err != nil {
			return nil, fmt.Errorf("eas: %s: gzip body: %w", cmd, err)
		}
		if err := gz.Close(); err != nil {
			return nil, fmt.Errorf("eas: %s: gzip close: %w", cmd, err)
		}
		reqBody = &buf
		contentLen = int64(buf.Len())
		bodyEnc = "gzip"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, reqBody)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: build request: %w", cmd, err)
	}
	req.ContentLength = contentLen
	if bodyEnc != "" {
		req.Header.Set("Content-Encoding", bodyEnc)
	}
	req.Header.Set("Content-Type", "application/vnd.ms-sync.wbxml")
	req.Header.Set("Accept", "application/vnd.ms-sync.wbxml")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("MS-ASProtocolVersion", c.cfg.ASVersion)
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	authVal, err := c.authHeaderValue(ctx)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: auth: %w", cmd, err)
	}
	if authVal != "" {
		req.Header.Set("Authorization", authVal)
	}

	pk, err := c.cfg.State.PolicyKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: read policy key: %w", cmd, err)
	}
	if pk != "" {
		req.Header.Set("X-MS-PolicyKey", pk)
	}

	c.cfg.Logger.Debug("eas request", "cmd", cmd, "url", target, "bodyBytes", len(body), "policyKey", pk != "")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: %w", cmd, err)
	}
	defer resp.Body.Close()

	respReader, err := decodeIfGzip(resp)
	if err != nil {
		return nil, fmt.Errorf("eas: %s: response gunzip: %w", cmd, err)
	}
	respBody, err := readCapped(respReader, 64<<20) // 64 MiB hard ceiling
	if err != nil {
		return nil, fmt.Errorf("eas: %s: read response: %w", cmd, err)
	}
	c.cfg.Logger.Debug("eas response",
		"cmd", cmd, "status", resp.StatusCode, "bodyBytes", len(respBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			URL:        target,
			Body:       capBytes(respBody, 4<<10),
		}
	}
	return respBody, nil
}

// authHeaderValue returns the Authorization header value for one
// request. Uses Config.AuthHeader if set; otherwise the pre-built Basic
// header from NewClient.
func (c *httpClient) authHeaderValue(ctx context.Context) (string, error) {
	if c.cfg.AuthHeader != nil {
		return c.cfg.AuthHeader(ctx)
	}
	return c.auth, nil
}

// httpDo issues an arbitrary request through the client's HTTP layer.
// Used by Options (HTTP OPTIONS verb) and Autodiscover (XML POST with
// different content type).
func (c *httpClient) httpDo(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	if req.Header.Get("Authorization") == "" {
		val, err := c.authHeaderValue(req.Context())
		if err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
		if val != "" {
			req.Header.Set("Authorization", val)
		}
	}
	return c.cfg.HTTPClient.Do(req)
}

// decodeIfGzip wraps the response body in a gzip reader if the server
// signaled gzip encoding. Most callers should use this rather than
// reading resp.Body directly.
func decodeIfGzip(resp *http.Response) (io.Reader, error) {
	if !strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		return resp.Body, nil
	}
	return gzip.NewReader(resp.Body)
}

// readCapped reads up to limit bytes; if more are available it returns an
// error rather than silently truncating.
func readCapped(r io.Reader, limit int64) ([]byte, error) {
	lr := &io.LimitedReader{R: r, N: limit + 1}
	b, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response exceeds %d byte cap", limit)
	}
	return b, nil
}

func capBytes(b []byte, n int) []byte {
	if len(b) <= n {
		out := make([]byte, len(b))
		copy(out, b)
		return out
	}
	out := make([]byte, n)
	copy(out, b[:n])
	return out
}

// findShallow does a bounded-depth search for a child with name. depth=0
// matches root only; depth=1 matches root+immediate children; etc.
func findShallow(e *wbxml.Element, name string, depth int) *wbxml.Element {
	if e == nil {
		return nil
	}
	if e.Name == name {
		return e
	}
	if depth == 0 {
		return nil
	}
	for _, c := range e.Children {
		if ce, ok := c.(*wbxml.Element); ok {
			if got := findShallow(ce, name, depth-1); got != nil {
				return got
			}
		}
	}
	return nil
}

// atoi is a minimal non-validating int parser; EAS status codes are always
// short ASCII decimal so we don't need strconv's full suite. Returns 0 for
// junk inputs (which the caller's status check will then trip on).
func atoi(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}
