// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// FolderType is the EAS class of a folder. Values match MS-ASCMD §2.2.3.171.
type FolderType int

// FolderType values used by EAS 14.1.
const (
	FolderTypeUserGeneric    FolderType = 1
	FolderTypeInbox          FolderType = 2
	FolderTypeDrafts         FolderType = 3
	FolderTypeDeletedItems   FolderType = 4
	FolderTypeSentItems      FolderType = 5
	FolderTypeOutbox         FolderType = 6
	FolderTypeTasks          FolderType = 7
	FolderTypeCalendar       FolderType = 8
	FolderTypeContacts       FolderType = 9
	FolderTypeNotes          FolderType = 10
	FolderTypeJournal        FolderType = 11
	FolderTypeUserMail       FolderType = 12
	FolderTypeUserCalendar   FolderType = 13
	FolderTypeUserContacts   FolderType = 14
	FolderTypeUserTasks      FolderType = 15
	FolderTypeUserJournal    FolderType = 16
	FolderTypeUserNotes      FolderType = 17
	FolderTypeUnknown        FolderType = 18
	FolderTypeRecipientCache FolderType = 19
)

// String returns a short label for diagnostics.
func (t FolderType) String() string {
	switch t {
	case FolderTypeUserGeneric:
		return "UserGeneric"
	case FolderTypeInbox:
		return "Inbox"
	case FolderTypeDrafts:
		return "Drafts"
	case FolderTypeDeletedItems:
		return "DeletedItems"
	case FolderTypeSentItems:
		return "SentItems"
	case FolderTypeOutbox:
		return "Outbox"
	case FolderTypeTasks:
		return "Tasks"
	case FolderTypeCalendar:
		return "Calendar"
	case FolderTypeContacts:
		return "Contacts"
	case FolderTypeNotes:
		return "Notes"
	case FolderTypeJournal:
		return "Journal"
	case FolderTypeUserMail:
		return "UserMail"
	case FolderTypeUserCalendar:
		return "UserCalendar"
	case FolderTypeUserContacts:
		return "UserContacts"
	case FolderTypeUserTasks:
		return "UserTasks"
	case FolderTypeUserJournal:
		return "UserJournal"
	case FolderTypeUserNotes:
		return "UserNotes"
	case FolderTypeUnknown:
		return "Unknown"
	case FolderTypeRecipientCache:
		return "RecipientCache"
	default:
		return fmt.Sprintf("FolderType(%d)", int(t))
	}
}

// Folder is one entry in the server's folder hierarchy.
type Folder struct {
	ServerID    string
	ParentID    string
	DisplayName string
	Type        FolderType
}

// FolderSyncResult is the parsed FolderSync response.
type FolderSyncResult struct {
	// SyncKey is the new key the server returned. Already persisted in the
	// StateStore by FolderSync; surfaced here for diagnostics.
	SyncKey string
	// Added are folders the server reports as new since the prior SyncKey.
	// On the first call (SyncKey "0") this is the entire folder hierarchy.
	Added []Folder
	// Updated are folders whose metadata changed (typically a rename or
	// reparent).
	Updated []Folder
	// Deleted are server IDs of folders the user removed.
	Deleted []string
}

// FolderSync requests the folder hierarchy delta from the last persisted
// SyncKey (or the entire hierarchy if no key has been persisted).
//
// On a successful response the new SyncKey is persisted. If the server
// returns InvalidSyncKey (status 3), FolderSync resets the persisted key
// to "0" and retries once — this is the canonical recovery for a
// server-side state reset.
func (c *httpClient) FolderSync(ctx context.Context) (*FolderSyncResult, error) {
	res, err := c.foldersyncOnce(ctx)
	if err != nil && IsStatusCode(err, StatusInvalidSyncKey) {
		// Reset and retry.
		if rerr := c.cfg.State.SetSyncKey(ctx, FolderRootID, "0"); rerr != nil {
			return nil, fmt.Errorf("eas: FolderSync: reset sync key: %w", rerr)
		}
		res, err = c.foldersyncOnce(ctx)
	}
	return res, err
}

func (c *httpClient) foldersyncOnce(ctx context.Context) (*FolderSyncResult, error) {
	key, err := c.cfg.State.SyncKey(ctx, FolderRootID)
	if err != nil {
		return nil, fmt.Errorf("eas: FolderSync: read sync key: %w", err)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text(key)),
		),
	}
	resp, err := c.post(ctx, "FolderSync", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: FolderSync: empty response")
	}
	st := topStatus(resp.Root)
	if st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "FolderSync", Code: st}
	}
	newKeyEl := resp.Root.Find("SyncKey")
	if newKeyEl == nil {
		return nil, errors.New("eas: FolderSync: response missing SyncKey")
	}
	newKey := newKeyEl.TextContent()
	out := &FolderSyncResult{SyncKey: newKey}

	if changes := resp.Root.Find("Changes"); changes != nil {
		for _, c := range changes.Children {
			el, ok := c.(*wbxml.Element)
			if !ok {
				continue
			}
			switch el.Name {
			case "Add":
				out.Added = append(out.Added, parseFolder(el))
			case "Update":
				out.Updated = append(out.Updated, parseFolder(el))
			case "Delete":
				if id := el.Find("ServerId"); id != nil {
					out.Deleted = append(out.Deleted, id.TextContent())
				}
			}
		}
	}

	if err := c.cfg.State.SetSyncKey(ctx, FolderRootID, newKey); err != nil {
		return nil, fmt.Errorf("eas: FolderSync: persist sync key: %w", err)
	}
	return out, nil
}

func parseFolder(e *wbxml.Element) Folder {
	f := Folder{}
	for _, c := range e.Children {
		el, ok := c.(*wbxml.Element)
		if !ok {
			continue
		}
		switch el.Name {
		case "ServerId":
			f.ServerID = el.TextContent()
		case "ParentId":
			f.ParentID = el.TextContent()
		case "DisplayName":
			f.DisplayName = el.TextContent()
		case "Type":
			f.Type = FolderType(atoi(el.TextContent()))
		}
	}
	return f
}
