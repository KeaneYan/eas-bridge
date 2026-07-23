// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestQuery_FreeText_encode(t *testing.T) {
	got := FreeText("hello world").encode()
	if got.Name != "FreeText" || got.TextContent() != "hello world" {
		t.Errorf("encode = %+v", got)
	}
}

func TestQuery_AndOr_encode(t *testing.T) {
	tree := And(
		Or(FreeText("a"), FreeText("b")),
		EqualTo(PropEmailFrom, "alice@x"),
	)
	got := tree.encode()
	if got.Name != "And" || len(got.Children) != 2 {
		t.Fatalf("And = %+v", got)
	}
	or, ok := got.Children[0].(*wbxml.Element)
	if !ok || or.Name != "Or" || len(or.Children) != 2 {
		t.Errorf("Or = %+v", or)
	}
}

func TestQuery_LessThan_encode(t *testing.T) {
	got := LessThan(PropEmailDateReceived, "2026-01-01T00:00:00Z").encode()
	if got.Name != "LessThan" {
		t.Errorf("op = %q", got.Name)
	}
	val := got.Find("Value")
	if val == nil || val.TextContent() != "2026-01-01T00:00:00Z" {
		t.Errorf("Value = %v", val)
	}
}

func TestQuery_CollectionID_encode(t *testing.T) {
	got := CollectionID("inbox").encode()
	if got.Name != "EqualTo" {
		t.Fatalf("op = %q", got.Name)
	}
	cid := got.Find("CollectionId")
	if cid == nil {
		t.Fatal("expected CollectionId child")
	}
	if val := got.Find("Value"); val == nil || val.TextContent() != "inbox" {
		t.Errorf("Value = %v", val)
	}
}

func TestQuery_EmailClass_encode(t *testing.T) {
	got := EmailClass().encode()
	if got.Name != "EqualTo" {
		t.Fatalf("op = %q", got.Name)
	}
	if val := got.Find("Value"); val == nil || val.TextContent() != "Email" {
		t.Errorf("Value = %v", val)
	}
}

func TestQuery_GreaterThan_encode(t *testing.T) {
	got := GreaterThan(PropEmailDateReceived, "2026-01-01T00:00:00Z").encode()
	if got.Name != "GreaterThan" {
		t.Errorf("op = %q", got.Name)
	}
}
