// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestFolderCreate_returnsServerID(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderCreate",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS-2")),
				wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text("new-folder")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), FolderRootID, "FS-1")

	res, err := c.FolderCreate(context.Background(), "0", "Projects", FolderTypeUserGeneric)
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerID != "new-folder" || res.SyncKey != "FS-2" {
		t.Errorf("res = %+v", res)
	}
	// Persisted.
	stored, _ := c.cfg.State.SyncKey(context.Background(), FolderRootID)
	if stored != "FS-2" {
		t.Errorf("sync key not persisted: %q", stored)
	}
	// Request includes DisplayName + Type.
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if dn := req.Root.Find("DisplayName"); dn == nil || dn.TextContent() != "Projects" {
		t.Errorf("DisplayName: %v", dn)
	}
}

func TestFolderUpdate_persistsKey(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderUpdate",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("FS-3")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), FolderRootID, "FS-2")
	if err := c.FolderUpdate(context.Background(), "f1", "0", "Renamed"); err != nil {
		t.Fatal(err)
	}
	stored, _ := c.cfg.State.SyncKey(context.Background(), FolderRootID)
	if stored != "FS-3" {
		t.Errorf("stored = %q", stored)
	}
}

func TestFolderCreate_validation(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	if _, err := c.FolderCreate(context.Background(), "0", "", FolderTypeUserGeneric); err == nil {
		t.Error("want error for empty displayName")
	}
}

func TestFolderCreate_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("boom")}
	if _, err := c.FolderCreate(context.Background(), "0", "X", FolderTypeUserGeneric); err == nil {
		t.Error("want error from SyncKey read")
	}
}

func TestFolderCreate_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.FolderCreate(context.Background(), "0", "X", FolderTypeUserGeneric); err == nil {
		t.Error("want HTTP error")
	}
}

func TestFolderCreate_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	if _, err := c.FolderCreate(context.Background(), "0", "X", FolderTypeUserGeneric); err == nil {
		t.Error("want error on empty response")
	}
}

func TestFolderCreate_statusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderCreate",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("3")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.FolderCreate(context.Background(), "0", "X", FolderTypeUserGeneric); !IsStatusCode(err, 3) {
		t.Errorf("err = %v", err)
	}
}

func TestFolderUpdate_validation(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	if err := c.FolderUpdate(context.Background(), "", "0", "X"); err == nil {
		t.Error("want error for empty serverID")
	}
	if err := c.FolderUpdate(context.Background(), "id", "0", ""); err == nil {
		t.Error("want error for empty newDisplayName")
	}
}

func TestFolderUpdate_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("boom")}
	if err := c.FolderUpdate(context.Background(), "id", "0", "X"); err == nil {
		t.Error("want error from SyncKey read")
	}
}

func TestFolderUpdate_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := c.FolderUpdate(context.Background(), "id", "0", "X"); err == nil {
		t.Error("want HTTP error")
	}
}

func TestFolderDelete_validation(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	if err := c.FolderDelete(context.Background(), ""); err == nil {
		t.Error("want error for empty serverID")
	}
}

func TestFolderDelete_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("boom")}
	if err := c.FolderDelete(context.Background(), "id"); err == nil {
		t.Error("want error from SyncKey read")
	}
}

func TestFolderDelete_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := c.FolderDelete(context.Background(), "id"); err == nil {
		t.Error("want HTTP error")
	}
}

func TestFolderUpdate_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	if err := c.FolderUpdate(context.Background(), "id", "0", "X"); err == nil {
		t.Error("want error on empty response")
	}
}

func TestFolderDelete_returnsErrorOnBadStatus(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderDelete",
				wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("3")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), FolderRootID, "x")
	err := c.FolderDelete(context.Background(), "f1")
	if !IsStatusCode(err, 3) {
		t.Errorf("err = %v", err)
	}
}
