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

func TestSyncTasks_emptyFolderRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit")
	})
	if _, err := c.SyncTasks(context.Background(), ""); err == nil {
		t.Error("want error for empty folderID")
	}
}

func TestSyncTasks_parsesItem(t *testing.T) {
	add := wbxml.E(wbxml.PageAirSync, "Add",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("t-1")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageTasks, "Subject", wbxml.Text("Write tests")),
			wbxml.E(wbxml.PageTasks, "Importance", wbxml.Text("2")),
			wbxml.E(wbxml.PageTasks, "Complete", wbxml.Text("0")),
			wbxml.E(wbxml.PageTasks, "DueDate", wbxml.Text("2026-05-15T17:00:00.000Z")),
		),
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse("tasks", "T1", add))
	})
	res, err := c.SyncTasks(context.Background(), "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("len = %d", len(res.Added))
	}
	tk := res.Added[0]
	if tk.Subject != "Write tests" || tk.Importance != 2 || tk.Complete {
		t.Errorf("got %+v", tk)
	}
	want := time.Date(2026, 5, 15, 17, 0, 0, 0, time.UTC)
	if !tk.DueDate.Equal(want) {
		t.Errorf("DueDate = %v want %v", tk.DueDate, want)
	}
}

func TestParseTaskItem_completeAndBody(t *testing.T) {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData",
		wbxml.E(wbxml.PageTasks, "Subject", wbxml.Text("Done thing")),
		wbxml.E(wbxml.PageTasks, "Sensitivity", wbxml.Text("3")),
		wbxml.E(wbxml.PageTasks, "Complete", wbxml.Text("1")),
		wbxml.E(wbxml.PageTasks, "DateCompleted", wbxml.Text("2026-05-01T12:00:00.000Z")),
		wbxml.E(wbxml.PageTasks, "StartDate", wbxml.Text("2026-04-01T09:00:00.000Z")),
		wbxml.E(wbxml.PageTasks, "UtcStartDate", wbxml.Text("2026-04-01T09:00:00.000Z")),
		wbxml.E(wbxml.PageTasks, "UtcDueDate", wbxml.Text("2026-05-01T12:00:00.000Z")),
		wbxml.E(wbxml.PageTasks, "ReminderTime", wbxml.Text("2026-04-30T09:00:00.000Z")),
		wbxml.E(wbxml.PageAirSyncBase, "Body",
			wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("body bytes")),
		),
	)
	tk := parseTaskItem("t-2", app)
	if !tk.Complete {
		t.Error("Complete not parsed")
	}
	if tk.Sensitivity != 3 || tk.Body != "body bytes" {
		t.Errorf("got %+v", tk)
	}
	if tk.DateCompleted.IsZero() || tk.StartDate.IsZero() || tk.UTCStartDate.IsZero() ||
		tk.UTCDueDate.IsZero() || tk.Reminder.IsZero() {
		t.Errorf("expected all dates parsed; got %+v", tk)
	}
}

func TestBuildTaskApp_setsAllFields(t *testing.T) {
	start := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	due := time.Date(2026, 6, 5, 17, 0, 0, 0, time.UTC)
	rem := time.Date(2026, 6, 4, 9, 0, 0, 0, time.UTC)
	app := buildTaskApp(TaskDraft{
		Subject:     "All fields",
		Body:        "the body",
		Importance:  2,
		Sensitivity: 1,
		Complete:    true,
		StartDate:   start,
		DueDate:     due,
		Reminder:    rem,
	})
	wantText := map[string]string{
		"Subject":      "All fields",
		"Importance":   "2",
		"Sensitivity":  "1",
		"Complete":     "1",
		"StartDate":    formatEASTime(start),
		"UtcStartDate": formatEASTime(start.UTC()),
		"DueDate":      formatEASTime(due),
		"UtcDueDate":   formatEASTime(due.UTC()),
		"ReminderSet":  "1",
		"ReminderTime": formatEASTime(rem),
	}
	for name, want := range wantText {
		if el := app.Find(name); el == nil {
			t.Errorf("buildTaskApp: missing <%s>", name)
		} else if got := el.TextContent(); got != want {
			t.Errorf("buildTaskApp: <%s> = %q, want %q", name, got, want)
		}
	}
	if app.Find("DateCompleted") == nil {
		t.Error("Complete=true did not auto-set DateCompleted")
	}
	if body := app.Find("Body"); body == nil {
		t.Error("Body element missing")
	} else if data := body.Find("Data"); data == nil || data.TextContent() != "the body" {
		t.Errorf("Body/Data = %v", data)
	}
}

