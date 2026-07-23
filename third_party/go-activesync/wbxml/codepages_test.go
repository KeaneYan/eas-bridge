// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package wbxml

import "testing"

func TestRegistry_AddAndLookup(t *testing.T) {
	r := NewRegistry()
	r.Add(&Codepage{
		Number: 99,
		Name:   "Test",
		Tags:   map[byte]string{0x05: "Foo", 0x06: "Bar"},
	})
	if name, ok := r.TagName(99, 0x05); !ok || name != "Foo" {
		t.Errorf("TagName: %q, ok=%v", name, ok)
	}
	if id, ok := r.TagID(99, "Bar"); !ok || id != 0x06 {
		t.Errorf("TagID: 0x%02X, ok=%v", id, ok)
	}
	if _, ok := r.TagName(99, 0xFF); ok {
		t.Error("unknown id should not be found")
	}
	if _, ok := r.TagID(99, "Nope"); ok {
		t.Error("unknown name should not be found")
	}
	if _, ok := r.TagName(100, 0x05); ok {
		t.Error("unknown page should not be found")
	}
	if r.PageName(99) != "Test" {
		t.Errorf("PageName: %q", r.PageName(99))
	}
	if r.PageName(100) != "" {
		t.Errorf("unknown page name: %q", r.PageName(100))
	}
}

func TestRegistry_AddRejectsOutOfRangeTag(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for out-of-range tag identity")
		}
	}()
	r := NewRegistry()
	r.Add(&Codepage{Number: 1, Name: "X", Tags: map[byte]string{0x04: "Bad"}})
}

func TestRegistry_AddRejectsHighIdentity(t *testing.T) {
	// 0x40 collides with the content-flag bit on the tag byte, so it is
	// reserved. Anything in 0x40..0xFF must be rejected by Add.
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for tag identity above 0x3F")
		}
	}()
	r := NewRegistry()
	r.Add(&Codepage{Number: 1, Name: "X", Tags: map[byte]string{0x40: "Bad"}})
}

func TestRegistry_TagID_UnknownPage(t *testing.T) {
	r := NewRegistry()
	if id, ok := r.TagID(99, "Foo"); ok || id != 0 {
		t.Errorf("TagID on unknown page = (0x%02X, %v); want (0, false)", id, ok)
	}
}

func TestRegistry_AddRejectsDuplicatePage(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate page registration")
		}
	}()
	r := NewRegistry()
	r.Add(&Codepage{Number: 1, Name: "A"})
	r.Add(&Codepage{Number: 1, Name: "B"})
}

func TestRegistry_AddRejectsDuplicateName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for duplicate tag name")
		}
	}()
	r := NewRegistry()
	r.Add(&Codepage{
		Number: 1, Name: "X",
		Tags: map[byte]string{0x05: "Foo", 0x06: "Foo"},
	})
}

func TestDefaultRegistry_hasCorePages(t *testing.T) {
	r := DefaultRegistry()
	for _, page := range []byte{
		PageAirSync, PageEmail, PageFolderHierarchy, PageProvision,
		PageAirSyncBase, PageSettings, PageItemOperations, PageEmail2,
	} {
		if r.PageName(page) == "" {
			t.Errorf("DefaultRegistry missing page %d", page)
		}
	}
	// Spot-check a few well-known tags so a transcription error breaks the
	// build before the EAS client lights up against a real server.
	cases := []struct {
		page byte
		id   byte
		name string
	}{
		{PageAirSync, 0x05, "Sync"},
		{PageAirSync, 0x0B, "SyncKey"},
		{PageFolderHierarchy, 0x16, "FolderSync"},
		{PageFolderHierarchy, 0x12, "SyncKey"},
		{PageProvision, 0x05, "Provision"},
		{PageProvision, 0x09, "PolicyKey"},
		{PageAirSyncBase, 0x0A, "Body"},
		{PageAirSyncBase, 0x0B, "Data"},
		{PageEmail, 0x14, "Subject"},
		{PageEmail, 0x18, "From"},
		{PageEmail2, 0x09, "ConversationId"},
		{PageItemOperations, 0x05, "ItemOperations"},
		{PageItemOperations, 0x06, "Fetch"},
		{PageSettings, 0x05, "Settings"},
		{PageSettings, 0x16, "DeviceInformation"},
		{PageSettings, 0x17, "Model"},
	}
	for _, c := range cases {
		if got, _ := r.TagName(c.page, c.id); got != c.name {
			t.Errorf("page %d id 0x%02X = %q, want %q", c.page, c.id, got, c.name)
		}
	}
}

func TestDefaultRegistry_reverseLookups(t *testing.T) {
	r := DefaultRegistry()
	if id, _ := r.TagID(PageAirSync, "Sync"); id != 0x05 {
		t.Errorf("Sync = 0x%02X", id)
	}
	if id, _ := r.TagID(PageFolderHierarchy, "FolderSync"); id != 0x16 {
		t.Errorf("FolderSync = 0x%02X", id)
	}
}
