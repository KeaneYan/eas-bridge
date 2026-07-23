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

// folderSyncResponse builds a canned FolderSync response with the given
// new sync key and a fixed Add/Update/Delete set.
func folderSyncResponse(syncKey string) []byte {
	resp := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
			wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text(syncKey)),
			wbxml.E(wbxml.PageFolderHierarchy, "Changes",
				wbxml.E(wbxml.PageFolderHierarchy, "Count", wbxml.Text("3")),
				wbxml.E(wbxml.PageFolderHierarchy, "Add",
					wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("1")),
					wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text("0")),
					wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Inbox")),
					wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text("2")),
				),
				wbxml.E(wbxml.PageFolderHierarchy, "Update",
					wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("5")),
					wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text("0")),
					wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Drafts")),
					wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text("3")),
				),
				wbxml.E(wbxml.PageFolderHierarchy, "Delete",
					wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("99")),
				),
			),
		),
	}
	out, err := wbxml.Marshal(resp, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return out
}

// invalidSyncKeyResponse builds the canonical "client got it wrong" response.
func invalidSyncKeyResponse() []byte {
	resp := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
			wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("3")),
		),
	}
	out, err := wbxml.Marshal(resp, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return out
}

func TestFolderSync_initialCallSendsSyncKeyZero(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(folderSyncResponse("KEY-1"))
	})
	res, err := c.FolderSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Sent SyncKey "0" since state is empty.
	reqDoc, err := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if got := reqDoc.Root.Find("SyncKey").TextContent(); got != "0" {
		t.Errorf("sent SyncKey: %q, want 0", got)
	}
	// New key persisted.
	stored, _ := c.cfg.State.SyncKey(context.Background(), FolderRootID)
	if stored != "KEY-1" {
		t.Errorf("stored: %q, want KEY-1", stored)
	}
	if res.SyncKey != "KEY-1" {
		t.Errorf("res.SyncKey = %q", res.SyncKey)
	}
	if len(res.Added) != 1 || res.Added[0].DisplayName != "Inbox" || res.Added[0].Type != FolderTypeInbox {
		t.Errorf("Added = %+v", res.Added)
	}
	if len(res.Updated) != 1 || res.Updated[0].DisplayName != "Drafts" {
		t.Errorf("Updated = %+v", res.Updated)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "99" {
		t.Errorf("Deleted = %+v", res.Deleted)
	}
}

func TestFolderSync_subsequentCallSendsStoredKey(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(folderSyncResponse("KEY-2"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), FolderRootID, "KEY-1")

	if _, err := c.FolderSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqDoc, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if got := reqDoc.Root.Find("SyncKey").TextContent(); got != "KEY-1" {
		t.Errorf("sent SyncKey: %q, want KEY-1", got)
	}
}

func TestFolderSync_invalidSyncKeyResetsAndRetries(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    int
		sentKeys []string
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		thisCall := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		// Capture the sync key the client sent.
		body, err := readCapped(r.Body, 1<<20)
		if err == nil {
			doc, err := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
			if err == nil {
				if k := doc.Root.Find("SyncKey"); k != nil {
					mu.Lock()
					sentKeys = append(sentKeys, k.TextContent())
					mu.Unlock()
				}
			}
		}
		switch thisCall {
		case 1:
			w.Write(invalidSyncKeyResponse())
		default:
			w.Write(folderSyncResponse("FRESH"))
		}
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), FolderRootID, "STALE")

	res, err := c.FolderSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if sentKeys[0] != "STALE" || sentKeys[1] != "0" {
		t.Errorf("sentKeys = %v, want [STALE, 0]", sentKeys)
	}
	if res.SyncKey != "FRESH" {
		t.Errorf("final SyncKey: %q", res.SyncKey)
	}
}

func TestFolderSync_propagatesNonRetryableStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		resp := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("110")), // ServerError
			),
		}
		body, _ := wbxml.Marshal(resp, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.FolderSync(context.Background())
	if !IsStatusCode(err, 110) {
		t.Errorf("err = %v", err)
	}
}

