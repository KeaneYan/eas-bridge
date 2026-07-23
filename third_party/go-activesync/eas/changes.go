// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// EmailChange describes one mutation to apply against a folder via the
// AirSync Sync command. Pointer fields are sentinel-nil for "leave
// unchanged" so a caller can target one attribute without disturbing
// the others.
type EmailChange struct {
	ServerID string
	// Read set to non-nil sets the Read flag. true=read, false=unread.
	Read *bool
	// Flagged set to non-nil sets the FlagStatus. true=2 (active),
	// false=0 (clear). Use SetFlagStatus for the full code.
	Flagged *bool
	// SetFlagStatus, when non-nil, sets the FlagStatus directly to the
	// EAS code (0=clear, 1=complete, 2=active). Overrides Flagged when
	// both are set.
	SetFlagStatus *int
	// Delete sends the change as a <Delete> rather than a <Change>.
	Delete bool
}

// EmailChangeResult is the per-change status reported by the server.
// Status 1 is success.
type EmailChangeResult struct {
	ServerID string
	Status   int
}

// ApplyEmailChanges issues a Sync command with client-originated
// commands for the given folder. It bootstraps the folder if needed
// (if no SyncKey has been persisted yet) and retries once on
// InvalidSyncKey by resetting the local key.
func (c *httpClient) ApplyEmailChanges(ctx context.Context, folderID string, changes []EmailChange) ([]EmailChangeResult, error) {
	if folderID == "" {
		return nil, errors.New("eas: ApplyEmailChanges: folderID is required")
	}
	if len(changes) == 0 {
		return nil, errors.New("eas: ApplyEmailChanges: at least one change is required")
	}
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return nil, err
	}
	out, err := c.applyChangesOnce(ctx, folderID, changes)
	if err != nil && IsStatusCode(err, StatusInvalidSyncKey) {
		// Reset and try once more after re-bootstrapping.
		if rerr := c.cfg.State.SetSyncKey(ctx, folderID, "0"); rerr != nil {
			return nil, fmt.Errorf("eas: ApplyEmailChanges: reset key: %w", rerr)
		}
		if rerr := c.ensureSynced(ctx, folderID); rerr != nil {
			return nil, rerr
		}
		out, err = c.applyChangesOnce(ctx, folderID, changes)
	}
	return out, err
}

// ensureSynced makes sure the folder has a non-zero SyncKey, performing
// a single bootstrap Sync (key=0 → new key) if needed.
func (c *httpClient) ensureSynced(ctx context.Context, folderID string) error {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return fmt.Errorf("eas: read sync key: %w", err)
	}
	if key != "" && key != "0" {
		return nil
	}
	// Run one bootstrap Sync without items. We pass NoBootstrap=true so
	// SyncEmail itself doesn't try to fetch a second batch.
	if _, err := c.SyncEmail(ctx, folderID, EmailSyncOptions{NoBootstrap: true}); err != nil {
		return fmt.Errorf("eas: bootstrap sync: %w", err)
	}
	return nil
}

func (c *httpClient) applyChangesOnce(ctx context.Context, folderID string, changes []EmailChange) ([]EmailChangeResult, error) {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("eas: read sync key: %w", err)
	}
	cmds := wbxml.E(wbxml.PageAirSync, "Commands")
	for _, ch := range changes {
		if ch.ServerID == "" {
			continue
		}
		if ch.Delete {
			cmds.Children = append(cmds.Children, wbxml.E(wbxml.PageAirSync, "Delete",
				wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(ch.ServerID)),
			))
			continue
		}
		// Build the ApplicationData with only the fields we care to change.
		app := wbxml.E(wbxml.PageAirSync, "ApplicationData")
		if ch.Read != nil {
			val := "0"
			if *ch.Read {
				val = "1"
			}
			app.Children = append(app.Children,
				wbxml.E(wbxml.PageEmail, "Read", wbxml.Text(val)),
			)
		}
		if ch.SetFlagStatus != nil || ch.Flagged != nil {
			code := 0
			switch {
			case ch.SetFlagStatus != nil:
				code = *ch.SetFlagStatus
			case ch.Flagged != nil && *ch.Flagged:
				code = 2
			default:
				code = 0
			}
			app.Children = append(app.Children,
				wbxml.E(wbxml.PageEmail, "Flag",
					wbxml.E(wbxml.PageEmail, "FlagStatus", wbxml.Text(itoa(code))),
				),
			)
		}
		if len(app.Children) == 0 {
			// Nothing to send for this change; skip.
			continue
		}
		cmds.Children = append(cmds.Children, wbxml.E(wbxml.PageAirSync, "Change",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(ch.ServerID)),
			app,
		))
	}
	if len(cmds.Children) == 0 {
		return nil, errors.New("eas: ApplyEmailChanges: no effective changes")
	}

	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		// GetChanges=0 keeps the server from pushing pending items in the
		// reply: this call is for outbound mutations only.
		wbxml.E(wbxml.PageAirSync, "DeletesAsMoves", wbxml.Text("1")),
		cmds,
	)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	resp, err := c.post(ctx, "Sync", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		// Empty 200 means "all good, no per-item status to report".
		return nil, nil
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Sync", Code: st}
	}
	respCollection := resp.Root.Find("Collection")
	if respCollection == nil {
		return nil, errors.New("eas: ApplyEmailChanges: response missing <Collection>")
	}
	if cs := findShallow(respCollection, "Status", 1); cs != nil {
		if code := atoi(cs.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Sync", Code: code}
		}
	}
	if newKey := findShallow(respCollection, "SyncKey", 1); newKey != nil {
		if err := c.cfg.State.SetSyncKey(ctx, folderID, newKey.TextContent()); err != nil {
			return nil, fmt.Errorf("eas: ApplyEmailChanges: persist key: %w", err)
		}
	}

	var out []EmailChangeResult
	if responses := findShallow(respCollection, "Responses", 1); responses != nil {
		for _, c := range responses.Children {
			el, ok := c.(*wbxml.Element)
			if !ok {
				continue
			}
			r := EmailChangeResult{}
			if id := el.Find("ServerId"); id != nil {
				r.ServerID = id.TextContent()
			}
			if st := el.Find("Status"); st != nil {
				r.Status = atoi(st.TextContent())
			}
			out = append(out, r)
		}
	}
	return out, nil
}
