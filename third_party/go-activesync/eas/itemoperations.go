// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// FetchEmailOptions controls a FetchEmail request.
type FetchEmailOptions struct {
	BodyType           BodyType // default BodyTypeMIME for full fidelity
	BodyTruncationSize int      // 0 = full body
}

// FetchEmail issues an ItemOperations Fetch for one email by
// (folderID, serverID). Returns the full ApplicationData parsed into an
// EmailItem.
//
// The Sync command also returns email content, but typically truncated to
// fit within a sync window. Use FetchEmail when the caller wants the full
// body or the raw MIME source.
func (c *httpClient) FetchEmail(ctx context.Context, folderID, serverID string, opts FetchEmailOptions) (*EmailItem, error) {
	if folderID == "" || serverID == "" {
		return nil, errors.New("eas: FetchEmail: folderID and serverID are required")
	}
	if opts.BodyType == BodyTypeNone {
		opts.BodyType = BodyTypeMIME
	}

	bodyPref := wbxml.E(wbxml.PageAirSyncBase, "BodyPreference",
		wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text(itoa(int(opts.BodyType)))),
	)
	if opts.BodyTruncationSize > 0 {
		bodyPref.Children = append(bodyPref.Children,
			wbxml.E(wbxml.PageAirSyncBase, "TruncationSize", wbxml.Text(itoa(opts.BodyTruncationSize))),
		)
	}

	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
			wbxml.E(wbxml.PageItemOperations, "Fetch",
				wbxml.E(wbxml.PageItemOperations, "Store", wbxml.Text("Mailbox")),
				wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
				wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
				wbxml.E(wbxml.PageItemOperations, "Options", bodyPref),
			),
		),
	}

	resp, err := c.post(ctx, "ItemOperations", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: ItemOperations: empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "ItemOperations", Code: st}
	}

	resps := resp.Root.FindAll("Response")
	if len(resps) == 0 {
		return nil, errors.New("eas: ItemOperations: response missing <Response>")
	}
	// Find the Fetch sub-response (the ItemOperations command can wrap
	// other operations, but FetchEmail issues only one).
	for _, r := range resps {
		fetch := r.Find("Fetch")
		if fetch == nil {
			continue
		}
		// Per-fetch Status comes before Properties.
		if fst := fetch.Find("Status"); fst != nil {
			if code := atoi(fst.TextContent()); code != 0 && code != StatusOK {
				return nil, &StatusError{Command: "ItemOperations/Fetch", Code: code}
			}
		}
		props := fetch.Find("Properties")
		if props == nil {
			return nil, fmt.Errorf("eas: ItemOperations: Fetch missing <Properties>")
		}
		item := parseEmailItem(serverID, props)
		// Server may echo CollectionId/ServerId next to Properties.
		if id := fetch.Find("ServerId"); id != nil {
			item.ServerID = id.TextContent()
		}
		return &item, nil
	}
	return nil, errors.New("eas: ItemOperations: no Fetch response")
}
