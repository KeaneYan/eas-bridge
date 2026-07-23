// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hstern/go-activesync/wbxml"
)

// EmailSearchOptions controls a SearchEmail request.
type EmailSearchOptions struct {
	// FolderID restricts the search to a single folder. Empty searches all
	// folders.
	FolderID string
	// DeepTraversal includes subfolders when FolderID is set.
	DeepTraversal bool
	// Range is the result window in EAS "<start>-<end>" form, inclusive
	// (e.g. "0-49" for the first 50 hits). Default "0-49".
	Range string
	// BodyPreviewBytes limits the per-hit body preview length. Default 256.
	BodyPreviewBytes int
	// RebuildResults forces the server to recompute its search index
	// rather than serving cached results. Off by default.
	RebuildResults bool
}

// EmailSearchResult is the parsed Search response.
type EmailSearchResult struct {
	// Items in result order. ServerID is set from the LongId element.
	Items []EmailItem
	// Range echoes the server's reported result window.
	Range string
	// Total is the server's estimate of the total matching items.
	Total int
}

// SearchEmail issues an EAS Search command against the Mailbox store and
// returns matching emails.
//
// EAS Search is a server-side full-text query; it does not require a
// SyncKey and does not advance per-folder Sync state.
func (c *httpClient) SearchEmail(ctx context.Context, query string, opts EmailSearchOptions) (*EmailSearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("eas: SearchEmail: query is required")
	}
	if opts.Range == "" {
		opts.Range = "0-49"
	}
	if opts.BodyPreviewBytes <= 0 {
		opts.BodyPreviewBytes = 256
	}

	// Build the And-clause: Class=Email plus optional CollectionId plus
	// FreeText. EAS uses AirSync namespace for Class/CollectionId.
	andEl := wbxml.E(wbxml.PageSearch, "And",
		wbxml.E(wbxml.PageAirSync, "Class", wbxml.Text("Email")),
	)
	if opts.FolderID != "" {
		andEl.Children = append(andEl.Children,
			wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(opts.FolderID)),
		)
	}
	andEl.Children = append(andEl.Children,
		wbxml.E(wbxml.PageSearch, "FreeText", wbxml.Text(query)),
	)

	options := wbxml.E(wbxml.PageSearch, "Options",
		wbxml.E(wbxml.PageSearch, "Range", wbxml.Text(opts.Range)),
		wbxml.E(wbxml.PageAirSyncBase, "BodyPreference",
			wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSyncBase, "TruncationSize", wbxml.Text(itoa(opts.BodyPreviewBytes))),
		),
	)
	if opts.RebuildResults {
		options.Children = append([]wbxml.Node{
			wbxml.E(wbxml.PageSearch, "RebuildResults"),
		}, options.Children...)
	}
	if opts.DeepTraversal && opts.FolderID != "" {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageSearch, "DeepTraversal"),
		)
	}

	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSearch, "Search",
			wbxml.E(wbxml.PageSearch, "Store",
				wbxml.E(wbxml.PageSearch, "Name", wbxml.Text("Mailbox")),
				wbxml.E(wbxml.PageSearch, "Query", andEl),
				options,
			),
		),
	}

	return c.runSearch(ctx, doc, opts)
}

// searchStructured is SearchEmail with a caller-provided Query AST
// instead of the (And Class=Email + CollectionId? + FreeText) tree
// SearchEmail builds.
func (c *httpClient) searchStructured(ctx context.Context, q Query, opts EmailSearchOptions) (*EmailSearchResult, error) {
	if q == nil {
		return nil, errors.New("eas: SearchEmailQuery: query is required")
	}
	if opts.Range == "" {
		opts.Range = "0-49"
	}
	if opts.BodyPreviewBytes <= 0 {
		opts.BodyPreviewBytes = 256
	}
	options := wbxml.E(wbxml.PageSearch, "Options",
		wbxml.E(wbxml.PageSearch, "Range", wbxml.Text(opts.Range)),
		wbxml.E(wbxml.PageAirSyncBase, "BodyPreference",
			wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSyncBase, "TruncationSize", wbxml.Text(itoa(opts.BodyPreviewBytes))),
		),
	)
	if opts.RebuildResults {
		options.Children = append([]wbxml.Node{
			wbxml.E(wbxml.PageSearch, "RebuildResults"),
		}, options.Children...)
	}
	if opts.DeepTraversal {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageSearch, "DeepTraversal"))
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSearch, "Search",
			wbxml.E(wbxml.PageSearch, "Store",
				wbxml.E(wbxml.PageSearch, "Name", wbxml.Text("Mailbox")),
				wbxml.E(wbxml.PageSearch, "Query", q.encode()),
				options,
			),
		),
	}
	return c.runSearch(ctx, doc, opts)
}

// runSearch issues the Search request built by SearchEmail or
// searchStructured and parses the result.
func (c *httpClient) runSearch(ctx context.Context, doc *wbxml.Document, opts EmailSearchOptions) (*EmailSearchResult, error) {
	_ = opts // reserved for future per-call tuning
	resp, err := c.post(ctx, "Search", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: Search: empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Search", Code: st}
	}

	store := resp.Root.Find("Store")
	if store == nil {
		return nil, errors.New("eas: Search: response missing <Store>")
	}
	// Per-Store Status (separate from the top-level Search status).
	if sst := findShallow(store, "Status", 1); sst != nil {
		if code := atoi(sst.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Search/Store", Code: code}
		}
	}

	out := &EmailSearchResult{}
	if r := store.Find("Range"); r != nil {
		out.Range = r.TextContent()
	}
	if t := store.Find("Total"); t != nil {
		out.Total = atoi(t.TextContent())
	}
	for _, c := range store.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Name != "Result" {
			continue
		}
		var serverID string
		if id := el.Find("LongId"); id != nil {
			serverID = id.TextContent()
		}
		props := el.Find("Properties")
		item := parseEmailItem(serverID, props)
		// CollectionId may also appear at Result level.
		if cid := findShallow(el, "CollectionId", 1); cid != nil && item.ServerID == "" {
			item.ServerID = cid.TextContent() + ":" + item.ServerID
		}
		if item.ServerID != "" {
			out.Items = append(out.Items, item)
		}
	}
	if out.Range == "" && out.Total == 0 && len(out.Items) == 0 {
		// Be tolerant of servers that return no results without a Range.
		return out, nil
	}
	if out.Range == "" {
		return nil, fmt.Errorf("eas: Search: response missing <Range>")
	}
	return out, nil
}
