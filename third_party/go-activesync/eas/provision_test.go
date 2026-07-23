// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

// fakeProvisionServer responds to two consecutive Provision requests with
// (TempKey="TKEY") then (FinalKey="FKEY"). It records the X-MS-PolicyKey
// header sent on each call so the test can verify the client uses the
// temp key on the second request.
type fakeProvisionServer struct {
	mu         sync.Mutex
	calls      int
	policyKeys []string
	tempKey    string
	finalKey   string
}

func (f *fakeProvisionServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.policyKeys = append(f.policyKeys, r.Header.Get("X-MS-PolicyKey"))
	call := f.calls
	f.mu.Unlock()

	var key string
	switch call {
	case 1:
		key = f.tempKey
	case 2:
		key = f.finalKey
	}
	resp := &wbxml.Document{
		Root: wbxml.E(wbxml.PageProvision, "Provision",
			wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageProvision, "Policies",
				wbxml.E(wbxml.PageProvision, "Policy",
					wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
					wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text(key)),
				),
			),
		),
	}
	body, err := wbxml.Marshal(resp, wbxml.DefaultRegistry())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
	w.Write(body)
}

func TestProvision_twoPhaseHandshake(t *testing.T) {
	f := &fakeProvisionServer{tempKey: "TKEY", finalKey: "FKEY"}
	c, _, _ := newTestClient(t, f.handle)
	if err := c.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Errorf("calls = %d, want 2", f.calls)
	}
	if f.policyKeys[0] != "" {
		t.Errorf("call 1 X-MS-PolicyKey: %q, want empty", f.policyKeys[0])
	}
	if f.policyKeys[1] != "TKEY" {
		t.Errorf("call 2 X-MS-PolicyKey: %q, want TKEY", f.policyKeys[1])
	}
	pk, _ := c.cfg.State.PolicyKey(context.Background())
	if pk != "FKEY" {
		t.Errorf("persisted policy key: %q, want FKEY", pk)
	}
}

func TestProvision_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := &wbxml.Document{
			Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("142")), // PolicyRefresh
			),
		}
		body, _ := wbxml.Marshal(resp, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	err := c.Provision(context.Background())
	if !IsStatusCode(err, 142) {
		t.Errorf("err = %v", err)
	}
}

func TestRemoteWipeRequested(t *testing.T) {
	if RemoteWipeRequested(nil) {
		t.Error("nil doc should be false")
	}
	if RemoteWipeRequested(&wbxml.Document{}) {
		t.Error("nil root should be false")
	}
	noWipe := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
		wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
	)}
	if RemoteWipeRequested(noWipe) {
		t.Error("absent <RemoteWipe> should be false")
	}
	withWipe := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
		wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "RemoteWipe"),
	)}
	if !RemoteWipeRequested(withWipe) {
		t.Error("present <RemoteWipe> should be true")
	}
}

func TestAcknowledgeRemoteWipe_emitsStatus(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
			wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.AcknowledgeRemoteWipe(context.Background(), 0); err != nil { // 0 → defaults to 1
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	wipe := req.Root.Find("RemoteWipe")
	if wipe == nil {
		t.Fatal("missing <RemoteWipe>")
	}
	if st := wipe.Find("Status"); st == nil || st.TextContent() != "1" {
		t.Errorf("Status = %v, want 1 (default)", st)
	}
}

func TestAcknowledgeRemoteWipe_serverError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
			wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("3")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.AcknowledgeRemoteWipe(context.Background(), 2); err == nil {
		t.Error("want error for non-OK status")
	}
}

func TestProvision_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := c.Provision(context.Background()); err == nil {
		t.Error("want HTTP error")
	}
}

func TestProvision_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → nil resp
	})
	if err := c.Provision(context.Background()); err == nil {
		t.Error("want error on empty response")
	}
}

func TestProvision_missingPolicyElement(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Status=1 but no Policy element at all.
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
			wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	err := c.Provision(context.Background())
	if err == nil || !strings.Contains(err.Error(), "<Policy>") {
		t.Errorf("err = %v", err)
	}
}

func TestProvision_policyStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
			wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageProvision, "Policies",
				wbxml.E(wbxml.PageProvision, "Policy",
					wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("X")),
					wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("3")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.Provision(context.Background()); !IsStatusCode(err, 3) {
		t.Errorf("err = %v", err)
	}
}

