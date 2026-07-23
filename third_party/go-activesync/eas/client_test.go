// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

// captured records the most-recent request a test server saw.
type captured struct {
	mu      sync.Mutex
	method  string
	url     *url.URL
	headers http.Header
	body    []byte
}

func (c *captured) record(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body)) // re-arm for handler
	c.mu.Lock()
	defer c.mu.Unlock()
	c.method = r.Method
	c.url = r.URL
	c.headers = r.Header.Clone()
	c.body = body
}

// newTestClient spins up an httptest.Server and a Client targeting it.
// The handler is the caller's; cap captures the last request.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*httpClient, *captured, *httptest.Server) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{
		ServerURL: srv.URL + "/Microsoft-Server-ActiveSync",
		Username:  "henry",
		Password:  "hunter2",
		DeviceID:  "abcdef0123456789abcdef0123456789",
		State:     NewMemoryState(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*httpClient), cap, srv
}

func TestNewClient_validation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"missing url", Config{Username: "u", Password: "p", DeviceID: "d", State: NewMemoryState()}, "ServerURL"},
		{"missing user", Config{ServerURL: "https://x", Password: "p", DeviceID: "d", State: NewMemoryState()}, "Username"},
		{"missing pass", Config{ServerURL: "https://x", Username: "u", DeviceID: "d", State: NewMemoryState()}, "Password"},
		{"missing device", Config{ServerURL: "https://x", Username: "u", Password: "p", State: NewMemoryState()}, "DeviceID"},
		{"missing state", Config{ServerURL: "https://x", Username: "u", Password: "p", DeviceID: "d"}, "State"},
		{"bad scheme", Config{ServerURL: "ftp://x", Username: "u", Password: "p", DeviceID: "d", State: NewMemoryState()}, "must be http"},
		{"unparseable url", Config{ServerURL: "://x", Username: "u", Password: "p", DeviceID: "d", State: NewMemoryState()}, "ServerURL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestNewClient_defaults(t *testing.T) {
	c, err := NewClient(Config{
		ServerURL: "https://x/Microsoft-Server-ActiveSync",
		Username:  "u", Password: "p", DeviceID: "d", State: NewMemoryState(),
	})
	if err != nil {
		t.Fatal(err)
	}
	hc := c.(*httpClient)
	if hc.cfg.DeviceType != "GoActiveSync" {
		t.Errorf("DeviceType: %q", hc.cfg.DeviceType)
	}
	if hc.cfg.ASVersion != "14.1" {
		t.Errorf("ASVersion: %q", hc.cfg.ASVersion)
	}
	if hc.cfg.UserAgent == "" {
		t.Error("UserAgent unset")
	}
	if hc.cfg.Registry == nil {
		t.Error("Registry unset")
	}
}

func TestPost_buildsURLAndHeaders(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte{}) // empty body, will produce nil doc
	})
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("0"))),
	}
	if _, err := c.post(context.Background(), "FolderSync", doc); err != nil {
		t.Fatal(err)
	}
	if cap.method != http.MethodPost {
		t.Errorf("method: %q", cap.method)
	}
	q := cap.url.Query()
	if q.Get("Cmd") != "FolderSync" {
		t.Errorf("Cmd: %q", q.Get("Cmd"))
	}
	if q.Get("User") != "henry" {
		t.Errorf("User: %q", q.Get("User"))
	}
	if q.Get("DeviceId") != "abcdef0123456789abcdef0123456789" {
		t.Errorf("DeviceId: %q", q.Get("DeviceId"))
	}
	if q.Get("DeviceType") != "GoActiveSync" {
		t.Errorf("DeviceType: %q", q.Get("DeviceType"))
	}
	if got := cap.headers.Get("Content-Type"); got != "application/vnd.ms-sync.wbxml" {
		t.Errorf("Content-Type: %q", got)
	}
	if got := cap.headers.Get("MS-ASProtocolVersion"); got != "14.1" {
		t.Errorf("ASVersion: %q", got)
	}
	auth := cap.headers.Get("Authorization")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("henry:hunter2"))
	if auth != want {
		t.Errorf("Auth: %q want %q", auth, want)
	}
	if cap.headers.Get("X-MS-PolicyKey") != "" {
		t.Errorf("X-MS-PolicyKey should be unset before provisioning")
	}
}

func TestPost_includesPolicyKeyAfterProvision(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte{})
	})
	_ = c.cfg.State.SetPolicyKey(context.Background(), "POLICY-42")

	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	if _, err := c.post(context.Background(), "FolderSync", doc); err != nil {
		t.Fatal(err)
	}
	if got := cap.headers.Get("X-MS-PolicyKey"); got != "POLICY-42" {
		t.Errorf("X-MS-PolicyKey: %q", got)
	}
}

func TestPost_HTTPErrorOnNon2xx(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})
	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	_, err := c.post(context.Background(), "FolderSync", doc)
	if !IsHTTPStatus(err, 401) {
		t.Errorf("err = %v", err)
	}
}

