// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func itemopFetchResponse(serverID string, mime []byte) []byte {
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
			wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageItemOperations, "Response",
				wbxml.E(wbxml.PageItemOperations, "Fetch",
					wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
					wbxml.E(wbxml.PageItemOperations, "Properties",
						wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("Full subject")),
						wbxml.E(wbxml.PageEmail, "From", wbxml.Text("alice@x")),
						wbxml.E(wbxml.PageAirSyncBase, "Body",
							wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("4")),
							wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Opaque(mime)),
						),
					),
				),
			),
		),
	}
	out, err := wbxml.Marshal(doc, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return out
}

func TestFetchEmail_returnsMIME(t *testing.T) {
	mime := []byte("From: alice@x\r\nSubject: Full subject\r\n\r\nbody")
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(itemopFetchResponse("inbox:42", mime))
	})

	got, err := c.FetchEmail(context.Background(), "inbox", "inbox:42", FetchEmailOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerID != "inbox:42" {
		t.Errorf("ServerID = %q", got.ServerID)
	}
	if got.Subject != "Full subject" {
		t.Errorf("Subject = %q", got.Subject)
	}
	if got.BodyType != BodyTypeMIME {
		t.Errorf("BodyType = %v", got.BodyType)
	}
	if !bytes.Equal(got.BodyMIME, mime) {
		t.Errorf("BodyMIME mismatch")
	}

	// Verify the request shape: Cmd=ItemOperations, contains BodyPreference
	// with Type=4.
	if cap.url.Query().Get("Cmd") != "ItemOperations" {
		t.Errorf("Cmd: %q", cap.url.Query().Get("Cmd"))
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if req.Root.Find("BodyPreference") == nil {
		t.Error("request missing BodyPreference")
	}
	if t2 := req.Root.Find("BodyPreference").Find("Type"); t2 == nil || t2.TextContent() != "4" {
		t.Errorf("body Type = %v", t2)
	}
}

func TestFetchEmail_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("110")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.FetchEmail(context.Background(), "inbox", "x", FetchEmailOptions{})
	if !IsStatusCode(err, 110) {
		t.Errorf("err = %v", err)
	}
}

func TestFetchEmail_perFetchStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageItemOperations, "Response",
					wbxml.E(wbxml.PageItemOperations, "Fetch",
						wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("8")), // ObjectNotFound
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.FetchEmail(context.Background(), "inbox", "ghost", FetchEmailOptions{})
	if !IsStatusCode(err, 8) {
		t.Errorf("err = %v", err)
	}
}

func TestFetchEmail_emptyArgs(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	if _, err := c.FetchEmail(context.Background(), "", "x", FetchEmailOptions{}); err == nil {
		t.Error("want error for empty folder")
	}
	if _, err := c.FetchEmail(context.Background(), "f", "", FetchEmailOptions{}); err == nil {
		t.Error("want error for empty server id")
	}
}

func TestFetchEmail_emitsTruncationSize(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(itemopFetchResponse("inbox:42", []byte("x")))
	})
	_, _ = c.FetchEmail(context.Background(), "inbox", "inbox:42", FetchEmailOptions{
		BodyType:           BodyTypeMIME,
		BodyTruncationSize: 4096,
	})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	bp := req.Root.Find("BodyPreference")
	if ts := bp.Find("TruncationSize"); ts == nil || ts.TextContent() != "4096" {
		t.Errorf("TruncationSize = %v", ts)
	}
}

func TestFetchEmail_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.FetchEmail(context.Background(), "inbox", "x", FetchEmailOptions{}); err == nil {
		t.Error("want HTTP error")
	}
}

func TestFetchEmail_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	if _, err := c.FetchEmail(context.Background(), "inbox", "x", FetchEmailOptions{}); err == nil {
		t.Error("want error on empty response")
	}
}

func TestFetchEmail_responseWithoutFetchSkipped(t *testing.T) {
	// First Response has no Fetch (must skip), second has the real data.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageItemOperations, "Response", // no Fetch — must skip
					wbxml.E(wbxml.PageItemOperations, "Move",
						wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
					),
				),
				wbxml.E(wbxml.PageItemOperations, "Response",
					wbxml.E(wbxml.PageItemOperations, "Fetch",
						wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageItemOperations, "Properties",
							wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("Hi")),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	got, err := c.FetchEmail(context.Background(), "inbox", "x", FetchEmailOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Hi" {
		t.Errorf("Subject = %q", got.Subject)
	}
}

func TestFetchEmail_fetchMissingProperties(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageItemOperations, "Response",
					wbxml.E(wbxml.PageItemOperations, "Fetch",
						wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
						// no Properties
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.FetchEmail(context.Background(), "inbox", "x", FetchEmailOptions{})
	if err == nil || !strings.Contains(err.Error(), "<Properties>") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchEmail_responsesWithNoFetchAtAll(t *testing.T) {
	// Server reports Response elements but none contain Fetch — falls
	// through to the "no Fetch response" error.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageItemOperations, "Response",
					wbxml.E(wbxml.PageItemOperations, "Move",
						wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.FetchEmail(context.Background(), "inbox", "x", FetchEmailOptions{})
	if err == nil || !strings.Contains(err.Error(), "no Fetch response") {
		t.Errorf("err = %v", err)
	}
}

func TestFetchEmail_noResponse(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageItemOperations, "ItemOperations",
				wbxml.E(wbxml.PageItemOperations, "Status", wbxml.Text("1")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.FetchEmail(context.Background(), "inbox", "x", FetchEmailOptions{})
	if err == nil || !strings.Contains(err.Error(), "missing <Response>") {
		t.Errorf("err = %v", err)
	}
}