func TestBuildTaskApp_incompleteOmitsDateCompleted(t *testing.T) {
	app := buildTaskApp(TaskDraft{Subject: "X"})
	if c := app.Find("Complete"); c == nil || c.TextContent() != "0" {
		t.Errorf("Complete = %v, want 0", c)
	}
	if app.Find("DateCompleted") != nil {
		t.Error("Complete=false should not emit DateCompleted")
	}
}

func TestCreateTask_returnsServerID(t *testing.T) {
	c, bodyP := twoCallSyncServer(t, "tasks", "BOOT", "tasks:NEW")
	due := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	id, err := c.CreateTask(context.Background(), "tasks", TaskDraft{
		Subject: "Write tests", Importance: 2, DueDate: due,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "tasks:NEW" {
		t.Errorf("id = %q", id)
	}
	if !strings.Contains(string(**bodyP), "Write tests") {
		t.Error("emitted body missing subject")
	}
}

func TestUpdateTask_emitsChange(t *testing.T) {
	c, lastBody := singleCallSyncServer(t, "tasks")
	if err := c.UpdateTask(context.Background(), "tasks", "t-1", TaskDraft{
		Subject: "Updated subject",
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(*lastBody, wbxml.DefaultRegistry())
	if req.Root.Find("Change") == nil {
		t.Error("request missing <Change>")
	}
	if sid := req.Root.Find("ServerId"); sid == nil || sid.TextContent() != "t-1" {
		t.Errorf("ServerId = %v", sid)
	}
	if !strings.Contains(string(*lastBody), "Updated subject") {
		t.Error("body missing new subject")
	}
}

func TestDeleteTask_emitsDelete(t *testing.T) {
	c, lastBody := singleCallSyncServer(t, "tasks")
	if err := c.DeleteTask(context.Background(), "tasks", "t-1"); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(*lastBody, wbxml.DefaultRegistry())
	if req.Root.Find("Delete") == nil {
		t.Error("request missing <Delete>")
	}
	if sid := req.Root.Find("ServerId"); sid == nil || sid.TextContent() != "t-1" {
		t.Errorf("ServerId = %v", sid)
	}
}

func TestParseTaskItem_nilSafe(t *testing.T) {
	got := parseTaskItem("id", nil)
	if got.ServerID != "id" || got.Subject != "" {
		t.Errorf("got %+v", got)
	}
}

func TestParseTaskItem_skipsNonElementAndForeignBodyChildren(t *testing.T) {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData",
		wbxml.Text("stray"), // top-level non-element → skip
		wbxml.E(wbxml.PageTasks, "Subject", wbxml.Text("S")),
		wbxml.E(wbxml.PageAirSyncBase, "Body",
			wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("ignored — wrong page")),
			wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("body bytes")),
		),
	)
	got := parseTaskItem("id", app)
	if got.Subject != "S" || got.Body != "body bytes" {
		t.Errorf("got %+v", got)
	}
}

func TestSyncTasks_parsesChangeAndDelete(t *testing.T) {
	change := wbxml.E(wbxml.PageAirSync, "Change",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("t-2")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageTasks, "Subject", wbxml.Text("changed")),
		),
	)
	del := wbxml.E(wbxml.PageAirSync, "Delete",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("t-3")),
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse("tasks", "T1", change, del))
	})
	res, err := c.SyncTasks(context.Background(), "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 || res.Changed[0].Subject != "changed" {
		t.Errorf("changed = %+v", res.Changed)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "t-3" {
		t.Errorf("deleted = %v", res.Deleted)
	}
}

func TestSyncTasks_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.SyncTasks(context.Background(), "tasks"); err == nil {
		t.Error("want HTTP error")
	}
}

func TestCompleteTask_setsCompleteFlag(t *testing.T) {
	c, lastBody := singleCallSyncServer(t, "tasks")
	if err := c.CompleteTask(context.Background(), "tasks", "t-1"); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(*lastBody, wbxml.DefaultRegistry())
	complete := req.Root.Find("Complete")
	if complete == nil || complete.TextContent() != "1" {
		t.Errorf("Complete = %v", complete)
	}
	if req.Root.Find("DateCompleted") == nil {
		t.Error("DateCompleted missing")
	}
}
