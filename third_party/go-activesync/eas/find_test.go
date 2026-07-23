// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestFindEmail_basic(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFind, "Find",
				wbxml.E(wbxml.PageFind, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFind, "Range", wbxml.Text("0-1")),
				wbxml.E(wbxml.PageFind, "Total", wbxml.Text("2")),
				wbxml.E(wbxml.PageFind, "Response",
					wbxml.E(wbxml.PageFind, "Result",
						wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("hit-1")),
						wbxml.E(wbxml.PageFind, "Properties",
							wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("Test")),
							wbxml.E(wbxml.PageEmail, "From", wbxml.Text("alice@x")),
						),
						wbxml.E(wbxml.PageFind, "Preview", wbxml.Text("This is a preview")),
						wbxml.E(wbxml.PageFind, "HasAttachments", wbxml.Text("1")),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.FindEmail(context.Background(), "test", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 || len(res.Hits) != 1 {
		t.Errorf("res = %+v", res)
	}
	if res.Hits[0].Item.Subject != "Test" || res.Hits[0].Preview != "This is a preview" {
		t.Errorf("hit = %+v", res.Hits[0])
	}
	if !res.Hits[0].HasAttachments {
		t.Errorf("HasAttachments = false")
	}
}

func TestFindEmail_emptyQuery(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	if _, err := c.FindEmail(context.Background(), "", FindOptions{}); err == nil {
		t.Error("want error")
	}
}

func TestFindEmail_emitsAllOptions(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	_, _ = c.FindEmail(context.Background(), "q", FindOptions{
		FolderID:      "inbox",
		DeepTraversal: true,
		PreviewBytes:  4096,
	})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if req.Root.Find("CollectionId") == nil {
		t.Error("CollectionId missing — FolderID branch not emitted")
	}
	if req.Root.Find("DeepTraversal") == nil {
		t.Error("DeepTraversal missing")
	}
	bp := req.Root.Find("BodyPreference")
	if bp == nil {
		t.Fatal("BodyPreference missing — PreviewBytes branch not emitted")
	}
	if ts := bp.Find("TruncationSize"); ts == nil || ts.TextContent() != "4096" {
		t.Errorf("TruncationSize = %v", ts)
	}
}

func TestFindEmail_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.FindEmail(context.Background(), "q", FindOptions{}); err == nil {
		t.Error("want HTTP error")
	}
}

func TestFindEmail_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → nil resp
	})
	if _, err := c.FindEmail(context.Background(), "q", FindOptions{}); err == nil {
		t.Error("want error on empty response")
	}
}

func TestFindEmail_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFind, "Find",
				wbxml.E(wbxml.PageFind, "Status", wbxml.Text("4")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.FindEmail(context.Background(), "q", FindOptions{}); !IsStatusCode(err, 4) {
		t.Errorf("err = %v", err)
	}
}

func TestFindEmail_zeroResultsAccepted(t *testing.T) {
	// Server returns Status=1 but no Range/Total/Response — caller wants
	// an empty FindResult, not an error.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFind, "Find",
				wbxml.E(wbxml.PageFind, "Status", wbxml.Text("1")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.FindEmail(context.Background(), "q", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 || res.Total != 0 {
		t.Errorf("res = %+v", res)
	}
}

func TestFindEmail_missingRangeWithTotal(t *testing.T) {
	// Server reports Total but no Range — that's a malformed response.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFind, "Find",
				wbxml.E(wbxml.PageFind, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFind, "Total", wbxml.Text("3")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.FindEmail(context.Background(), "q", FindOptions{}); err == nil {
		t.Error("want error for Total without Range")
	}
}

func TestFindEmail_skipsNonResultChildren(t *testing.T) {
	// Inside <Response> only <Result> children should be parsed.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageFind, "Find",
				wbxml.E(wbxml.PageFind, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageFind, "Range", wbxml.Text("0-0")),
				wbxml.E(wbxml.PageFind, "Total", wbxml.Text("1")),
				// First top-level entry is not a <Response> — must skip.
				wbxml.E(wbxml.PageFind, "SearchId", wbxml.Text("ignored")),
				wbxml.E(wbxml.PageFind, "Response",
					// Stray child that isn't a <Result> — must skip.
					wbxml.E(wbxml.PageFind, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageFind, "Result",
						wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("hit")),
						wbxml.E(wbxml.PageFind, "Properties",
							wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("S")),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.FindEmail(context.Background(), "q", FindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Item.Subject != "S" {
		t.Errorf("hits = %+v", res.Hits)
	}
}
