// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// AutodiscoverResult is the parsed output of a successful Autodiscover
// query for the "MobileSync" (i.e. EAS) protocol.
type AutodiscoverResult struct {
	URL            string // the EAS endpoint URL the server reports
	ServerHostname string // the bare hostname (informational)
	DisplayName    string // the user's display name, if reported
}

// AutodiscoverOptions configures a single Autodiscover query.
type AutodiscoverOptions struct {
	// HTTPClient transports the discovery requests. If nil, http.DefaultClient.
	HTTPClient *http.Client
	// Logger receives debug events. If nil, slog.Default().
	Logger *slog.Logger
	// UserAgent is sent in the request header. Default "go-activesync/0.1".
	UserAgent string
	// Endpoints overrides the default endpoint candidates (e.g. for tests).
	// When nil, autodiscoverEndpointsFor is used.
	Endpoints []string
	// SkipHTTPRedirect disables option 3 (the HTTP GET fallback that
	// reads a 302 from autodiscover.<domain>). Useful in environments
	// where unauthenticated HTTP probes might be blocked.
	SkipHTTPRedirect bool
	// SkipSRVLookup disables option 4 (DNS SRV record fallback). Useful
	// when DNS is restricted or the lookup is too slow.
	SkipSRVLookup bool
	// SkipWellKnownFallback disables option 5 (the well-known EAS path
	// probe). When every schema-aware Autodiscover attempt fails, the
	// library tries HTTP OPTIONS against the canonical EAS endpoint at
	// the bare domain, autodiscover.<domain>, and mail.<domain>. This
	// handles deployments whose autodiscover responder does not speak the
	// EAS mobilesync request schema (e.g. SOGo, which historically only
	// implements the Outlook schema).
	SkipWellKnownFallback bool
	// SRVLookup is the DNS SRV resolver. Defaults to net.DefaultResolver.
	// Tests inject a stub.
	SRVLookup func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

// Autodiscover queries the standard EAS Autodiscover endpoints for the
// given email address and returns the first successful response.
//
// EAS Autodiscover ([MS-ASCMD] §2.2.2.1; the protocol predates XML
// namespacing conventions and uses raw element names) is an XML POST that
// returns the EAS endpoint URL. The full discovery flow tries four
// candidates in order, matching Outlook's behavior:
//
//  1. POST https://<domain>/Autodiscover/Autodiscover.xml
//  2. POST https://autodiscover.<domain>/Autodiscover/Autodiscover.xml
//  3. GET  http://autodiscover.<domain>/Autodiscover/Autodiscover.xml
//     — expecting a 302 redirect to a https://… URL we then POST to.
//  4. SRV  _autodiscover._tcp.<domain> — query DNS for the host:port
//     to POST to.
//  5. OPTIONS https://<domain>/Microsoft-Server-ActiveSync (and the
//     autodiscover.<domain> / mail.<domain> variants) — accept any 2xx
//     response carrying an EAS server header. This is a last-resort
//     fallback for deployments whose autodiscover service does not
//     speak the mobilesync schema.
//
// Each step can be disabled via AutodiscoverOptions. The password is
// required because Autodiscover responses are authenticated; this
// function does not store the password.
func Autodiscover(ctx context.Context, email, password string, opts AutodiscoverOptions) (*AutodiscoverResult, error) {
	if email == "" {
		return nil, errors.New("eas: Autodiscover: email is required")
	}
	domain := domainOf(email)
	if domain == "" {
		return nil, fmt.Errorf("eas: Autodiscover: %q has no domain", email)
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "go-activesync/0.1"
	}

	body, err := buildAutodiscoverRequest(email)
	if err != nil {
		return nil, err
	}
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+password))

	tryEndpoint := func(ep string) (*AutodiscoverResult, error) {
		return autodiscoverPost(ctx, hc, ua, auth, ep, body)
	}

	// Steps 1 + 2: explicit endpoints (or caller override).
	endpoints := opts.Endpoints
	if endpoints == nil {
		endpoints = autodiscoverEndpointsFor(domain)
	}
	var lastErr error
	for _, ep := range endpoints {
		logger.Debug("autodiscover try", "step", "post", "endpoint", ep)
		out, err := tryEndpoint(ep)
		if err == nil {
			return out, nil
		}
		lastErr = fmt.Errorf("%s: %w", ep, err)
	}

	// Step 3: HTTP GET, follow the 302.
	if !opts.SkipHTTPRedirect && opts.Endpoints == nil {
		if redirected, err := autodiscoverFollowRedirect(ctx, hc, ua, domain, logger); err == nil {
			logger.Debug("autodiscover try", "step", "post-after-redirect", "endpoint", redirected)
			out, err := tryEndpoint(redirected)
			if err == nil {
				return out, nil
			}
			lastErr = fmt.Errorf("redirect %s: %w", redirected, err)
		} else {
			lastErr = fmt.Errorf("redirect: %w", err)
		}
	}

	// Step 4: SRV record fallback.
	if !opts.SkipSRVLookup && opts.Endpoints == nil {
		lookup := opts.SRVLookup
		if lookup == nil {
			lookup = net.DefaultResolver.LookupSRV
		}
		_, addrs, err := lookup(ctx, "autodiscover", "tcp", domain)
		if err != nil {
			lastErr = fmt.Errorf("srv lookup: %w", err)
		} else {
			for _, srv := range addrs {
				host := strings.TrimSuffix(srv.Target, ".")
				ep := fmt.Sprintf("https://%s/Autodiscover/Autodiscover.xml", host)
				if srv.Port != 0 && srv.Port != 443 {
					ep = fmt.Sprintf("https://%s:%d/Autodiscover/Autodiscover.xml", host, srv.Port)
				}
				logger.Debug("autodiscover try", "step", "srv", "endpoint", ep)
				out, err := tryEndpoint(ep)
				if err == nil {
					return out, nil
				}
				lastErr = fmt.Errorf("%s: %w", ep, err)
			}
		}
	}

	// Step 5: well-known EAS path probe via OPTIONS. Only runs when the
	// caller hasn't pinned an explicit endpoint list.
	if !opts.SkipWellKnownFallback && opts.Endpoints == nil {
		out, err := autodiscoverWellKnown(ctx, hc, ua, auth, domain, logger)
		if err == nil {
			return out, nil
		}
		lastErr = fmt.Errorf("well-known: %w", err)
	}

	if lastErr == nil {
		lastErr = errors.New("no endpoints attempted")
	}
	return nil, fmt.Errorf("eas: Autodiscover: %w", lastErr)
}

