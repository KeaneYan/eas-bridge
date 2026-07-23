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

// GALEntry is a Global Address List directory entry.
type GALEntry struct {
	DisplayName  string
	FirstName    string
	LastName     string
	Title        string
	Office       string
	Company      string
	Alias        string
	EmailAddress string
	Phone        string
	HomePhone    string
	MobilePhone  string
}

// GALSearchResult is the parsed GAL search response.
type GALSearchResult struct {
	Entries []GALEntry
	Range   string
	Total   int
}

// GALSearch issues a Search command against the GAL store. Useful for
// directory lookups ("find Alice's email address") without syncing the
// full corporate address book.
func (c *httpClient) GALSearch(ctx context.Context, query string, limit int) (*GALSearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("eas: GALSearch: query is required")
	}
	if limit <= 0 {
		limit = 25
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSearch, "Search",
			wbxml.E(wbxml.PageSearch, "Store",
				wbxml.E(wbxml.PageSearch, "Name", wbxml.Text("GAL")),
				wbxml.E(wbxml.PageSearch, "Query", wbxml.Text(query)),
				wbxml.E(wbxml.PageSearch, "Options",
					wbxml.E(wbxml.PageSearch, "Range", wbxml.Text(fmt.Sprintf("0-%d", limit-1))),
				),
			),
		),
	}
	resp, err := c.post(ctx, "Search", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: GALSearch: empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Search/GAL", Code: st}
	}
	store := resp.Root.Find("Store")
	if store == nil {
		return nil, errors.New("eas: GALSearch: response missing <Store>")
	}
	if sst := findShallow(store, "Status", 1); sst != nil {
		if code := atoi(sst.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Search/GAL", Code: code}
		}
	}
	out := &GALSearchResult{}
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
		props := el.Find("Properties")
		if props == nil {
			continue
		}
		entry := parseGALEntry(props)
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}

func parseGALEntry(props *wbxml.Element) GALEntry {
	out := GALEntry{}
	for _, c := range props.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Codepage != wbxml.PageGAL {
			continue
		}
		switch el.Name {
		case "DisplayName":
			out.DisplayName = el.TextContent()
		case "FirstName":
			out.FirstName = el.TextContent()
		case "LastName":
			out.LastName = el.TextContent()
		case "Title":
			out.Title = el.TextContent()
		case "Office":
			out.Office = el.TextContent()
		case "Company":
			out.Company = el.TextContent()
		case "Alias":
			out.Alias = el.TextContent()
		case "EmailAddress":
			out.EmailAddress = el.TextContent()
		case "Phone":
			out.Phone = el.TextContent()
		case "HomePhone":
			out.HomePhone = el.TextContent()
		case "MobilePhone":
			out.MobilePhone = el.TextContent()
		}
	}
	return out
}
