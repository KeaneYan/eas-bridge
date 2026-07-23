// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestGzip_responseDecompressed(t *testing.T) {
	resp := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
			wbxml.E(wbxml.PageFolderHierarchy, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("ZK1")),
		),
	}
	respBytes, err := wbxml.Marshal(resp, wbxml.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}

	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		gz.Write(respBytes)
		gz.Close()
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(buf.Bytes())
	})

	req := &wbxml.Document{Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync")}
	doc, err := c.post(context.Background(), "FolderSync", req)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Root.Find("SyncKey").TextContent() != "ZK1" {
		t.Errorf("decoded body wrong: %v", doc)
	}
}

func TestGzip_requestCompressedAboveThreshold(t *testing.T) {
	var seenEncoding string
	var seenBody []byte
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seenEncoding = r.Header.Get("Content-Encoding")
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	})
	c.cfg.GzipRequests = true
	c.cfg.GzipMinBytes = 8 // tiny so our small request triggers compression

	body := strings.Repeat("AAAAAAAAAA", 16) // 160 bytes well over 8
	if _, err := c.postRaw(context.Background(), "X", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if seenEncoding != "gzip" {
		t.Errorf("Content-Encoding = %q", seenEncoding)
	}
	gz, err := gzip.NewReader(bytes.NewReader(seenBody))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	got, _ := io.ReadAll(gz)
	if string(got) != body {
		t.Errorf("decompressed body mismatch")
	}
}

func TestGzip_requestNotCompressedUnderThreshold(t *testing.T) {
	var seenEncoding string
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seenEncoding = r.Header.Get("Content-Encoding")
		w.WriteHeader(200)
	})
	c.cfg.GzipRequests = true
	c.cfg.GzipMinBytes = 1024
	if _, err := c.postRaw(context.Background(), "X", []byte("short")); err != nil {
		t.Fatal(err)
	}
	if seenEncoding == "gzip" {
		t.Error("body below threshold should not be gzipped")
	}
}

func TestGzip_acceptEncodingAlwaysSet(t *testing.T) {
	var seen string
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Accept-Encoding")
		w.WriteHeader(200)
	})
	if _, err := c.postRaw(context.Background(), "X", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "gzip") {
		t.Errorf("Accept-Encoding = %q", seen)
	}
}