func TestProvision_persistTempKeyError(t *testing.T) {
	// Phase 1 succeeds with a temp key but SetPolicyKey fails before phase 2.
	calls := 0
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
			wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageProvision, "Policies",
				wbxml.E(wbxml.PageProvision, "Policy",
					wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("X")),
					wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text("TKEY")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), setPolicyErr: errSentinel("ro")}
	err := c.Provision(context.Background())
	if err == nil || !strings.Contains(err.Error(), "persist temp key") {
		t.Errorf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no phase 2 after persist failure)", calls)
	}
}

func TestProvision_phase2EmptyKey(t *testing.T) {
	// Phase 1 returns a temp key; phase 2 returns success but no key.
	f := &fakeProvisionServer{tempKey: "TKEY", finalKey: ""}
	c, _, _ := newTestClient(t, f.handle)
	err := c.Provision(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no final policy key") {
		t.Errorf("err = %v", err)
	}
}

func TestProvision_phase2ServerError(t *testing.T) {
	// Phase 1 returns a temp key; phase 2 fails with a transport error.
	calls := 0
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageProvision, "Policies",
					wbxml.E(wbxml.PageProvision, "Policy",
						wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("X")),
						wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text("TKEY")),
					),
				),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
			w.Write(body)
			return
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	err := c.Provision(context.Background())
	if err == nil || !strings.Contains(err.Error(), "phase 2") {
		t.Errorf("err = %v", err)
	}
}

func TestProvision_persistFinalKeyError(t *testing.T) {
	// Phase 1 + 2 succeed but the final SetPolicyKey fails.
	f := &fakeProvisionServer{tempKey: "TKEY", finalKey: "FKEY"}
	c, _, _ := newTestClient(t, f.handle)
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	calls := 0
	prevSet := es.setPolicyErr
	// Allow the temp-key persist to succeed, fail on the final key.
	es.setPolicyErr = nil
	es.inner = NewMemoryState()
	_ = prevSet
	// Wrap PolicyKey accept to count calls and trip on the second.
	originalSet := func(ctx context.Context, k string) error {
		calls++
		if calls >= 2 {
			return errSentinel("ro")
		}
		return es.inner.SetPolicyKey(ctx, k)
	}
	c.cfg.State = &countingState{inner: NewMemoryState(), setPolicyKey: originalSet}
	err := c.Provision(context.Background())
	if err == nil || !strings.Contains(err.Error(), "persist final key") {
		t.Errorf("err = %v", err)
	}
}

// countingState is a StateStore whose methods can be overridden per call.
type countingState struct {
	inner        StateStore
	setPolicyKey func(ctx context.Context, k string) error
}

func (s *countingState) PolicyKey(ctx context.Context) (string, error) {
	return s.inner.PolicyKey(ctx)
}

func (s *countingState) SetPolicyKey(ctx context.Context, k string) error {
	if s.setPolicyKey != nil {
		return s.setPolicyKey(ctx, k)
	}
	return s.inner.SetPolicyKey(ctx, k)
}

func (s *countingState) SyncKey(ctx context.Context, fid string) (string, error) {
	return s.inner.SyncKey(ctx, fid)
}

func (s *countingState) SetSyncKey(ctx context.Context, fid, k string) error {
	return s.inner.SetSyncKey(ctx, fid, k)
}

func TestAcknowledgeRemoteWipe_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := c.AcknowledgeRemoteWipe(context.Background(), 1); err == nil {
		t.Error("want HTTP error")
	}
}

func TestAcknowledgeRemoteWipe_emptyResponseIsSuccess(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → success
	})
	if err := c.AcknowledgeRemoteWipe(context.Background(), 1); err != nil {
		t.Errorf("empty 200 OK should be success, got %v", err)
	}
}

func TestProvision_missingPolicyKey(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := &wbxml.Document{
			Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageProvision, "Policies",
					wbxml.E(wbxml.PageProvision, "Policy",
						wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("X")),
						wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
						// No PolicyKey
					),
				),
			),
		}
		body, _ := wbxml.Marshal(resp, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	err := c.Provision(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no temporary policy key") {
		t.Errorf("err = %v", err)
	}
}
