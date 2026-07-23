// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/wbxml"
)

// ItemEstimate is the per-collection result of GetItemEstimate.
type ItemEstimate struct {
	CollectionID string
	Class        string
	Estimate     int
	Status       int
}

// GetItemEstimate asks the server how many items are pending sync for
// each (folder, syncKey) pair the client supplies. Useful for "show
// unread count" displays without performing a full Sync.
//
// folderIDs is the list of collections to query. The client must have
// a prior SyncKey for each (use ensureSynced or call SyncEmail/Calendar
// first to bootstrap).
func (c *httpClient) GetItemEstimate(ctx context.Context, folderIDs []string) ([]ItemEstimate, error) {
	if len(folderIDs) == 0 {
		return nil, errors.New("eas: GetItemEstimate: at least one folderID required")
	}
	collections := wbxml.E(wbxml.PageGetItemEstimate, "Collections")
	for _, fid := range folderIDs {
		key, err := c.cfg.State.SyncKey(ctx, fid)
		if err != nil {
			return nil, err
		}
		coll := wbxml.E(wbxml.PageGetItemEstimate, "Collection",
			// SyncKey is required and lives in the AirSync namespace.
			wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
			wbxml.E(wbxml.PageGetItemEstimate, "CollectionId", wbxml.Text(fid)),
		)
		collections.Children = append(collections.Children, coll)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageGetItemEstimate, "GetItemEstimate", collections),
	}
	resp, err := c.post(ctx, "GetItemEstimate", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: GetItemEstimate: empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "GetItemEstimate", Code: st}
	}
	var out []ItemEstimate
	for _, c := range resp.Root.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Name != "Response" {
			continue
		}
		ie := ItemEstimate{}
		if st := el.Find("Status"); st != nil {
			ie.Status = atoi(st.TextContent())
		}
		if coll := el.Find("Collection"); coll != nil {
			if cid := coll.Find("CollectionId"); cid != nil {
				ie.CollectionID = cid.TextContent()
			}
			if cl := coll.Find("Class"); cl != nil {
				ie.Class = cl.TextContent()
			}
			if est := coll.Find("Estimate"); est != nil {
				ie.Estimate = atoi(est.TextContent())
			}
		}
		out = append(out, ie)
	}
	return out, nil
}
