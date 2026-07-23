// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package wbxml

import (
	"bytes"
	"errors"
	"fmt"
)

// Global token bytes (WBXML 1.3 spec, table 4).
const (
	tokSwitchPage byte = 0x00
	tokEnd        byte = 0x01
	tokStrI       byte = 0x03
	tokOpaque     byte = 0xC3
)

// Tag flag bits for the tag-token byte.
const (
	tagFlagContent byte = 0x40 // child content present (terminated with END)
	tagFlagAttrs   byte = 0x80 // attributes present (EAS never sets this)
)

// Default header values per MS-ASWBXML.
const (
	defaultVersion  byte   = 0x03 // WBXML 1.3
	defaultPublicID uint32 = 0x01 // "unknown" — clients look up via charset
	defaultCharset  uint32 = 0x6A // UTF-8 (IANA MIBenum)
)

// Marshal serializes a Document to the WBXML byte stream the EAS server
// expects. The Registry must define every code page referenced by the tree.
func Marshal(d *Document, r *Registry) ([]byte, error) {
	if d == nil {
		return nil, errors.New("wbxml: Marshal nil document")
	}
	if r == nil {
		return nil, errors.New("wbxml: Marshal nil registry")
	}
	if d.Root == nil {
		return nil, errors.New("wbxml: Marshal document with nil root")
	}

	version := d.Version
	if version == 0 {
		version = defaultVersion
	}
	publicID := d.PublicID
	if publicID == 0 {
		publicID = defaultPublicID
	}
	charset := d.Charset
	if charset == 0 {
		charset = defaultCharset
	}

	var buf bytes.Buffer
	buf.WriteByte(version)
	writeMBUInt32(&buf, publicID)
	writeMBUInt32(&buf, charset)
	writeMBUInt32(&buf, 0) // empty string table

	enc := &encoder{
		buf:         &buf,
		reg:         r,
		currentPage: d.Root.Codepage,
	}
	// First element implicitly establishes the active page; emit a
	// SWITCH_PAGE if it isn't 0 (decoders start with page 0 active).
	if enc.currentPage != 0 {
		buf.WriteByte(tokSwitchPage)
		buf.WriteByte(enc.currentPage)
	}
	if err := enc.writeElement(d.Root); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type encoder struct {
	buf         *bytes.Buffer
	reg         *Registry
	currentPage byte
}

func (e *encoder) writeElement(el *Element) error {
	if el == nil {
		return errors.New("wbxml: nil element")
	}
	if el.Codepage != e.currentPage {
		e.buf.WriteByte(tokSwitchPage)
		e.buf.WriteByte(el.Codepage)
		e.currentPage = el.Codepage
	}
	id, ok := e.reg.TagID(el.Codepage, el.Name)
	if !ok {
		return fmt.Errorf("wbxml: encode: unknown tag %q on page %d (%s)",
			el.Name, el.Codepage, e.reg.PageName(el.Codepage))
	}
	hasContent := len(el.Children) > 0
	tag := id
	if hasContent {
		tag |= tagFlagContent
	}
	e.buf.WriteByte(tag)
	if !hasContent {
		return nil
	}
	for _, child := range el.Children {
		if err := e.writeChild(child); err != nil {
			return err
		}
	}
	e.buf.WriteByte(tokEnd)
	return nil
}

func (e *encoder) writeChild(n Node) error {
	switch c := n.(type) {
	case *Element:
		return e.writeElement(c)
	case Text:
		// STR_I: token, UTF-8 bytes, NUL terminator. EAS strings cannot
		// contain embedded NUL bytes; reject them rather than producing a
		// truncated payload the server will silently misinterpret.
		s := string(c)
		for i := 0; i < len(s); i++ {
			if s[i] == 0 {
				return errors.New("wbxml: encode: text contains NUL byte")
			}
		}
		e.buf.WriteByte(tokStrI)
		e.buf.WriteString(s)
		e.buf.WriteByte(0)
	case Opaque:
		// OPAQUE: token, mb_u_int32 length, raw bytes (no terminator).
		e.buf.WriteByte(tokOpaque)
		writeMBUInt32(e.buf, uint32(len(c)))
		e.buf.Write(c)
	default:
		return fmt.Errorf("wbxml: encode: unsupported node type %T", n)
	}
	return nil
}

// writeMBUInt32 writes a multi-byte unsigned integer per WBXML 1.3 §5.1:
// 7 bits per byte, big-endian, MSB set on every byte except the last.
func writeMBUInt32(w *bytes.Buffer, v uint32) {
	// Find the highest set bit's septet.
	var tmp [5]byte // uint32 fits in 5 septets
	n := 0
	tmp[n] = byte(v & 0x7F)
	v >>= 7
	for v > 0 {
		n++
		tmp[n] = byte(v&0x7F) | 0x80
		v >>= 7
	}
	for i := n; i >= 0; i-- {
		w.WriteByte(tmp[i])
	}
}
