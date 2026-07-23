// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package wbxml_test

import (
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// Marshal turns a Document tree into the raw WBXML byte stream an EAS
// server expects. Build the tree with the wbxml.E helper.
//
// The 13-byte output decodes as:
//
//	03 01 6a 00  header: WBXML 1.3, public id 1, charset 0x6A (UTF-8), empty string table
//	00 07        SWITCH_PAGE to FolderHierarchy (page 7)
//	56           FolderSync (id 0x16) | content flag (0x40)
//	52           SyncKey (id 0x12) | content flag (0x40)
//	03 30 00     STR_I "0" terminated by NUL
//	01 01        END SyncKey, END FolderSync
func ExampleMarshal() {
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("0")),
		),
	}
	out, err := wbxml.Marshal(doc, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	fmt.Printf("% x\n", out)
	// Output: 03 01 6a 00 00 07 56 52 03 30 00 01 01
}

// Unmarshal parses a WBXML byte stream back into a Document tree. Use
// Find / FindAll / TextContent to walk the result.
func ExampleUnmarshal() {
	// A minimal FolderSync response: <FolderSync><Status>1</Status>
	//                                <SyncKey>42</SyncKey></FolderSync>
	raw := []byte{
		0x03, 0x01, 0x6a, 0x00, // header
		0x00, 0x07, // SWITCH_PAGE FolderHierarchy
		0x56,                        // FolderSync (content)
		0x4c, 0x03, '1', 0x00, 0x01, // Status (content) STR_I "1" END
		0x52, 0x03, '4', '2', 0x00, 0x01, // SyncKey (content) STR_I "42" END
		0x01, // END FolderSync
	}
	doc, err := wbxml.Unmarshal(raw, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	fmt.Println("status:", doc.Root.Find("Status").TextContent())
	fmt.Println("synckey:", doc.Root.Find("SyncKey").TextContent())
	// Output:
	// status: 1
	// synckey: 42
}

// Find walks descendants depth-first by tag name. Use FindAll for every
// match in document order, or iterate Element.Children directly when the
// same name is ambiguous across code pages.
func ExampleElement_Find() {
	root := wbxml.E(wbxml.PageAirSync, "Sync",
		wbxml.E(wbxml.PageAirSync, "Collections",
			wbxml.E(wbxml.PageAirSync, "Collection",
				wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("abc")),
				wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("inbox")),
			),
		),
	)
	fmt.Println(root.Find("CollectionId").TextContent())
	// Output: inbox
}

// NewRegistry plus Codepage lets callers add namespaces beyond the
// 25 EAS code pages DefaultRegistry ships with — e.g. for a vendor
// extension or a non-EAS WBXML profile.
func ExampleNewRegistry() {
	r := wbxml.NewRegistry()
	r.Add(&wbxml.Codepage{
		Number: 99,
		Name:   "MyApp",
		Tags: map[byte]string{
			0x05: "Hello",
			0x06: "World",
		},
	})

	name, _ := r.TagName(99, 0x05)
	id, _ := r.TagID(99, "World")
	fmt.Printf("0x05 → %s\n", name)
	fmt.Printf("World → 0x%02X\n", id)
	// Output:
	// 0x05 → Hello
	// World → 0x06
}