// autodiscoverWellKnown probes the canonical EAS path with HTTP OPTIONS
// across a small set of plausible hostnames. Used when the
// schema-aware autodiscover responder is missing or doesn't understand
// the mobilesync request schema (e.g. SOGo).
func autodiscoverWellKnown(ctx context.Context, hc *http.Client, ua, auth, domain string, logger *slog.Logger) (*AutodiscoverResult, error) {
	hosts := []string{
		domain,
		"autodiscover." + domain,
		"mail." + domain,
	}
	var lastErr error
	for _, host := range hosts {
		ep := "https://" + host + "/Microsoft-Server-ActiveSync"
		logger.Debug("autodiscover try", "step", "well-known", "endpoint", ep)
		req, err := http.NewRequestWithContext(ctx, http.MethodOptions, ep, nil)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", ep, err)
			continue
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Authorization", auth)
		resp, err := hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", ep, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 &&
			(resp.Header.Get("MS-Server-ActiveSync") != "" ||
				resp.Header.Get("MS-ASProtocolVersions") != "") {
			u, _ := url.Parse(ep)
			return &AutodiscoverResult{
				URL:            ep,
				ServerHostname: u.Hostname(),
			}, nil
		}
		lastErr = fmt.Errorf("%s: HTTP %d (no EAS server header)", ep, resp.StatusCode)
	}
	if lastErr == nil {
		lastErr = errors.New("no well-known candidates probed")
	}
	return nil, lastErr
}

