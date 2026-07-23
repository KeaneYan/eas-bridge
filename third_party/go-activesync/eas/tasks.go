// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// TaskItem is a parsed task item.
type TaskItem struct {
	ServerID string

	Subject       string
	Body          string
	Importance    int // 0=low, 1=normal, 2=high
	Sensitivity   int
	Complete      bool
	DateCompleted time.Time
	StartDate     time.Time
	UTCStartDate  time.Time
	DueDate       time.Time
	UTCDueDate    time.Time
	Reminder      time.Time
}

// TaskDraft is the input to CreateTask / UpdateTask.
type TaskDraft = TaskItem

// TasksSyncResult is the parsed Sync output for a tasks folder.
type TasksSyncResult struct {
	SyncKey       string
	MoreAvailable bool
	Added         []TaskItem
	Changed       []TaskItem
	Deleted       []string
}

// SyncTasks fetches a tasks folder.
func (c *httpClient) SyncTasks(ctx context.Context, folderID string) (*TasksSyncResult, error) {
	if folderID == "" {
		return nil, errors.New("eas: SyncTasks: folderID is required")
	}
	out := &TasksSyncResult{}
	key, more, err := c.genericSyncFolder(ctx, folderID,
		func(id string, app *wbxml.Element) {
			out.Added = append(out.Added, parseTaskItem(id, app))
		},
		func(id string, app *wbxml.Element) {
			out.Changed = append(out.Changed, parseTaskItem(id, app))
		},
		func(id string) {
			out.Deleted = append(out.Deleted, id)
		},
	)
	if err != nil {
		return nil, err
	}
	out.SyncKey = key
	out.MoreAvailable = more
	return out, nil
}

// CreateTask creates a new task.
func (c *httpClient) CreateTask(ctx context.Context, folderID string, draft TaskDraft) (string, error) {
	return c.addItemViaSync(ctx, folderID, buildTaskApp(draft))
}

// UpdateTask modifies an existing task.
func (c *httpClient) UpdateTask(ctx context.Context, folderID, serverID string, draft TaskDraft) error {
	return c.changeItemViaSync(ctx, folderID, serverID, buildTaskApp(draft))
}

// CompleteTask marks a task complete.
func (c *httpClient) CompleteTask(ctx context.Context, folderID, serverID string) error {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData",
		wbxml.E(wbxml.PageTasks, "Complete", wbxml.Text("1")),
		wbxml.E(wbxml.PageTasks, "DateCompleted", wbxml.Text(formatEASTime(time.Now().UTC()))),
	)
	return c.changeItemViaSync(ctx, folderID, serverID, app)
}

// DeleteTask removes a task.
func (c *httpClient) DeleteTask(ctx context.Context, folderID, serverID string) error {
	return c.deleteItemViaSync(ctx, folderID, serverID)
}

func parseTaskItem(serverID string, app *wbxml.Element) TaskItem {
	out := TaskItem{ServerID: serverID}
	if app == nil {
		return out
	}
	for _, c := range app.Children {
		el, ok := c.(*wbxml.Element)
		if !ok {
			continue
		}
		switch el.Codepage {
		case wbxml.PageTasks:
			switch el.Name {
			case "Subject":
				out.Subject = el.TextContent()
			case "Importance":
				out.Importance = atoi(el.TextContent())
			case "Sensitivity":
				out.Sensitivity = atoi(el.TextContent())
			case "Complete":
				out.Complete = el.TextContent() == "1"
			case "DateCompleted":
				out.DateCompleted, _ = parseEASTime(el.TextContent())
			case "StartDate":
				out.StartDate, _ = parseEASTime(el.TextContent())
			case "UtcStartDate":
				out.UTCStartDate, _ = parseEASTime(el.TextContent())
			case "DueDate":
				out.DueDate, _ = parseEASTime(el.TextContent())
			case "UtcDueDate":
				out.UTCDueDate, _ = parseEASTime(el.TextContent())
			case "ReminderTime":
				out.Reminder, _ = parseEASTime(el.TextContent())
			}
		case wbxml.PageAirSyncBase:
			if el.Name == "Body" {
				for _, bc := range el.Children {
					be, ok := bc.(*wbxml.Element)
					if !ok || be.Codepage != wbxml.PageAirSyncBase {
						continue
					}
					if be.Name == "Data" {
						out.Body = be.TextContent()
					}
				}
			}
		}
	}
	return out
}

func buildTaskApp(d TaskDraft) *wbxml.Element {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData")
	if d.Subject != "" {
		app.Children = append(app.Children, wbxml.E(wbxml.PageTasks, "Subject", wbxml.Text(d.Subject)))
	}
	app.Children = append(app.Children,
		wbxml.E(wbxml.PageTasks, "Importance", wbxml.Text(itoa(d.Importance))),
		wbxml.E(wbxml.PageTasks, "Sensitivity", wbxml.Text(itoa(d.Sensitivity))),
	)
	if d.Complete {
		app.Children = append(app.Children, wbxml.E(wbxml.PageTasks, "Complete", wbxml.Text("1")))
		if d.DateCompleted.IsZero() {
			d.DateCompleted = time.Now().UTC()
		}
		app.Children = append(app.Children, wbxml.E(wbxml.PageTasks, "DateCompleted", wbxml.Text(formatEASTime(d.DateCompleted))))
	} else {
		app.Children = append(app.Children, wbxml.E(wbxml.PageTasks, "Complete", wbxml.Text("0")))
	}
	if !d.StartDate.IsZero() {
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageTasks, "StartDate", wbxml.Text(formatEASTime(d.StartDate))),
			wbxml.E(wbxml.PageTasks, "UtcStartDate", wbxml.Text(formatEASTime(d.StartDate.UTC()))),
		)
	}
	if !d.DueDate.IsZero() {
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageTasks, "DueDate", wbxml.Text(formatEASTime(d.DueDate))),
			wbxml.E(wbxml.PageTasks, "UtcDueDate", wbxml.Text(formatEASTime(d.DueDate.UTC()))),
		)
	}
	if !d.Reminder.IsZero() {
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageTasks, "ReminderSet", wbxml.Text("1")),
			wbxml.E(wbxml.PageTasks, "ReminderTime", wbxml.Text(formatEASTime(d.Reminder))),
		)
	}
	if d.Body != "" {
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageAirSyncBase, "Body",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text(d.Body)),
			),
		)
	}
	return app
}
