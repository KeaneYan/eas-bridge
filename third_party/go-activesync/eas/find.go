// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// FindOptions controls a Find request (EAS 16.0+).
type FindOptions struct {
	// FolderID restricts the search to a single folder. Empty searches
	// the inbox by default per server policy.
	FolderID string
	// DeepTraversal includes subfolders when FolderID is set.
	DeepTraversal bool
	// Range is the result window in EAS "<start>-<end>" form, inclusive.
	// Default "0-49".
	Range string
	// PreviewBytes requests an HTML preview of each hit, up to N bytes.
	PreviewBytes int
}

// FindResult is the parsed Find response.
type FindResult struct {
	Hits  []FindHit
	Range string
	Total int
}

// FindHit is one entry returned by Find. Properties are returned as
// raw EmailItem fields (subject, from, etc.) plus a few Find-specific
// fields like Preview.
type FindHit struct {
	Item           EmailItem
	Preview        string
	HasAttachments bool
}

// FindEmail issues an EAS 16.0+ Find command for messages matching
// the free-text query. Find is a richer alternative to Search with
// structured query operators and HTML previews; servers that don't
// implement it (Z-Push <2.7, SOGo) will reject with an HTTP error.
//
// Most callers should use SearchEmail (works on 14.x); Find is
// available for callers that target newer servers.
func (c *httpClient) FindEmail(ctx context.Context, query string, opts FindOptions) (*FindResult, error) {
	if query == "" {
		return nil, errors.New("eas: FindEmail: query is required")
	}
	if opts.Range == "" {
		opts.Range = "0-49"
	}
	criterion := wbxml.E(wbxml.PageFind, "MailBoxSearchCriterion",
		wbxml.E(wbxml.PageFind, "Query",
			wbxml.E(wbxml.PageFind, "FreeText", wbxml.Text(query)),
		),
	)
	if opts.FolderID != "" {
		criterion.Children = append(criterion.Children,
			wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(opts.FolderID)))
	}
	if opts.DeepTraversal {
		criterion.Children = append(criterion.Children, wbxml.E(wbxml.PageFind, "DeepTraversal"))
	}

	options := wbxml.E(wbxml.PageFind, "Options",
		wbxml.E(wbxml.PageFind, "Range", wbxml.Text(opts.Range)),
	)
	if opts.PreviewBytes > 0 {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageAirSyncBase, "BodyPreference",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "TruncationSize", wbxml.Text(itoa(opts.PreviewBytes))),
			))
	}

	exec := wbxml.E(wbxml.PageFind, "ExecuteSearch", criterion)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFind, "Find", exec, options),
	}
	resp, err := c.post(ctx, "Find", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: Find: empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Find", Code: st}
	}
	out := &FindResult{}
	if r := resp.Root.Find("Range"); r != nil {
		out.Range = r.TextContent()
	}
	if t := resp.Root.Find("Total"); t != nil {
		out.Total = atoi(t.TextContent())
	}
	for _, c := range resp.Root.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Name != "Response" {
			continue
		}
		for _, rc := range el.Children {
			re, ok := rc.(*wbxml.Element)
			if !ok || re.Name != "Result" {
				continue
			}
			hit := FindHit{}
			id := ""
			if sid := re.Find("ServerId"); sid != nil {
				id = sid.TextContent()
			}
			if props := re.Find("Properties"); props != nil {
				hit.Item = parseEmailItem(id, props)
			}
			if pv := re.Find("Preview"); pv != nil {
				hit.Preview = pv.TextContent()
			}
			if ha := re.Find("HasAttachments"); ha != nil {
				hit.HasAttachments = ha.TextContent() == "1"
			}
			out.Hits = append(out.Hits, hit)
		}
	}
	if out.Range == "" && out.Total == 0 {
		// 0-result response is OK — don't error.
		return out, nil
	}
	if out.Range == "" {
		return nil, fmt.Errorf("eas: Find: response missing <Range>")
	}
	return out, nil
}
