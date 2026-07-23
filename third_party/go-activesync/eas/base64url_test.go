// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestBuildBase64Query_layout(t *testing.T) {
	encoded, err := buildBase64Query("14.1", "FolderSync", "DEV1234", "DEV", "POL")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw[0] != 141 {
		t.Errorf("version byte = %d", raw[0])
	}
	if raw[1] != commandTokens["FolderSync"] {
		t.Errorf("cmd byte = %d", raw[1])
	}
	if raw[2] != 0 || raw[3] != 0 {
		t.Errorf("locale = %d,%d", raw[2], raw[3])
	}
	// device id length + value
	idLen := raw[4]
	if int(idLen) != len("DEV1234") {
		t.Errorf("idLen = %d", idLen)
	}
	if string(raw[5:5+idLen]) != "DEV1234" {
		t.Errorf("device id = %q", raw[5:5+idLen])
	}
	// device type length + value
	off := 5 + int(idLen)
	dtLen := raw[off]
	if int(dtLen) != len("DEV") {
		t.Errorf("dtLen = %d", dtLen)
	}
	if string(raw[off+1:off+1+int(dtLen)]) != "DEV" {
		t.Errorf("device type = %q", raw[off+1:off+1+int(dtLen)])
	}
	off += 1 + int(dtLen)
	pkLen := raw[off]
	if int(pkLen) != len("POL") {
		t.Errorf("pkLen = %d", pkLen)
	}
	if string(raw[off+1:off+1+int(pkLen)]) != "POL" {
		t.Errorf("policy key = %q", raw[off+1:off+1+int(pkLen)])
	}
}

func TestBuildBase64Query_unknownCommand(t *testing.T) {
	if _, err := buildBase64Query("14.1", "Frobnicate", "x", "M", ""); err == nil {
		t.Error("want error for unknown command")
	}
}

func TestBuildBase64Query_oversizedField(t *testing.T) {
	big := strings.Repeat("X", 256)
	if _, err := buildBase64Query("14.1", "FolderSync", big, "M", ""); err == nil {
		t.Error("want error for oversized field")
	}
}

func TestEncodeVersion(t *testing.T) {
	cases := map[string]byte{
		"12.0": 120, "12.1": 121,
		"14.0": 140, "14.1": 141,
		"16.0": 160, "16.1": 161,
		"":      141, // fallback
		"99":    99,
		"abcde": 141, // unparseable → fallback
	}
	for in, want := range cases {
		if got := encodeVersion(in); got != want {
			t.Errorf("encodeVersion(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestApplyB64ToURL_stripsExistingQuery(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {})
	out, err := c.applyB64ToURL("FolderSync",
		"https://x/Microsoft-Server-ActiveSync?Cmd=FolderSync&User=u")
	if err != nil {
		t.Fatal(err)
	}
	// One '?' in result, no '&' (single base64 param replaces the original).
	if strings.Count(out, "?") != 1 {
		t.Errorf("expected exactly one '?': %q", out)
	}
	if strings.Contains(out, "&") {
		t.Errorf("expected no '&': %q", out)
	}
}

func TestApplyB64ToURL_stateError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), policyKeyErr: errSentinel("boom")}
	_, err := c.applyB64ToURL("FolderSync", "https://x/Microsoft-Server-ActiveSync")
	if err == nil {
		t.Error("want error from PolicyKey lookup")
	}
}

func TestApplyB64ToURL_buildError(t *testing.T) {
	// DeviceID > 255 bytes forces buildBase64Query to fail; applyB64ToURL
	// must surface that error.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {})
	c.cfg.DeviceID = strings.Repeat("X", 256)
	_, err := c.applyB64ToURL("FolderSync", "https://x/")
	if err == nil {
		t.Error("want error from buildBase64Query")
	}
}

func TestCommandURL_base64Mode(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	c.cfg.Base64URL = true

	if _, err := c.postRaw(context.Background(), "FolderSync", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if cap.url == nil {
		t.Fatal("captured URL is nil")
	}
	q := cap.url.RawQuery
	if q == "" {
		t.Fatal("query empty")
	}
	if strings.Contains(q, "&") {
		t.Errorf("base64 mode should produce a single query parameter, got %q", q)
	}
	// Decode and confirm command byte.
	raw, err := base64.RawURLEncoding.DecodeString(q)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw[1] != commandTokens["FolderSync"] {
		t.Errorf("cmd byte = %d", raw[1])
	}
}
