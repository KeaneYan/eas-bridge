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

// addCmd builds an Add command for the test fixtures.
func addCmd(serverID, subject, from string) *wbxml.Element {
	return wbxml.E(wbxml.PageAirSync, "Add",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text(subject)),
			wbxml.E(wbxml.PageEmail, "From", wbxml.Text(from)),
			wbxml.E(wbxml.PageEmail, "Read", wbxml.Text("0")),
		),
	)
}

func syncResponse(syncKey string, more bool, cmds ...*wbxml.Element) []byte {
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "Class", wbxml.Text("Email")),
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(syncKey)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
		wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
	)
	if more {
		collection.Children = append(collection.Children,
			wbxml.E(wbxml.PageAirSync, "MoreAvailable"),
		)
	}
	if len(cmds) > 0 {
		commands := wbxml.E(wbxml.PageAirSync, "Commands", asNodes(cmds)...)
		collection.Children = append(collection.Children, commands)
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

func asNodes(els []*wbxml.Element) []wbxml.Node {
	out := make([]wbxml.Node, len(els))
	for i, e := range els {
		out[i] = e
	}
	return out
}

func TestSyncEmail_bootstrapTwoCalls(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    int
		sentKeys []string
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		req, _ := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
		key := req.Root.Find("SyncKey").TextContent()
		mu.Lock()
		calls++
		sentKeys = append(sentKeys, key)
		thisCall := calls
		mu.Unlock()

		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch thisCall {
		case 1:
			// Bootstrap: return new key, no items.
			w.Write(syncResponse("S1", false))
		case 2:
			// Now return actual items.
			w.Write(syncResponse("S2", false, addCmd("inbox:1", "Hello", "alice@x")))
		}
	})

	res, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if sentKeys[0] != "0" || sentKeys[1] != "S1" {
		t.Errorf("sentKeys = %v, want [0, S1]", sentKeys)
	}
	if res.SyncKey != "S2" {
		t.Errorf("SyncKey = %q", res.SyncKey)
	}
	if len(res.Added) != 1 || res.Added[0].Subject != "Hello" {
		t.Errorf("Added = %+v", res.Added)
	}
}

func TestSyncEmail_skipsBootstrapWhenStateAdvanced(t *testing.T) {
	var calls int
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("S2", false, addCmd("inbox:2", "Reply", "carol@x")))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")

	res, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no bootstrap when state already advanced)", calls)
	}
	if res.SyncKey != "S2" || len(res.Added) != 1 {
		t.Errorf("res = %+v", res)
	}
}

func TestSyncEmail_invalidSyncKeyRetries(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
		keys  []string
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		req, _ := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
		mu.Lock()
		calls++
		keys = append(keys, req.Root.Find("SyncKey").TextContent())
		thisCall := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch thisCall {
		case 1:
			doc := &wbxml.Document{
				Root: wbxml.E(wbxml.PageAirSync, "Sync",
					wbxml.E(wbxml.PageAirSync, "Collections",
						wbxml.E(wbxml.PageAirSync, "Collection",
							wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
						),
					),
				),
			}
			b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(b)
		default:
			// After reset to "0", return a bootstrap response, then items
			// would be the second part of the bootstrap dance. To keep
			// this test focused, return data immediately.
			w.Write(syncResponse("FRESH", false, addCmd("inbox:1", "Topic", "bob@x")))
		}
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "STALE")

	res, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d", calls)
	}
	if keys[0] != "STALE" || keys[1] != "0" {
		t.Errorf("sentKeys = %v", keys)
	}
	if res.SyncKey != "FRESH" || len(res.Added) != 1 {
		t.Errorf("res = %+v", res)
	}
}

func TestSyncEmail_moreAvailable(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("S2", true,
			addCmd("inbox:1", "a", "a@x"),
			addCmd("inbox:2", "b", "b@x"),
		))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	res, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.MoreAvailable {
		t.Error("MoreAvailable = false")
	}
	if len(res.Added) != 2 {
		t.Errorf("len(Added) = %d", len(res.Added))
	}
}

func TestSyncEmail_changeAndDelete(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("S3", false,
			wbxml.E(wbxml.PageAirSync, "Change",
				wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("inbox:1")),
				wbxml.E(wbxml.PageAirSync, "ApplicationData",
					wbxml.E(wbxml.PageEmail, "Read", wbxml.Text("1")),
				),
			),
			wbxml.E(wbxml.PageAirSync, "Delete",
				wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("inbox:gone")),
			),
		))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S2")
	res, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 || !res.Changed[0].Read || res.Changed[0].ServerID != "inbox:1" {
		t.Errorf("Changed = %+v", res.Changed)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "inbox:gone" {
		t.Errorf("Deleted = %v", res.Deleted)
	}
}

