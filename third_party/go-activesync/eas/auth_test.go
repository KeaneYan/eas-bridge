// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestAuthHeader_overridesBasic(t *testing.T) {
	var seen string
	srv := newAuthHeaderCapturingClient(t, &seen)
	srv.cfg.AuthHeader = func(_ context.Context) (string, error) {
		return "Bearer my-token", nil
	}

	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	if _, err := srv.post(context.Background(), "FolderSync", doc); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer my-token" {
		t.Errorf("auth header = %q", seen)
	}
}

func TestAuthHeader_errorPropagated(t *testing.T) {
	srv, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	srv.cfg.AuthHeader = func(_ context.Context) (string, error) {
		return "", errAuth
	}
	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	_, err := srv.post(context.Background(), "FolderSync", doc)
	if err == nil {
		t.Error("want error")
	}
}

var errAuth = errFromString("auth source unreachable")

type errString string

func (e errString) Error() string { return string(e) }

func errFromString(s string) error { return errString(s) }

func TestRetryOn401_refreshesAuth(t *testing.T) {
	var calls int32
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(200)
	})
	c.cfg.AuthHeader = func(_ context.Context) (string, error) {
		return "Bearer t" + itoa(int(atomic.LoadInt32(&calls))), nil
	}
	c.cfg.RetryOn401 = true

	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	if _, err := c.post(context.Background(), "FolderSync", doc); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2", got)
	}
}

// TestRetryOn449_reprovisionsAndRetries exercises the MS-ASPROV §3.1.5.2
// recovery path: when a non-Provision command receives HTTP 449, the
// client must transparently re-Provision and retry the original command
// once. Without this, every PolicyKey expiry would surface to the
// caller as a hard failure.
func TestRetryOn449_reprovisionsAndRetries(t *testing.T) {
	var calls int32
	// The transcript on the wire is:
	//   call 1: original FolderSync → 449
	//   call 2: Provision (phase 1) → temp policy + key
	//   call 3: Provision (phase 2 ack) → final policy key
	//   call 4: original FolderSync retried → 200
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch n {
		case 1:
			w.WriteHeader(449)
		case 2:
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageProvision, "Policies",
					wbxml.E(wbxml.PageProvision, "Policy",
						wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
						wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text("temp")),
						wbxml.E(wbxml.PageProvision, "Data",
							wbxml.E(wbxml.PageProvision, "EASProvisionDoc"),
						),
					),
				),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(body)
		case 3:
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageProvision, "Policies",
					wbxml.E(wbxml.PageProvision, "Policy",
						wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
						wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text("FINAL")),
					),
				),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(body)
		default:
			w.WriteHeader(200)
		}
	})
	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	if _, err := c.post(context.Background(), "FolderSync", doc); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Errorf("calls = %d, want 4 (449 → 2× Provision → retry)", got)
	}
}

func TestRetryOn401_disabledByDefault(t *testing.T) {
	var calls int32
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	// AuthHeader not set ⇒ pre-built Basic header used; retry only fires
	// when both AuthHeader and RetryOn401 are set.
	doc := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	if _, err := c.post(context.Background(), "FolderSync", doc); err == nil {
		t.Error("want 401 error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry)", got)
	}
}

// newAuthHeaderCapturingClient builds a Client whose handler stores the
// most-recent Authorization header in *seen.
func newAuthHeaderCapturingClient(t *testing.T, seen *string) *httpClient {
	t.Helper()
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		*seen = r.Header.Get("Authorization")
		w.WriteHeader(200)
	})
	return c
}
