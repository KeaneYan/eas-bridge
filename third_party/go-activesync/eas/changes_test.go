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

func changeAck(syncKey string, statuses ...struct {
	id     string
	status string
}) []byte {
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(syncKey)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
		wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
	)
	if len(statuses) > 0 {
		responses := wbxml.E(wbxml.PageAirSync, "Responses")
		for _, s := range statuses {
			responses.Children = append(responses.Children,
				wbxml.E(wbxml.PageAirSync, "Change",
					wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(s.id)),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text(s.status)),
				),
			)
		}
		collection.Children = append(collection.Children, responses)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	out, err := wbxml.Marshal(doc, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return out
}

// helper to take a pointer to a bool literal in tests.
func boolp(b bool) *bool { return &b }

func TestApplyEmailChanges_setRead(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("S2"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")

	if _, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "inbox:1", Read: boolp(true)},
	}); err != nil {
		t.Fatal(err)
	}
	// Verify Sync key advanced.
	if k, _ := c.cfg.State.SyncKey(context.Background(), "inbox"); k != "S2" {
		t.Errorf("sync key not advanced: %q", k)
	}
	// Verify request shape: Change with ServerId + ApplicationData.Read=1
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	change := req.Root.Find("Change")
	if change == nil {
		t.Fatal("Change missing")
	}
	if r := change.Find("Read"); r == nil || r.TextContent() != "1" {
		t.Errorf("Read = %v", r)
	}
}

func TestApplyEmailChanges_setFlagged(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("S2"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Flagged: boolp(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if fs := req.Root.Find("FlagStatus"); fs == nil || fs.TextContent() != "2" {
		t.Errorf("FlagStatus = %v", fs)
	}
}

func TestApplyEmailChanges_delete(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("S2"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "doomed", Delete: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	del := req.Root.Find("Delete")
	if del == nil {
		t.Fatal("Delete missing")
	}
	if id := del.Find("ServerId"); id == nil || id.TextContent() != "doomed" {
		t.Errorf("ServerId = %v", id)
	}
	// Should NOT have a Change element when only deletes are sent.
	if req.Root.Find("Change") != nil {
		t.Error("unexpected Change present")
	}
}

func TestApplyEmailChanges_bootstrapsIfNeeded(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    int
		sentKeys []string
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		req, _ := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
		mu.Lock()
		calls++
		thisCall := calls
		sentKeys = append(sentKeys, req.Root.Find("SyncKey").TextContent())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch thisCall {
		case 1:
			// bootstrap: no items, just a new key
			w.Write(changeAck("BOOT"))
		case 2:
			w.Write(changeAck("DONE"))
		}
	})

	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d", calls)
	}
	if sentKeys[0] != "0" || sentKeys[1] != "BOOT" {
		t.Errorf("sentKeys = %v", sentKeys)
	}
}

func TestApplyEmailChanges_perItemStatus(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("S2",
			struct{ id, status string }{"a", "1"},
			struct{ id, status string }{"b", "8"}, // ObjectNotFound
		))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")

	res, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "a", Read: boolp(true)},
		{ServerID: "b", Read: boolp(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("len = %d", len(res))
	}
	if res[0].Status != 1 || res[1].Status != 8 {
		t.Errorf("results = %+v", res)
	}
}

// TestApplyEmailChanges_invalidKeyResetsAndRetries: server responds with
// Status=3 once, the client must reset the per-folder key, re-bootstrap,
// and replay the change command transparently.
func TestApplyEmailChanges_invalidKeyResetsAndRetries(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		this := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch this {
		case 1: // first applyChangesOnce → Status=3
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(body)
		default: // bootstrap re-Sync after reset, then replay succeeds
			w.Write(changeAck("RESET-" + itoa(this)))
		}
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "STALE")
	read := true
	if _, err := c.ApplyEmailChanges(context.Background(), "inbox",
		[]EmailChange{{ServerID: "m-1", Read: &read}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 3 {
		t.Errorf("calls = %d, want 3 (status3 → bootstrap → replay)", got)
	}
}

// TestApplyEmailChanges_allEmptyIsError: a change with no fields set and
// no Delete is silently skipped. With only such changes, the call must
// error rather than emit a no-op Sync.
func TestApplyEmailChanges_allEmptyIsError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Only the bootstrap may hit; the change-emit path must short-circuit.
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("BOOT"))
	})
	// Skip bootstrap by pre-populating a key.
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "K1")
	if _, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "m-1"}, // no Read/Flag/Delete set
	}); err == nil {
		t.Error("want 'no effective changes' error")
	}
}

// TestApplyEmailChanges_unflaggedSendsFlagStatusZero: passing
// Flagged=&false must emit FlagStatus=0 (the explicit "clear flag"
// instruction).
func TestApplyEmailChanges_unflaggedSendsFlagStatusZero(t *testing.T) {
	var lastBody []byte
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		lastBody = body
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("DONE"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "K1")
	flagged := false
	if _, err := c.ApplyEmailChanges(context.Background(), "inbox",
		[]EmailChange{{ServerID: "m-1", Flagged: &flagged}}); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(lastBody, wbxml.DefaultRegistry())
	if fs := req.Root.Find("FlagStatus"); fs == nil || fs.TextContent() != "0" {
		t.Errorf("FlagStatus = %v, want 0", fs)
	}
}