func TestSyncEmail_emptyFolderID(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	_, err := c.SyncEmail(context.Background(), "", EmailSyncOptions{})
	if err == nil {
		t.Error("want error")
	}
}

func TestSyncEmail_persistsKey(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("PERSISTED", false))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	if _, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true}); err != nil {
		t.Fatal(err)
	}
	stored, _ := c.cfg.State.SyncKey(context.Background(), "inbox")
	if stored != "PERSISTED" {
		t.Errorf("stored = %q", stored)
	}
}

func TestSyncEmail_advancedOptionsEmitted(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("S2", false))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{
		NoBootstrap:             true,
		MIMESupport:             2,
		MIMETruncation:          65536,
		ConversationMode:        true,
		RightsManagementSupport: true,
		BodyPartPreviewBytes:    512,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	for name, want := range map[string]string{
		"MIMESupport":      "2",
		"MIMETruncation":   "65536",
		"ConversationMode": "1",
	} {
		el := req.Root.Find(name)
		if el == nil || el.TextContent() != want {
			t.Errorf("%s = %v, want %q", name, el, want)
		}
	}
	if rms := req.Root.Find("RightsManagementSupport"); rms == nil || rms.TextContent() != "1" {
		t.Errorf("RightsManagementSupport = %v", rms)
	}
	if bpp := req.Root.Find("BodyPartPreference"); bpp == nil {
		t.Error("BodyPartPreference missing")
	}
}

func TestSyncEmail_emitsWaitMinutes(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("S2", false))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, _ = c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{
		NoBootstrap: true,
		WaitMinutes: 10,
	})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if w := req.Root.Find("Wait"); w == nil || w.TextContent() != "10" {
		t.Errorf("Wait = %v", w)
	}
}

func TestSyncEmail_emitsHeartbeat(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("S2", false))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, _ = c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{
		NoBootstrap:      true,
		HeartbeatSeconds: 300,
	})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if h := req.Root.Find("HeartbeatInterval"); h == nil || h.TextContent() != "300" {
		t.Errorf("HeartbeatInterval = %v", h)
	}
}

func TestSyncEmail_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("read fail")}
	if _, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true}); err == nil {
		t.Error("want error from SyncKey read")
	}
}

func TestSyncEmail_emptyResponseTreatedAsNoChanges(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "PRIOR")
	res, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SyncKey != "PRIOR" {
		t.Errorf("expected old key carried through; got %q", res.SyncKey)
	}
}

func TestSyncEmail_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("8")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if !IsStatusCode(err, 8) {
		t.Errorf("err = %v", err)
	}
}

func TestSyncEmail_missingCollectionRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
				// no <Collection>
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err == nil || !strings.Contains(err.Error(), "<Collection>") {
		t.Errorf("err = %v", err)
	}
}

func TestSyncEmail_skipsNonElementCommands(t *testing.T) {
	// Stray text inside <Commands> is skipped without affecting parse.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Collections",
					wbxml.E(wbxml.PageAirSync, "Collection",
						wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("S2")),
						wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
						wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageAirSync, "Commands",
							wbxml.Text("stray"),
							wbxml.E(wbxml.PageAirSync, "Add",
								wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("1")),
								wbxml.E(wbxml.PageAirSync, "ApplicationData",
									wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("Hi")),
								),
							),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	res, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0].Subject != "Hi" {
		t.Errorf("added = %+v", res.Added)
	}
}

func TestSyncEmail_missingSyncKeyRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Collections",
					wbxml.E(wbxml.PageAirSync, "Collection",
						wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
						wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
						// no <SyncKey>
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "inbox", "S1")
	_, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err == nil || !strings.Contains(err.Error(), "<SyncKey>") {
		t.Errorf("err = %v", err)
	}
}

func TestSyncEmail_persistKeyError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(syncResponse("S2", false))
	})
	es := &errStateStore{inner: NewMemoryState(), setSyncKeyErr: errSentinel("disk full")}
	c.cfg.State = es
	if _, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true}); err == nil {
		t.Error("want error from persist failure")
	}
}

func TestSyncEmail_invalidSyncKeyResetFails(t *testing.T) {
	// The reset SetSyncKey fails after Status=3; the wrapped error should
	// surface, not the underlying StatusError.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(invalidSyncKeyResponse())
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), "inbox", "STALE")
	es.setSyncKeyErr = errSentinel("ro state")
	_, err := c.SyncEmail(context.Background(), "inbox", EmailSyncOptions{NoBootstrap: true})
	if err == nil || !strings.Contains(err.Error(), "reset") {
		t.Errorf("err = %v", err)
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{
		0: "0", 1: "1", 9: "9", 10: "10", 100: "100",
		-1: "-1", -42: "-42",
	}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}
