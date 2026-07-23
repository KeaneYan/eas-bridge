// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

// Package wbxml encodes and decodes the WAP Binary XML (WBXML 1.3) format
// as profiled by Microsoft Exchange ActiveSync (MS-ASWBXML).
//
// EAS uses WBXML for the body of every command except Ping and OPTIONS.
// Each EAS namespace lives in a numbered "code page"; tag names are encoded
// as one-byte tokens whose meaning depends on the active code page. The
// SWITCH_PAGE control token changes the active page mid-stream.
//
// This package handles encoding (Marshal) and decoding (Unmarshal) of
// documents and exposes a Registry of named code pages so that callers can
// add or replace pages as the EAS spec evolves. EAS does not use WBXML
// attributes, processing instructions, the LITERAL family, or string-table
// references, so the codec deliberately omits support for them — they would
// be dead code.
//
// # More
//
// See README.md in this directory for the wire-format diagrams, the
// full code-page table, and tree-walking examples. Or browse the full
// godoc.
package wbxml

import "strings"

// Document is a complete WBXML document: header fields plus a single root
// element. Only Root is significant when constructing a request; the header
// fields default to their EAS-standard values during Marshal.
type Document struct {
	// Version is the WBXML version byte (defaults to 0x03 = WBXML 1.3).
	Version byte
	// PublicID identifies the document type. EAS always uses 0x01 ("unknown,
	// look up via charset"). Any nonzero value is preserved on round-trip.
	PublicID uint32
	// Charset identifies the character encoding (defaults to 0x6A = UTF-8,
	// which is what EAS mandates).
	Charset uint32
	// Root is the document's single root element.
	Root *Element
}

// Node is anything that may appear as a child of an Element: another
// Element, Text content, or Opaque (raw byte) content.
//
// EAS rarely mixes these inside the same parent — most elements have either
// child elements, a single Text child, or a single Opaque child — but the
// encoding allows them to be freely interleaved and the decoder preserves
// whatever it sees.
type Node interface {
	isNode()
}

// Element is a tagged node identified by (Codepage, Name).
type Element struct {
	// Codepage is the EAS code page the tag lives in (0 = AirSync, 7 =
	// FolderHierarchy, etc.). The encoder emits SWITCH_PAGE tokens when this
	// changes between sibling elements; the decoder records the active page
	// at decode time.
	Codepage byte
	// Name is the tag name (e.g. "Sync", "FolderSync").
	Name string
	// Children is the ordered list of child nodes.
	Children []Node
}

func (*Element) isNode() {}

// Text is inline UTF-8 string content (encoded as STR_I in WBXML).
type Text string

func (Text) isNode() {}

// Opaque is raw byte content (encoded as the OPAQUE token in WBXML). EAS
// uses opaque content for MIME bodies in AirSyncBase elements.
type Opaque []byte

func (Opaque) isNode() {}

// E constructs a new Element with the given page, name, and children.
// Convenience for building request documents in tests and command builders:
//
//	root := wbxml.E(wbxml.PageFolderHierarchy, "FolderSync",
//	    wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text("0")),
//	)
func E(page byte, name string, children ...Node) *Element {
	return &Element{Codepage: page, Name: name, Children: children}
}

// TextContent returns the concatenation of all top-level Text children.
// Most EAS scalar elements (status codes, sync keys, IDs) use a single Text
// child; this helper avoids open-coding the type assertion at every call
// site.
func (e *Element) TextContent() string {
	var sb strings.Builder
	for _, c := range e.Children {
		if t, ok := c.(Text); ok {
			sb.WriteString(string(t))
		}
	}
	return sb.String()
}

// Find returns the first descendant element with the given name, or nil.
// Search is depth-first, pre-order. Codepage is not matched (EAS tag names
// are unique within the spec given the relevant page context).
func (e *Element) Find(name string) *Element {
	if e == nil {
		return nil
	}
	if e.Name == name {
		return e
	}
	for _, c := range e.Children {
		if child, ok := c.(*Element); ok {
			if found := child.Find(name); found != nil {
				return found
			}
		}
	}
	return nil
}

// FindAll returns all descendant elements with the given name in
// document order (pre-order traversal).
func (e *Element) FindAll(name string) []*Element {
	if e == nil {
		return nil
	}
	var out []*Element
	if e.Name == name {
		out = append(out, e)
	}
	for _, c := range e.Children {
		if child, ok := c.(*Element); ok {
			out = append(out, child.FindAll(name)...)
		}
	}
	return out
}
