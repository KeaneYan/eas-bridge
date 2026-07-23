// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// NoteItem is a parsed note item.
type NoteItem struct {
	ServerID         string
	Subject          string
	Body             string
	BodyType         BodyType // typically Plain or HTML
	LastModifiedDate time.Time
	Categories       []string
}

// NoteDraft is the input to CreateNote / UpdateNote.
type NoteDraft = NoteItem

// NotesSyncResult is the parsed Sync output for a notes folder.
type NotesSyncResult struct {
	SyncKey       string
	MoreAvailable bool
	Added         []NoteItem
	Changed       []NoteItem
	Deleted       []string
}

// SyncNotes fetches a notes folder.
func (c *httpClient) SyncNotes(ctx context.Context, folderID string) (*NotesSyncResult, error) {
	if folderID == "" {
		return nil, errors.New("eas: SyncNotes: folderID is required")
	}
	out := &NotesSyncResult{}
	key, more, err := c.genericSyncFolder(ctx, folderID,
		func(id string, app *wbxml.Element) {
			out.Added = append(out.Added, parseNoteItem(id, app))
		},
		func(id string, app *wbxml.Element) {
			out.Changed = append(out.Changed, parseNoteItem(id, app))
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

// CreateNote creates a new note.
func (c *httpClient) CreateNote(ctx context.Context, folderID string, draft NoteDraft) (string, error) {
	return c.addItemViaSync(ctx, folderID, buildNoteApp(draft))
}

// UpdateNote modifies an existing note.
func (c *httpClient) UpdateNote(ctx context.Context, folderID, serverID string, draft NoteDraft) error {
	return c.changeItemViaSync(ctx, folderID, serverID, buildNoteApp(draft))
}

// DeleteNote removes a note.
func (c *httpClient) DeleteNote(ctx context.Context, folderID, serverID string) error {
	return c.deleteItemViaSync(ctx, folderID, serverID)
}

func parseNoteItem(serverID string, app *wbxml.Element) NoteItem {
	out := NoteItem{ServerID: serverID}
	if app == nil {
		return out
	}
	for _, c := range app.Children {
		el, ok := c.(*wbxml.Element)
		if !ok {
			continue
		}
		switch el.Codepage {
		case wbxml.PageNotes:
			switch el.Name {
			case "Subject":
				out.Subject = el.TextContent()
			case "LastModifiedDate":
				out.LastModifiedDate, _ = parseEASTime(el.TextContent())
			case "Categories":
				for _, cc := range el.Children {
					if ce, ok := cc.(*wbxml.Element); ok && ce.Name == "Category" {
						out.Categories = append(out.Categories, ce.TextContent())
					}
				}
			}
		case wbxml.PageAirSyncBase:
			if el.Name == "Body" {
				for _, bc := range el.Children {
					be, ok := bc.(*wbxml.Element)
					if !ok || be.Codepage != wbxml.PageAirSyncBase {
						continue
					}
					switch be.Name {
					case "Type":
						out.BodyType = BodyType(atoi(be.TextContent()))
					case "Data":
						out.Body = be.TextContent()
					}
				}
			}
		}
	}
	return out
}

func buildNoteApp(d NoteDraft) *wbxml.Element {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData")
	if d.Subject != "" {
		app.Children = append(app.Children, wbxml.E(wbxml.PageNotes, "Subject", wbxml.Text(d.Subject)))
	}
	if d.Body != "" {
		bt := d.BodyType
		if bt == BodyTypeNone {
			bt = BodyTypePlain
		}
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageAirSyncBase, "Body",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text(itoa(int(bt)))),
				wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text(d.Body)),
			),
		)
	}
	if len(d.Categories) > 0 {
		cats := wbxml.E(wbxml.PageNotes, "Categories")
		for _, c := range d.Categories {
			cats.Children = append(cats.Children, wbxml.E(wbxml.PageNotes, "Category", wbxml.Text(c)))
		}
		app.Children = append(app.Children, cats)
	}
	if !d.LastModifiedDate.IsZero() {
		app.Children = append(app.Children, wbxml.E(wbxml.PageNotes, "LastModifiedDate",
			wbxml.Text(formatEASTime(d.LastModifiedDate))))
	}
	return app
}
