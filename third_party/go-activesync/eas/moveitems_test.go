// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestMoveItems_oneItem(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMove, "MoveItems",
				wbxml.E(wbxml.PageMove, "Response",
					wbxml.E(wbxml.PageMove, "SrcMsgId", wbxml.Text("inbox:42")),
					wbxml.E(wbxml.PageMove, "Status", wbxml.Text("3")),
					wbxml.E(wbxml.PageMove, "DstMsgId", wbxml.Text("archive:1")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	results, err := c.MoveItems(context.Background(), "inbox", "archive", []string{"inbox:42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d", len(results))
	}
	if results[0].SrcServerID != "inbox:42" || results[0].DstServerID != "archive:1" || results[0].Status != 3 {
		t.Errorf("got %+v", results[0])
	}

	// Request shape.
	if cap.url.Query().Get("Cmd") != "MoveItems" {
		t.Errorf("Cmd: %q", cap.url.Query().Get("Cmd"))
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	move := req.Root.Find("Move")
	if move == nil {
		t.Fatal("Move missing")
	}
	for _, want := range []struct {
		name, val string
	}{
		{"SrcMsgId", "inbox:42"},
		{"SrcFldId", "inbox"},
		{"DstFldId", "archive"},
	} {
		el := move.Find(want.name)
		if el == nil || el.TextContent() != want.val {
			t.Errorf("%s = %v", want.name, el)
		}
	}
}

func TestMoveItems_multipleItems(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMove, "MoveItems",
				wbxml.E(wbxml.PageMove, "Response",
					wbxml.E(wbxml.PageMove, "SrcMsgId", wbxml.Text("a")),
					wbxml.E(wbxml.PageMove, "Status", wbxml.Text("3")),
					wbxml.E(wbxml.PageMove, "DstMsgId", wbxml.Text("a-new")),
				),
				wbxml.E(wbxml.PageMove, "Response",
					wbxml.E(wbxml.PageMove, "SrcMsgId", wbxml.Text("b")),
					wbxml.E(wbxml.PageMove, "Status", wbxml.Text("3")),
					wbxml.E(wbxml.PageMove, "DstMsgId", wbxml.Text("b-new")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	results, err := c.MoveItems(context.Background(), "src", "dst", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].DstServerID != "a-new" || results[1].DstServerID != "b-new" {
		t.Errorf("results = %+v", results)
	}
}

func TestMoveItems_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	if _, err := c.MoveItems(context.Background(), "src", "dst", []string{"a"}); err == nil {
		t.Error("want HTTP error")
	}
}

func TestMoveItems_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → nil resp
	})
	_, err := c.MoveItems(context.Background(), "src", "dst", []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Errorf("err = %v", err)
	}
}

func TestMoveItems_skipsNonResponseChildren(t *testing.T) {
	// Server may include other elements (e.g. <Status>) at the top level;
	// the parser must skip anything that isn't a <Response>.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMove, "MoveItems",
				wbxml.E(wbxml.PageMove, "Status", wbxml.Text("1")), // not a Response
				wbxml.E(wbxml.PageMove, "Response",
					wbxml.E(wbxml.PageMove, "SrcMsgId", wbxml.Text("a")),
					wbxml.E(wbxml.PageMove, "DstMsgId", wbxml.Text("a-new")),
					wbxml.E(wbxml.PageMove, "Status", wbxml.Text("3")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.MoveItems(context.Background(), "src", "dst", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].SrcServerID != "a" {
		t.Errorf("res = %+v", res)
	}
}

func TestMoveItems_validation(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	cases := []struct {
		src, dst string
		ids      []string
		want     string
	}{
		{"", "x", []string{"a"}, "srcFolder"},
		{"x", "", []string{"a"}, "srcFolder"},
		{"x", "y", nil, "id"},
	}
	for _, tc := range cases {
		_, err := c.MoveItems(context.Background(), tc.src, tc.dst, tc.ids)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("(%q,%q,%v): err = %v, want substring %q", tc.src, tc.dst, tc.ids, err, tc.want)
		}
	}
}