func TestFolderSync_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("read fail")}
	if _, err := c.FolderSync(context.Background()); err == nil {
		t.Error("want error from SyncKey read")
	}
}

func TestFolderSync_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.FolderSync(context.Background()); err == nil {
		t.Error("want HTTP error")
	}
}

func TestFolderSync_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	if _, err := c.FolderSync(context.Background()); err == nil {
		t.Error("want error on empty response")
	}
}

func TestFolderSync_missingSyncKeyRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				// no <SyncKey>
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.FolderSync(context.Background())
	if err == nil || !contains(err.Error(), "missing SyncKey") {
		t.Errorf("err = %v", err)
	}
}

func TestFolderSync_skipsNonElementChangesChildren(t *testing.T) {
	// Stray text inside <Changes> must be skipped without affecting the
	// real Add/Update/Delete entries; same for stray text inside an Add.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("K")),
				wbxml.E(wbxml.PageFolderHierarchy, "Changes",
					wbxml.Text("stray"),
					wbxml.E(wbxml.PageFolderHierarchy, "Add",
						wbxml.Text("stray-inside-add"),
						wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("1")),
						wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Inbox")),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.FolderSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0].DisplayName != "Inbox" {
		t.Errorf("added = %+v", res.Added)
	}
}

func TestFolderSync_resetSetSyncKeyError(t *testing.T) {
	// First call returns InvalidSyncKey; the reset SetSyncKey then fails.
	calls := 0
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(invalidSyncKeyResponse())
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), FolderRootID, "STALE")
	es.setSyncKeyErr = errSentinel("can't write")
	_, err := c.FolderSync(context.Background())
	if err == nil || !contains(err.Error(), "reset sync key") {
		t.Errorf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry after reset failure)", calls)
	}
}

func TestFolderSync_persistSetSyncKeyError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(folderSyncResponse("NEWKEY"))
	})
	es := &errStateStore{inner: NewMemoryState(), setSyncKeyErr: errSentinel("disk full")}
	c.cfg.State = es
	if _, err := c.FolderSync(context.Background()); err == nil {
		t.Error("want error from persist failure")
	}
}

func TestParseFolder_skipsNonElement(t *testing.T) {
	// parseFolder ignores stray text children alongside the real ServerId etc.
	e := wbxml.E(wbxml.PageFolderHierarchy, "Add",
		wbxml.Text("stray"),
		wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("42")),
		wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text("Inbox")),
		wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text("2")),
	)
	got := parseFolder(e)
	if got.ServerID != "42" || got.DisplayName != "Inbox" || got.Type != FolderTypeInbox {
		t.Errorf("got %+v", got)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestFolderType_String(t *testing.T) {
	cases := map[FolderType]string{
		FolderTypeUserGeneric:    "UserGeneric",
		FolderTypeInbox:          "Inbox",
		FolderTypeDrafts:         "Drafts",
		FolderTypeDeletedItems:   "DeletedItems",
		FolderTypeSentItems:      "SentItems",
		FolderTypeOutbox:         "Outbox",
		FolderTypeTasks:          "Tasks",
		FolderTypeCalendar:       "Calendar",
		FolderTypeContacts:       "Contacts",
		FolderTypeNotes:          "Notes",
		FolderTypeJournal:        "Journal",
		FolderTypeUserMail:       "UserMail",
		FolderTypeUserCalendar:   "UserCalendar",
		FolderTypeUserContacts:   "UserContacts",
		FolderTypeUserTasks:      "UserTasks",
		FolderTypeUserJournal:    "UserJournal",
		FolderTypeUserNotes:      "UserNotes",
		FolderTypeUnknown:        "Unknown",
		FolderTypeRecipientCache: "RecipientCache",
		FolderType(99):           "FolderType(99)",
	}
	for ft, want := range cases {
		if got := ft.String(); got != want {
			t.Errorf("FolderType(%d) = %q, want %q", ft, got, want)
		}
	}
}