func TestPost_decodesResponseBody(t *testing.T) {
	resp := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
			wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("42")),
		),
	}
	respBytes, err := wbxml.Marshal(resp, wbxml.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(respBytes)
	})
	doc, err := c.post(context.Background(), "FolderSync",
		&wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")})
	if err != nil {
		t.Fatal(err)
	}
	if doc == nil || doc.Root.Find("SyncKey").TextContent() != "42" {
		t.Errorf("decoded badly: %+v", doc)
	}
}

func TestReadCapped(t *testing.T) {
	r := strings.NewReader("hello")
	b, err := readCapped(r, 100)
	if err != nil || string(b) != "hello" {
		t.Errorf("ok read failed: b=%q err=%v", b, err)
	}
	r = strings.NewReader("toolong")
	if _, err := readCapped(r, 4); err == nil {
		t.Error("want error when over cap")
	}
}

func TestCapBytes(t *testing.T) {
	in := []byte("0123456789")
	if got := capBytes(in, 100); string(got) != "0123456789" {
		t.Errorf("under cap: %q", got)
	}
	if got := capBytes(in, 4); string(got) != "0123" {
		t.Errorf("over cap: %q", got)
	}
	// Returned slice is a copy, not aliased.
	out := capBytes(in, 4)
	out[0] = 'X'
	if in[0] != '0' {
		t.Error("capBytes did not copy")
	}
}

func TestPost_marshalError(t *testing.T) {
	// Custom registry without AirSync; a request that uses AirSync will
	// fail at Marshal time, so the server is never reached.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached when Marshal fails")
	})
	c.cfg.Registry = wbxml.NewRegistry() // empty
	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync")}
	if _, err := c.post(context.Background(), "Sync", doc); err == nil ||
		!strings.Contains(err.Error(), "marshal") {
		t.Errorf("err = %v", err)
	}
}

func TestPost_unmarshalError(t *testing.T) {
	// Server returns garbage that isn't valid WBXML.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write([]byte{0xFF, 0xFE, 0xFD}) // not a valid WBXML document
	})
	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	if _, err := c.post(context.Background(), "FolderSync", doc); err == nil ||
		!strings.Contains(err.Error(), "decode") {
		t.Errorf("err = %v", err)
	}
}

func TestPostRaw_policyKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached when state read fails")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), policyKeyErr: errSentinel("boom")}
	if _, err := c.postRaw(context.Background(), "FolderSync", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "policy key") {
		t.Errorf("err = %v", err)
	}
}

func TestPostRaw_gunzipBadServerStream(t *testing.T) {
	// Server claims gzip but sends garbage; gunzip should fail.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write([]byte("not actually gzip"))
	})
	if _, err := c.postRaw(context.Background(), "FolderSync", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "gunzip") {
		t.Errorf("err = %v", err)
	}
}

func TestPostRaw_449TriggersProvisionRetry(t *testing.T) {
	// First non-Provision call returns 449. The client should attempt to
	// re-Provision (which we make fail), and surface the wrapped error.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Always 449 — so even the Provision retry sees it (recursion is
		// guarded by the `cmd != "Provision"` check inside postRaw).
		http.Error(w, "needs provision", 449)
	})
	_, err := c.postRaw(context.Background(), "FolderSync", []byte("x"))
	if err == nil {
		t.Fatal("want error")
	}
	// The final error should mention re-provision since Provision itself fails.
	if !strings.Contains(err.Error(), "re-provision") && !IsHTTPStatus(err, 449) {
		t.Errorf("err = %v", err)
	}
}

func TestHTTPDo_authHeaderError(t *testing.T) {
	c, _, srv := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached when auth fails")
	})
	c.cfg.AuthHeader = func(ctx context.Context) (string, error) {
		return "", errSentinel("token expired")
	}
	c.auth = "" // ensure fallback isn't used
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, srv.URL, nil)
	if _, err := c.httpDo(req); err == nil || !strings.Contains(err.Error(), "auth") {
		t.Errorf("err = %v", err)
	}
}

// errReader returns its configured error on Read.
type errReader struct{ err error }

func (e *errReader) Read(_ []byte) (int, error) { return 0, e.err }

func TestReadCapped_readError(t *testing.T) {
	want := errSentinel("network died")
	if _, err := readCapped(&errReader{err: want}, 100); err != want {
		t.Errorf("err = %v", err)
	}
}

func TestFindShallow_nilReceiver(t *testing.T) {
	if findShallow(nil, "x", 5) != nil {
		t.Error("findShallow(nil, ...) should be nil")
	}
}

func TestAtoi(t *testing.T) {
	cases := map[string]int{
		"":     0,
		"0":    0,
		"1":    1,
		"42":   42,
		"  3 ": 3,
		"abc":  0,
		"1a":   0,
	}
	for in, want := range cases {
		if got := atoi(in); got != want {
			t.Errorf("atoi(%q) = %d, want %d", in, got, want)
		}
	}
}
