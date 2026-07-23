// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func pingResponse(status int, heartbeat int, changed ...string) []byte {
	root := wbxml.E(wbxml.PagePing, "Ping",
		wbxml.E(wbxml.PagePing, "Status", wbxml.Text(itoa(status))),
	)
	if heartbeat > 0 {
		root.Children = append(root.Children, wbxml.E(wbxml.PagePing, "HeartbeatInterval", wbxml.Text(itoa(heartbeat))))
	}
	if len(changed) > 0 {
		folders := wbxml.E(wbxml.PagePing, "Folders")
		for _, id := range changed {
			folders.Children = append(folders.Children, wbxml.E(wbxml.PagePing, "Folder",
				wbxml.E(wbxml.PagePing, "Id", wbxml.Text(id)),
				wbxml.E(wbxml.PagePing, "Class", wbxml.Text("Email")),
			))
		}
		root.Children = append(root.Children, folders)
	}
	out, err := wbxml.Marshal(&wbxml.Document{Root: root}, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return out
}

func TestPing_noChanges(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pingResponse(1, 0))
	})
	res, err := c.Ping(context.Background(), 60, []PingFolder{{ID: "inbox", Class: "Email"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 1 || len(res.ChangedFolders) != 0 {
		t.Errorf("res = %+v", res)
	}
}

func TestPing_changesAvailable(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pingResponse(2, 0, "inbox", "calendar"))
	})
	res, err := c.Ping(context.Background(), 60, []PingFolder{
		{ID: "inbox", Class: "Email"},
		{ID: "calendar", Class: "Calendar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 2 {
		t.Errorf("Status = %d", res.Status)
	}
	if len(res.ChangedFolders) != 2 || res.ChangedFolders[0] != "inbox" {
		t.Errorf("ChangedFolders = %v", res.ChangedFolders)
	}
}

// MS-ASCMD §2.2.2.11.2 says <Folder> contains the folder ID as text,
// not in a nested <Id> element. Z-Push and most real servers send this
// form. The Folder-as-text path is what made TestIntegration_Ping
// originally fail on a real server.
func TestPing_changesAvailable_folderAsText(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		root := wbxml.E(wbxml.PagePing, "Ping",
			wbxml.E(wbxml.PagePing, "Status", wbxml.Text("2")),
			wbxml.E(wbxml.PagePing, "Folders",
				wbxml.E(wbxml.PagePing, "Folder", wbxml.Text("inbox")),
				wbxml.E(wbxml.PagePing, "Folder", wbxml.Text("calendar")),
			),
		)
		body, _ := wbxml.Marshal(&wbxml.Document{Root: root}, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.Ping(context.Background(), 60, []PingFolder{
		{ID: "inbox", Class: "Email"},
		{ID: "calendar", Class: "Calendar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 2 {
		t.Errorf("Status = %d", res.Status)
	}
	if len(res.ChangedFolders) != 2 || res.ChangedFolders[0] != "inbox" || res.ChangedFolders[1] != "calendar" {
		t.Errorf("ChangedFolders = %v", res.ChangedFolders)
	}
}

func TestPing_heartbeatTooShort(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pingResponse(3, 90))
	})
	res, err := c.Ping(context.Background(), 30, []PingFolder{{ID: "inbox", Class: "Email"}})
	if err == nil {
		t.Fatal("want error for non-1/2 status")
	}
	if res == nil || res.HeartbeatInterval != 90 {
		t.Errorf("res = %+v", res)
	}
}

func TestPing_emptyFoldersRejected(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	if _, err := c.Ping(context.Background(), 60, nil); err == nil {
		t.Error("want error")
	}
}

func TestPing_zeroHeartbeatDefaults(t *testing.T) {
	// heartbeat <= 0 is replaced by 60 before the request is built.
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pingResponse(1, 0))
	})
	_, _ = c.Ping(context.Background(), 0, []PingFolder{{ID: "inbox", Class: "Email"}})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if hb := req.Root.Find("HeartbeatInterval"); hb == nil || hb.TextContent() != "60" {
		t.Errorf("HeartbeatInterval = %v", hb)
	}
}

func TestPing_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.Ping(context.Background(), 60, []PingFolder{{ID: "i"}}); err == nil {
		t.Error("want HTTP error")
	}
}

func TestPing_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	if _, err := c.Ping(context.Background(), 60, []PingFolder{{ID: "i"}}); err == nil {
		t.Error("want error on empty response")
	}
}

func TestPing_topLevelIdElement(t *testing.T) {
	// Some implementations put <Id> directly inside <Folders> instead of
	// wrapping it in <Folder>. The parser handles that path too.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		root := wbxml.E(wbxml.PagePing, "Ping",
			wbxml.E(wbxml.PagePing, "Status", wbxml.Text("2")),
			wbxml.E(wbxml.PagePing, "Folders",
				wbxml.Text("stray"), // non-element child must be skipped
				wbxml.E(wbxml.PagePing, "Id", wbxml.Text("inbox")),
			),
		)
		body, _ := wbxml.Marshal(&wbxml.Document{Root: root}, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.Ping(context.Background(), 60, []PingFolder{{ID: "inbox", Class: "Email"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ChangedFolders) != 1 || res.ChangedFolders[0] != "inbox" {
		t.Errorf("ChangedFolders = %v", res.ChangedFolders)
	}
}

func TestPing_missingStatusIsError(t *testing.T) {
	// Status=0 (no <Status>) must be reported as a malformed response.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		root := wbxml.E(wbxml.PagePing, "Ping") // no Status child
		body, _ := wbxml.Marshal(&wbxml.Document{Root: root}, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.Ping(context.Background(), 60, []PingFolder{{ID: "i"}}); err == nil {
		t.Error("want error when Status is missing")
	}
}
