// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestOptions_parsesHeaders(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("MS-ASProtocolVersions", "2.5,12.0,12.1,14.0,14.1")
		w.Header().Set("MS-ASProtocolCommands", "Sync,FolderSync,Provision,SendMail")
		w.WriteHeader(http.StatusOK)
	})
	got, err := c.Options(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != http.MethodOptions {
		t.Errorf("method = %q, want OPTIONS", cap.method)
	}
	wantVersions := []string{"2.5", "12.0", "12.1", "14.0", "14.1"}
	if !reflect.DeepEqual(got.ProtocolVersions, wantVersions) {
		t.Errorf("versions = %v", got.ProtocolVersions)
	}
	wantCmds := []string{"Sync", "FolderSync", "Provision", "SendMail"}
	if !reflect.DeepEqual(got.Commands, wantCmds) {
		t.Errorf("commands = %v", got.Commands)
	}
	if !got.Supports("14.1") {
		t.Error("Supports(14.1) = false")
	}
	if got.Supports("99.9") {
		t.Error("Supports(99.9) = true")
	}
	if !got.HasCommand("FolderSync") {
		t.Error("HasCommand(FolderSync) = false")
	}
	if got.HasCommand("Wat") {
		t.Error("HasCommand(Wat) = true")
	}
}

func TestOptions_HTTPError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	})
	_, err := c.Options(context.Background())
	if !IsHTTPStatus(err, 403) {
		t.Errorf("err = %v", err)
	}
}

func TestSupportsAndHasCommand_nilSafe(t *testing.T) {
	var o *OptionsResult
	if o.Supports("14.1") {
		t.Error("nil should not support anything")
	}
	if o.HasCommand("X") {
		t.Error("nil should not have any command")
	}
}

func TestNegotiateVersion_picksHighest(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("MS-ASProtocolVersions", "12.1,14.0,14.1,16.0")
		w.WriteHeader(200)
	})
	v, err := c.NegotiateVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "14.1" {
		t.Errorf("got %q, want 14.1 (preferred)", v)
	}
	if c.cfg.ASVersion != "14.1" {
		t.Errorf("cfg.ASVersion = %q", c.cfg.ASVersion)
	}
}

func TestNegotiateVersion_fallsBackTo12(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("MS-ASProtocolVersions", "12.0,12.1")
		w.WriteHeader(200)
	})
	v, err := c.NegotiateVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "12.1" {
		t.Errorf("got %q, want 12.1", v)
	}
}

func TestNegotiateVersion_optionsErrorReturnsCurrent(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", 500)
	})
	v, err := c.NegotiateVersion(context.Background())
	if err == nil {
		t.Error("want error from broken OPTIONS")
	}
	if v != "14.1" {
		t.Errorf("got %q, want unchanged 14.1 default", v)
	}
}

func TestNegotiateVersion_keepsCurrentWhenOnlyServerHasIt(t *testing.T) {
	// Server lists only "99.9" — none of supportedVersions match, but the
	// client's current ASVersion is in the server's list, so the post-loop
	// fallback should keep it.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("MS-ASProtocolVersions", "99.9")
		w.WriteHeader(200)
	})
	c.cfg.ASVersion = "99.9"
	v, err := c.NegotiateVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "99.9" {
		t.Errorf("got %q, want 99.9 (kept current)", v)
	}
}

func TestNegotiateVersion_noOverlapErrors(t *testing.T) {
	// Server lists only versions we can't speak, and the client's current
	// version isn't in that list either. Must error.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("MS-ASProtocolVersions", "8.0,9.0")
		w.WriteHeader(200)
	})
	v, err := c.NegotiateVersion(context.Background())
	if err == nil {
		t.Errorf("want error, got version %q", v)
	}
	if v != "14.1" {
		t.Errorf("kept version = %q (should be unchanged default)", v)
	}
}

func TestOptions_buildRequestError(t *testing.T) {
	// A control character (\x7f) in the URL forces http.NewRequestWithContext
	// to return an error, exercising the build-request branch.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {})
	bad, _ := url.Parse("https://x/path")
	bad.Path = "/bad\x7fpath"
	c.baseURL = bad
	if _, err := c.Options(context.Background()); err == nil {
		t.Error("want error from invalid URL")
	}
}

func TestSplitCSV(t *testing.T) {
	if got := splitCSV(""); got != nil {
		t.Errorf("empty: %v", got)
	}
	if got := splitCSV("a, b ,c"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("got %v", got)
	}
}