// autodiscoverPost posts the Autodiscover XML body to one URL and
// parses the response.
func autodiscoverPost(ctx context.Context, hc *http.Client, ua, auth, ep string, body []byte) (*AutodiscoverResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("Accept", "text/xml")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Authorization", auth)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			URL:        ep,
			Body:       capBytes(respBody, 4<<10),
		}
	}
	return parseAutodiscoverResponse(respBody)
}

// autodiscoverFollowRedirect issues an HTTP GET to
// http://autodiscover.<domain>/Autodiscover/Autodiscover.xml and returns
// the Location header from a 302 response. We deliberately use a
// non-redirect-following client so we can inspect the Location.
func autodiscoverFollowRedirect(ctx context.Context, hc *http.Client, ua, domain string, logger *slog.Logger) (string, error) {
	ep := fmt.Sprintf("http://autodiscover.%s/Autodiscover/Autodiscover.xml", domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ua)

	// Use a clone of the configured client that doesn't follow
	// redirects; we want the raw Location header.
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently &&
		resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusTemporaryRedirect &&
		resp.StatusCode != http.StatusPermanentRedirect {
		return "", fmt.Errorf("expected redirect, got HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("redirect missing Location header")
	}
	logger.Debug("autodiscover redirect", "from", ep, "to", loc)
	if !strings.HasPrefix(loc, "https://") {
		// Spec-mandated: clients refuse to downgrade from a redirect.
		return "", fmt.Errorf("redirect target %q is not https", loc)
	}
	return loc, nil
}

func autodiscoverEndpointsFor(domain string) []string {
	return []string{
		"https://" + domain + "/Autodiscover/Autodiscover.xml",
		"https://autodiscover." + domain + "/Autodiscover/Autodiscover.xml",
	}
}

func domainOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// --- Autodiscover XML wire types --------------------------------------

const autodiscoverRespNS = "http://schemas.microsoft.com/exchange/autodiscover/mobilesync/responseschema/2006"

type adRequest struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/autodiscover/mobilesync/requestschema/2006 Autodiscover"`
	Request struct {
		EMailAddress    string `xml:"EMailAddress"`
		AcceptableRespS string `xml:"AcceptableResponseSchema"`
	} `xml:"Request"`
}

type adResponse struct {
	XMLName  xml.Name `xml:"Autodiscover"`
	Response struct {
		User struct {
			DisplayName  string `xml:"DisplayName"`
			EMailAddress string `xml:"EMailAddress"`
		} `xml:"User"`
		Action struct {
			Settings struct {
				Server []struct {
					Type string `xml:"Type"`
					URL  string `xml:"Url"`
					Name string `xml:"Name"`
				} `xml:"Server"`
			} `xml:"Settings"`
			Redirect string `xml:"Redirect"`
			Error    struct {
				Status  string `xml:"Status"`
				Message string `xml:"Message"`
			} `xml:"Error"`
		} `xml:"Action"`
	} `xml:"Response"`
}

func buildAutodiscoverRequest(email string) ([]byte, error) {
	var r adRequest
	r.Request.EMailAddress = email
	r.Request.AcceptableRespS = autodiscoverRespNS
	out, err := xml.Marshal(&r)
	if err != nil {
		return nil, fmt.Errorf("autodiscover: marshal request: %w", err)
	}
	return append([]byte(xml.Header), out...), nil
}

func parseAutodiscoverResponse(body []byte) (*AutodiscoverResult, error) {
	var r adResponse
	if err := xml.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("autodiscover: parse: %w", err)
	}
	if r.Response.Action.Error.Status != "" {
		return nil, fmt.Errorf("autodiscover: server error %s: %s",
			r.Response.Action.Error.Status, r.Response.Action.Error.Message)
	}
	for _, s := range r.Response.Action.Settings.Server {
		if strings.EqualFold(s.Type, "MobileSync") {
			res := &AutodiscoverResult{
				URL:         s.URL,
				DisplayName: r.Response.User.DisplayName,
			}
			if u, err := url.Parse(s.URL); err == nil {
				res.ServerHostname = u.Hostname()
			}
			return res, nil
		}
	}
	return nil, errors.New("autodiscover: response contained no MobileSync server entry")
}
