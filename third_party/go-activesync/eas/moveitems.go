// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/wbxml"
)

// MoveItemResult is the per-item outcome of MoveItems.
type MoveItemResult struct {
	SrcServerID string
	DstServerID string // assigned by destination folder
	Status      int
}

// MoveItems moves one or more items from srcFolder to dstFolder.
// Returns a per-item result list parallel to the input ids.
func (c *httpClient) MoveItems(ctx context.Context, srcFolder, dstFolder string, ids []string) ([]MoveItemResult, error) {
	if srcFolder == "" || dstFolder == "" {
		return nil, errors.New("eas: MoveItems: srcFolder and dstFolder are required")
	}
	if len(ids) == 0 {
		return nil, errors.New("eas: MoveItems: at least one id is required")
	}
	root := wbxml.E(wbxml.PageMove, "MoveItems")
	for _, id := range ids {
		root.Children = append(root.Children, wbxml.E(wbxml.PageMove, "Move",
			wbxml.E(wbxml.PageMove, "SrcMsgId", wbxml.Text(id)),
			wbxml.E(wbxml.PageMove, "SrcFldId", wbxml.Text(srcFolder)),
			wbxml.E(wbxml.PageMove, "DstFldId", wbxml.Text(dstFolder)),
		))
	}
	resp, err := c.post(ctx, "MoveItems", &wbxml.Document{Root: root})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: MoveItems: empty response")
	}
	out := make([]MoveItemResult, 0, len(ids))
	for _, c := range resp.Root.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Name != "Response" {
			continue
		}
		r := MoveItemResult{}
		if id := el.Find("SrcMsgId"); id != nil {
			r.SrcServerID = id.TextContent()
		}
		if id := el.Find("DstMsgId"); id != nil {
			r.DstServerID = id.TextContent()
		}
		if st := el.Find("Status"); st != nil {
			r.Status = atoi(st.TextContent())
		}
		out = append(out, r)
	}
	return out, nil
}