// TestApplyEmailChanges_explicitFlagStatus: SetFlagStatus takes
// precedence and is sent verbatim regardless of Flagged.
func TestApplyEmailChanges_explicitFlagStatus(t *testing.T) {
	var lastBody []byte
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		lastBody = body
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("DONE"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "K1")
	complete := 1 // 1 = complete
	if _, err := c.ApplyEmailChanges(context.Background(), "inbox",
		[]EmailChange{{ServerID: "m-1", SetFlagStatus: &complete}}); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(lastBody, wbxml.DefaultRegistry())
	if fs := req.Root.Find("FlagStatus"); fs == nil || fs.TextContent() != "1" {
		t.Errorf("FlagStatus = %v, want 1", fs)
	}
}

func TestApplyEmailChanges_skipsEmptyServerID(t *testing.T) {
	// One valid change + one with empty ServerID; only the valid one
	// should hit the wire.
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("S2"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "", Read: boolp(true)}, // dropped
		{ServerID: "real", Read: boolp(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	changes := req.Root.FindAll("Change")
	if len(changes) != 1 {
		t.Errorf("got %d Change elements, want 1", len(changes))
	}
}

func TestApplyEmailChanges_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err == nil {
		t.Error("want HTTP error")
	}
}

func TestApplyEmailChanges_emptyResponseIsSuccess(t *testing.T) {
	// Per MS-ASCMD §2.2.2.20, an empty 200 OK after Sync indicates the
	// server applied the changes without per-item status. Caller should
	// see (nil, nil).
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	res, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err != nil {
		t.Errorf("want no error, got %v", err)
	}
	if res != nil {
		t.Errorf("want nil result, got %+v", res)
	}
}

func TestApplyEmailChanges_missingCollectionRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err == nil || !strings.Contains(err.Error(), "<Collection>") {
		t.Errorf("err = %v", err)
	}
}

func TestApplyEmailChanges_collectionStatusError(t *testing.T) {
	// Top-level Status=1 but Collection/Status reports an error.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("S2")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("8")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if !IsStatusCode(err, 8) {
		t.Errorf("err = %v", err)
	}
}

func TestApplyEmailChanges_persistKeyError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(changeAck("S2"))
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), "inbox", "S1")
	es.setSyncKeyErr = errSentinel("disk full")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err == nil || !strings.Contains(err.Error(), "persist") {
		t.Errorf("err = %v", err)
	}
}

func TestApplyEmailChanges_skipsNonElementResponses(t *testing.T) {
	// Stray text inside <Responses> must not affect parsing.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("S2")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageAirSync, "Responses",
						wbxml.Text("stray"),
						wbxml.E(wbxml.PageAirSync, "Change",
							wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("a")),
							wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
						),
					),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	res, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "a", Read: boolp(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ServerID != "a" {
		t.Errorf("res = %+v", res)
	}
}

func TestApplyChangesOnce_syncKeyReadError(t *testing.T) {
	// applyChangesOnce reads the SyncKey itself; bypass ApplyEmailChanges
	// so we can fail the read without ensureSynced intercepting first.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("read fail")}
	_, err := c.applyChangesOnce(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err == nil || !strings.Contains(err.Error(), "read sync key") {
		t.Errorf("err = %v", err)
	}
}

func TestApplyEmailChanges_invalidKeyResetThenBootstrapFails(t *testing.T) {
	// Status=3 → reset OK → re-bootstrap (ensureSynced) fails because the
	// follow-up Sync is now an HTTP error.
	calls := 0
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			// First applyChangesOnce reply: Status=3.
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
			w.Write(body)
			return
		}
		// Re-bootstrap call after reset: HTTP error.
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "STALE")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err == nil {
		t.Error("want error from failed re-bootstrap")
	}
}

func TestEnsureSynced_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("read fail")}
	if err := c.ensureSynced(context.Background(), "inbox"); err == nil {
		t.Error("want error from SyncKey read")
	}
}

func TestApplyEmailChanges_invalidKeyResetFails(t *testing.T) {
	// Status=3 → SetSyncKey reset fails → wrapped error surfaces.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Write(body)
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), "inbox", "STALE")
	es.setSyncKeyErr = errSentinel("ro state")
	_, err := c.ApplyEmailChanges(context.Background(), "inbox", []EmailChange{
		{ServerID: "x", Read: boolp(true)},
	})
	if err == nil || !strings.Contains(err.Error(), "reset") {
		t.Errorf("err = %v", err)
	}
}

func TestApplyEmailChanges_validation(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	if _, err := c.ApplyEmailChanges(context.Background(), "", []EmailChange{{ServerID: "x"}}); err == nil {
		t.Error("want error for empty folder")
	}
	if _, err := c.ApplyEmailChanges(context.Background(), "f", nil); err == nil {
		t.Error("want error for empty changes")
	}
	// Change with no fields and no Delete → no effective changes
	if _, err := c.ApplyEmailChanges(context.Background(), "f", []EmailChange{{ServerID: "x"}}); err == nil ||
		!strings.Contains(err.Error(), "no effective changes") &&
			!strings.Contains(err.Error(), "bootstrap") {
		// May error during ensureSynced (no httptest), which is fine; we
		// just want SOME error here.
		t.Errorf("err = %v", err)
	}
}
