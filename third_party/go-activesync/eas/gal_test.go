// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestGALSearch_parsesEntries(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSearch, "Search",
				wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSearch, "Response",
					wbxml.E(wbxml.PageSearch, "Store",
						wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageSearch, "Result",
							wbxml.E(wbxml.PageSearch, "Properties",
								wbxml.E(wbxml.PageGAL, "DisplayName", wbxml.Text("Alice Engineer")),
								wbxml.E(wbxml.PageGAL, "EmailAddress", wbxml.Text("alice@corp.com")),
								wbxml.E(wbxml.PageGAL, "Title", wbxml.Text("Staff SWE")),
							),
						),
						wbxml.E(wbxml.PageSearch, "Range", wbxml.Text("0-0")),
						wbxml.E(wbxml.PageSearch, "Total", wbxml.Text("1")),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.GALSearch(context.Background(), "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Entries) != 1 {
		t.Errorf("res = %+v", res)
	}
	if res.Entries[0].EmailAddress != "alice@corp.com" {
		t.Errorf("entry = %+v", res.Entries[0])
	}
}

func TestParseGALEntry_allFields(t *testing.T) {
	props := wbxml.E(wbxml.PageSearch, "Properties",
		wbxml.E(wbxml.PageGAL, "DisplayName", wbxml.Text("Alice Engineer")),
		wbxml.E(wbxml.PageGAL, "FirstName", wbxml.Text("Alice")),
		wbxml.E(wbxml.PageGAL, "LastName", wbxml.Text("Engineer")),
		wbxml.E(wbxml.PageGAL, "Title", wbxml.Text("Staff SWE")),
		wbxml.E(wbxml.PageGAL, "Office", wbxml.Text("HQ-3")),
		wbxml.E(wbxml.PageGAL, "Company", wbxml.Text("Acme")),
		wbxml.E(wbxml.PageGAL, "Alias", wbxml.Text("aengineer")),
		wbxml.E(wbxml.PageGAL, "EmailAddress", wbxml.Text("alice@x")),
		wbxml.E(wbxml.PageGAL, "Phone", wbxml.Text("+1-555-0100")),
		wbxml.E(wbxml.PageGAL, "HomePhone", wbxml.Text("+1-555-0200")),
		wbxml.E(wbxml.PageGAL, "MobilePhone", wbxml.Text("+1-555-0300")),
		// Wrong codepage: must be skipped.
		wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("ignored")),
	)
	got := parseGALEntry(props)
	want := GALEntry{
		DisplayName:  "Alice Engineer",
		FirstName:    "Alice",
		LastName:     "Engineer",
		Title:        "Staff SWE",
		Office:       "HQ-3",
		Company:      "Acme",
		Alias:        "aengineer",
		EmailAddress: "alice@x",
		Phone:        "+1-555-0100",
		HomePhone:    "+1-555-0200",
		MobilePhone:  "+1-555-0300",
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestGALSearch_emptyQueryRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit")
	})
	if _, err := c.GALSearch(context.Background(), "", 5); err == nil {
		t.Error("want error for empty query")
	}
}

func TestGALSearch_defaultsLimit(t *testing.T) {
	// limit=0 must use the default range (0-24).
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	_, _ = c.GALSearch(context.Background(), "alice", 0)
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if r := req.Root.Find("Range"); r == nil || r.TextContent() != "0-24" {
		t.Errorf("Range = %v, want '0-24' for default limit", r)
	}
}

func TestGALSearch_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.GALSearch(context.Background(), "alice", 5); err == nil {
		t.Error("want HTTP error")
	}
}

func TestGALSearch_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → nil resp
	})
	if _, err := c.GALSearch(context.Background(), "alice", 5); err == nil {
		t.Error("want error on empty response")
	}
}

func TestGALSearch_missingStoreRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSearch, "Search",
				wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("1")),
				// no <Store>
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.GALSearch(context.Background(), "alice", 5); err == nil {
		t.Error("want error when Store is missing")
	}
}

func TestGALSearch_nestedStoreStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSearch, "Search",
				wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSearch, "Store",
					wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("4")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.GALSearch(context.Background(), "alice", 5); !IsStatusCode(err, 4) {
		t.Errorf("err = %v", err)
	}
}

func TestGALSearch_resultWithoutPropertiesSkipped(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSearch, "Search",
				wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSearch, "Store",
					wbxml.E(wbxml.PageSearch, "Result"), // no Properties — must skip
					wbxml.E(wbxml.PageSearch, "Result",
						wbxml.E(wbxml.PageSearch, "Properties",
							wbxml.E(wbxml.PageGAL, "EmailAddress", wbxml.Text("alice@x")),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.GALSearch(context.Background(), "alice", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].EmailAddress != "alice@x" {
		t.Errorf("entries = %+v", res.Entries)
	}
}

func TestGALSearch_serverError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSearch, "Search",
				wbxml.E(wbxml.PageSearch, "Status", wbxml.Text("3")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.GALSearch(context.Background(), "alice", 10); err == nil {
		t.Error("want error for non-OK status")
	}
}
