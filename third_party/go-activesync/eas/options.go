// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// OptionsResult reports what a server says it supports in response to an
// HTTP OPTIONS request. EAS servers advertise this via two non-standard
// response headers; both are comma-separated.
type OptionsResult struct {
	// ProtocolVersions are the EAS protocol versions the server supports
	// (e.g. ["2.5", "12.0", "12.1", "14.0", "14.1"]).
	ProtocolVersions []string
	// Commands are the EAS commands the server accepts (e.g. ["Sync",
	// "FolderSync", "Provision", ...]).
	Commands []string
}

// Supports reports whether the server claims to support the given protocol
// version (e.g. "14.1").
func (o *OptionsResult) Supports(version string) bool {
	if o == nil {
		return false
	}
	return slices.Contains(o.ProtocolVersions, version)
}

// HasCommand reports whether the server claims to support the given command.
func (o *OptionsResult) HasCommand(name string) bool {
	if o == nil {
		return false
	}
	return slices.Contains(o.Commands, name)
}

// supportedVersions is the priority-ordered list of EAS versions this
// client speaks. Negotiation picks the first one the server also lists.
var supportedVersions = []string{"14.1", "14.0", "16.1", "16.0", "12.1", "12.0"}

// NegotiateVersion runs OPTIONS and updates the client's protocol
// version to the highest both parties support, with 14.1 preferred.
// Returns the chosen version (or the existing one when negotiation
// found no overlap and the existing one is already supported by the
// server).
//
// Call once before any WBXML command if the server's protocol set is
// unknown. The Manager calls this transparently before the first
// Provision exchange.
func (c *httpClient) NegotiateVersion(ctx context.Context) (string, error) {
	opts, err := c.Options(ctx)
	if err != nil {
		return c.cfg.ASVersion, err
	}
	for _, v := range supportedVersions {
		if opts.Supports(v) {
			c.cfg.ASVersion = v
			c.cfg.Logger.Debug("eas: negotiated protocol version", "version", v)
			return v, nil
		}
	}
	if opts.Supports(c.cfg.ASVersion) {
		return c.cfg.ASVersion, nil
	}
	return c.cfg.ASVersion, fmt.Errorf("eas: server supports versions %v, none in our preferred set", opts.ProtocolVersions)
}

// Options issues an HTTP OPTIONS request to discover the server's
// capabilities. No authentication or policy key is required by the spec
// for OPTIONS, but in practice servers refuse anonymous OPTIONS so we send
// Authorization anyway.
func (c *httpClient) Options(ctx context.Context) (*OptionsResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, c.baseURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("eas: OPTIONS: build request: %w", err)
	}
	resp, err := c.httpDo(req)
	if err != nil {
		return nil, fmt.Errorf("eas: OPTIONS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			URL:        c.baseURL.String(),
		}
	}
	return &OptionsResult{
		ProtocolVersions: splitCSV(resp.Header.Get("MS-ASProtocolVersions")),
		Commands:         splitCSV(resp.Header.Get("MS-ASProtocolCommands")),
	}, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
