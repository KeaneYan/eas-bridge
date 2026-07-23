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

func TestSendMail_emptyBodyMeansSuccess(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Empty 200 OK is success per MS-ASCMD §2.2.2.16.
		w.WriteHeader(200)
	})
	mime := []byte("From: a@x\r\nTo: b@x\r\nSubject: hi\r\n\r\nbody")
	if err := c.SendMail(context.Background(), SendMailOptions{MIME: mime}); err != nil {
		t.Fatal(err)
	}
	if cap.url.Query().Get("Cmd") != "SendMail" {
		t.Errorf("Cmd: %q", cap.url.Query().Get("Cmd"))
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	mimeEl := req.Root.Find("Mime")
	if mimeEl == nil {
		t.Fatal("Mime element missing")
	}
	for _, c := range mimeEl.Children {
		if op, ok := c.(wbxml.Opaque); ok {
			if !bytes.Equal([]byte(op), mime) {
				t.Errorf("MIME bytes don't round-trip")
			}
			return
		}
	}
	t.Error("MIME element has no Opaque child")
}

func TestSendMail_saveInSentDefault(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	_ = c.SendMail(context.Background(), SendMailOptions{MIME: []byte("x")})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if req.Root.Find("SaveInSentItems") == nil {
		t.Error("SaveInSentItems should be present by default")
	}
}

func TestSendMail_skipSaveInSent(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	_ = c.SendMail(context.Background(), SendMailOptions{MIME: []byte("x"), SkipSaveInSent: true})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if req.Root.Find("SaveInSentItems") != nil {
		t.Error("SaveInSentItems should NOT be present when SkipSaveInSent")
	}
}

func TestSendMail_emptyMIMERejected(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	if err := c.SendMail(context.Background(), SendMailOptions{}); err == nil {
		t.Error("want error for empty MIME")
	}
}

func TestSendMail_clientIDProvidedOrGenerated(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	_ = c.SendMail(context.Background(), SendMailOptions{MIME: []byte("x"), ClientID: "MYID"})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if id := req.Root.Find("ClientId"); id == nil || id.TextContent() != "MYID" {
		t.Errorf("ClientId: %v", id)
	}

	c2, cap2, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	_ = c2.SendMail(context.Background(), SendMailOptions{MIME: []byte("x")})
	req2, _ := wbxml.Unmarshal(cap2.body, wbxml.DefaultRegistry())
	if id := req2.Root.Find("ClientId"); id == nil || len(id.TextContent()) != 32 {
		t.Errorf("auto ClientId len: %v", id)
	}
}

func TestSmartReply_emitsSourceElement(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	err := c.SmartReply(context.Background(), ReplyForwardOptions{
		SendMailOptions: SendMailOptions{MIME: []byte("reply body")},
		FolderID:        "inbox",
		ServerID:        "inbox:42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.url.Query().Get("Cmd") != "SmartReply" {
		t.Errorf("Cmd: %q", cap.url.Query().Get("Cmd"))
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	src := req.Root.Find("Source")
	if src == nil {
		t.Fatal("Source missing")
	}
	if f := src.Find("FolderId"); f == nil || f.TextContent() != "inbox" {
		t.Errorf("FolderId = %v", f)
	}
	if id := src.Find("ItemId"); id == nil || id.TextContent() != "inbox:42" {
		t.Errorf("ItemId = %v", id)
	}
}

func TestSmartForward_validatesArgs(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	err := c.SmartForward(context.Background(), ReplyForwardOptions{
		SendMailOptions: SendMailOptions{MIME: []byte("x")},
	})
	if err == nil || !strings.Contains(err.Error(), "FolderID and ServerID") {
		t.Errorf("err = %v", err)
	}
}

func TestSmartReply_emptyMIMERejected(t *testing.T) {
	c, _, _ := newTestClient(t, nil)
	err := c.SmartReply(context.Background(), ReplyForwardOptions{
		FolderID: "inbox",
		ServerID: "inbox:1",
	})
	if err == nil || !strings.Contains(err.Error(), "MIME") {
		t.Errorf("err = %v", err)
	}
}

func TestSendMail_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := c.SendMail(context.Background(), SendMailOptions{MIME: []byte("x")}); err == nil {
		t.Error("want error from transport")
	}
}

func TestSendMail_status1IsSuccess(t *testing.T) {
	// Server returns a body with Status=1; sendMailLike must treat that as
	// success even though the response is non-empty.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageComposeMail, "SendMail",
				wbxml.E(wbxml.PageComposeMail, "Status", wbxml.Text("1")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.SendMail(context.Background(), SendMailOptions{MIME: []byte("x")}); err != nil {
		t.Errorf("Status=1 should be success, got %v", err)
	}
}

func TestSendMail_statusErrorPropagated(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// EAS reports SendMail errors via WBXML status.
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageComposeMail, "SendMail",
				wbxml.E(wbxml.PageComposeMail, "Status", wbxml.Text("110")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	err := c.SendMail(context.Background(), SendMailOptions{MIME: []byte("x")})
	if !IsStatusCode(err, 110) {
		t.Errorf("err = %v", err)
	}
}
