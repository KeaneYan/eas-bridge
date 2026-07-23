// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

func TestSyncNotes_emptyFolderRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit")
	})
	if _, err := c.SyncNotes(context.Background(), ""); err == nil {
		t.Error("want error for empty folderID")
	}
}

func TestSyncNotes_parsesItem(t *testing.T) {
	add := wbxml.E(wbxml.PageAirSync, "Add",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("n-1")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageNotes, "Subject", wbxml.Text("Quick note")),
			wbxml.E(wbxml.PageAirSyncBase, "Body",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("body text")),
			),
			wbxml.E(wbxml.PageNotes, "Categories",
				wbxml.E(wbxml.PageNotes, "Category", wbxml.Text("work")),
				wbxml.E(wbxml.PageNotes, "Category", wbxml.Text("urgent")),
			),
		),
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse("notes", "N1", add))
	})
	res, err := c.SyncNotes(context.Background(), "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("len = %d", len(res.Added))
	}
	n := res.Added[0]
	if n.Subject != "Quick note" || n.Body != "body text" {
		t.Errorf("note = %+v", n)
	}
	if len(n.Categories) != 2 || n.Categories[0] != "work" {
		t.Errorf("categories = %v", n.Categories)
	}
}

func TestBuildNoteApp_emitsCategoriesAndBody(t *testing.T) {
	app := buildNoteApp(NoteDraft{
		Subject:          "S",
		Body:             "B",
		BodyType:         BodyTypeHTML,
		Categories:       []string{"a", "b"},
		LastModifiedDate: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
	})
	if subj := app.Find("Subject"); subj == nil || subj.TextContent() != "S" {
		t.Errorf("Subject = %v", subj)
	}
	body := app.Find("Body")
	if body == nil {
		t.Fatal("Body element missing")
	}
	if data := body.Find("Data"); data == nil || data.TextContent() != "B" {
		t.Errorf("Body/Data = %v", data)
	}
	if bt := body.Find("Type"); bt == nil || bt.TextContent() != "2" { // HTML = 2
		t.Errorf("Body/Type = %v", bt)
	}
	cats := app.Find("Categories")
	if cats == nil || len(cats.Children) != 2 {
		t.Fatalf("Categories = %v", cats)
	}
	got := []string{}
	for _, ch := range cats.Children {
		if e, ok := ch.(*wbxml.Element); ok {
			got = append(got, e.TextContent())
		}
	}
	if strings.Join(got, ",") != "a,b" {
		t.Errorf("Categories text = %v", got)
	}
}

func TestCreateNote_returnsServerID(t *testing.T) {
	c, bodyP := twoCallSyncServer(t, "notes", "BOOT", "notes:NEW")
	id, err := c.CreateNote(context.Background(), "notes", NoteDraft{
		Subject:    "Reminder",
		Body:       "buy milk",
		Categories: []string{"home"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "notes:NEW" {
		t.Errorf("id = %q", id)
	}
	if !strings.Contains(string(**bodyP), "buy milk") {
		t.Error("body missing note text")
	}
}

func TestUpdateNote_emitsChange(t *testing.T) {
	c, lastBody := singleCallSyncServer(t, "notes")
	if err := c.UpdateNote(context.Background(), "notes", "n-1", NoteDraft{
		Subject: "renamed",
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(*lastBody, wbxml.DefaultRegistry())
	if req.Root.Find("Change") == nil {
		t.Error("request missing <Change>")
	}
}

func TestParseNoteItem_nilSafe(t *testing.T) {
	got := parseNoteItem("id", nil)
	if got.ServerID != "id" || got.Subject != "" {
		t.Errorf("got %+v, want zero except ServerID", got)
	}
}

func TestParseNoteItem_handlesAllFields(t *testing.T) {
	// Cover LastModifiedDate parse, the non-element/wrong-codepage skips,
	// and the Body inner-element non-element/wrong-codepage skip.
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData",
		wbxml.Text("stray"), // top-level non-element → skip
		wbxml.E(wbxml.PageAirSync, "Class", wbxml.Text("ignored")), // unrelated codepage
		wbxml.E(wbxml.PageNotes, "Subject", wbxml.Text("S")),
		wbxml.E(wbxml.PageNotes, "LastModifiedDate", wbxml.Text("2024-05-10T12:00:00.000Z")),
		wbxml.E(wbxml.PageAirSyncBase, "Body",
			wbxml.Text("stray inside body"), // non-element → skip
			wbxml.E(wbxml.PageNotes, "Subject", wbxml.Text("ignored — wrong page")),
			wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("body")),
		),
	)
	got := parseNoteItem("n-1", app)
	if got.Subject != "S" || got.Body != "body" {
		t.Errorf("got %+v", got)
	}
	if got.LastModifiedDate.IsZero() {
		t.Error("LastModifiedDate not parsed")
	}
}

func TestSyncNotes_parsesChangeAndDelete(t *testing.T) {
	change := wbxml.E(wbxml.PageAirSync, "Change",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("n-2")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageNotes, "Subject", wbxml.Text("changed")),
		),
	)
	del := wbxml.E(wbxml.PageAirSync, "Delete",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("n-3")),
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse("notes", "N1", change, del))
	})
	res, err := c.SyncNotes(context.Background(), "notes")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 || res.Changed[0].Subject != "changed" {
		t.Errorf("changed = %+v", res.Changed)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "n-3" {
		t.Errorf("deleted = %v", res.Deleted)
	}
}

func TestSyncNotes_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.SyncNotes(context.Background(), "notes"); err == nil {
		t.Error("want HTTP error")
	}
}

func TestDeleteNote_emitsDelete(t *testing.T) {
	c, lastBody := singleCallSyncServer(t, "notes")
	if err := c.DeleteNote(context.Background(), "notes", "n-1"); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(*lastBody, wbxml.DefaultRegistry())
	if req.Root.Find("Delete") == nil {
		t.Error("request missing <Delete>")
	}
}
