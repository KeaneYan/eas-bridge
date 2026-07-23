// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package wbxml

import (
	"errors"
	"fmt"
	"io"
)

// Unmarshal parses a WBXML byte stream into a Document. The Registry must
// define every code page referenced by the stream — unknown tag tokens are
// reported with byte offsets so fixtures can be debugged against the wire.
func Unmarshal(b []byte, r *Registry) (*Document, error) {
	if r == nil {
		return nil, errors.New("wbxml: Unmarshal nil registry")
	}
	d := &decoder{src: b, reg: r}

	version, err := d.readByte()
	if err != nil {
		return nil, fmt.Errorf("wbxml: header: version: %w", err)
	}
	publicID, err := d.readMBUInt32()
	if err != nil {
		return nil, fmt.Errorf("wbxml: header: publicid: %w", err)
	}
	charset, err := d.readMBUInt32()
	if err != nil {
		return nil, fmt.Errorf("wbxml: header: charset: %w", err)
	}
	strTabLen, err := d.readMBUInt32()
	if err != nil {
		return nil, fmt.Errorf("wbxml: header: strtab len: %w", err)
	}
	if strTabLen > 0 {
		// EAS doesn't use the string table; consume and ignore so we don't
		// fail outright on a server that emits one.
		if _, err := d.readN(int(strTabLen)); err != nil {
			return nil, fmt.Errorf("wbxml: header: strtab body: %w", err)
		}
	}

	doc := &Document{Version: version, PublicID: publicID, Charset: charset}

	// Drain leading SWITCH_PAGEs (allowed before the root) so the root's
	// codepage is set correctly without recording phantom switches.
	for d.peekByte() == tokSwitchPage {
		_, _ = d.readByte()
		page, err := d.readByte()
		if err != nil {
			return nil, fmt.Errorf("wbxml: switch_page: %w", err)
		}
		d.currentPage = page
	}

	root, err := d.readElement()
	if err != nil {
		return nil, err
	}
	doc.Root = root
	return doc, nil
}

type decoder struct {
	src         []byte
	pos         int
	reg         *Registry
	currentPage byte
}

func (d *decoder) readByte() (byte, error) {
	if d.pos >= len(d.src) {
		return 0, io.ErrUnexpectedEOF
	}
	b := d.src[d.pos]
	d.pos++
	return b, nil
}

// peekByte returns the next byte without consuming, or 0xFF if at EOF
// (0xFF is not a valid leading token byte, so the caller can use it as a
// "not present" sentinel without ambiguity).
func (d *decoder) peekByte() byte {
	if d.pos >= len(d.src) {
		return 0xFF
	}
	return d.src[d.pos]
}

func (d *decoder) readN(n int) ([]byte, error) {
	if d.pos+n > len(d.src) {
		return nil, io.ErrUnexpectedEOF
	}
	out := d.src[d.pos : d.pos+n]
	d.pos += n
	return out, nil
}

// readMBUInt32 reads a multi-byte unsigned int (7 bits/byte, MSB =
// continuation). Caps at 5 bytes to avoid pathological inputs.
func (d *decoder) readMBUInt32() (uint32, error) {
	var v uint32
	for range 5 {
		b, err := d.readByte()
		if err != nil {
			return 0, err
		}
		v = (v << 7) | uint32(b&0x7F)
		if b&0x80 == 0 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("wbxml: mb_u_int32 longer than 5 bytes at offset %d", d.pos)
}

// readElement reads one element (tag byte + optional content + END if
// content was present). It assumes any leading SWITCH_PAGE has been
// consumed by the caller.
func (d *decoder) readElement() (*Element, error) {
	tagByte, err := d.readByte()
	if err != nil {
		return nil, fmt.Errorf("wbxml: element: %w", err)
	}
	if tagByte == tokEnd {
		return nil, fmt.Errorf("wbxml: unexpected END at offset %d", d.pos-1)
	}
	if tagByte&tagFlagAttrs != 0 {
		return nil, fmt.Errorf("wbxml: element with attributes (byte 0x%02X) at offset %d: not supported (EAS does not use attributes)",
			tagByte, d.pos-1)
	}
	hasContent := tagByte&tagFlagContent != 0
	id := tagByte &^ (tagFlagContent | tagFlagAttrs)
	page := d.currentPage
	name, ok := d.reg.TagName(page, id)
	if !ok {
		return nil, fmt.Errorf("wbxml: unknown tag 0x%02X on page %d (%s) at offset %d",
			id, page, d.reg.PageName(page), d.pos-1)
	}
	el := &Element{Codepage: page, Name: name}
	if !hasContent {
		return el, nil
	}
	for {
		// Check for SWITCH_PAGE between siblings.
		next, err := d.readByte()
		if err != nil {
			return nil, fmt.Errorf("wbxml: element %q: %w", name, err)
		}
		switch next {
		case tokEnd:
			return el, nil
		case tokSwitchPage:
			page, err := d.readByte()
			if err != nil {
				return nil, fmt.Errorf("wbxml: element %q: switch_page: %w", el.Name, err)
			}
			d.currentPage = page
			continue
		case tokStrI:
			s, err := d.readCStr()
			if err != nil {
				return nil, fmt.Errorf("wbxml: element %q: str_i: %w", name, err)
			}
			el.Children = append(el.Children, Text(s))
		case tokOpaque:
			n, err := d.readMBUInt32()
			if err != nil {
				return nil, fmt.Errorf("wbxml: element %q: opaque length: %w", name, err)
			}
			body, err := d.readN(int(n))
			if err != nil {
				return nil, fmt.Errorf("wbxml: element %q: opaque body: %w", name, err)
			}
			// Copy out: readN returns a slice into d.src.
			cp := make([]byte, len(body))
			copy(cp, body)
			el.Children = append(el.Children, Opaque(cp))
		default:
			// It's a tag byte for a child element. Push it back and recurse.
			d.pos--
			child, err := d.readElement()
			if err != nil {
				return nil, fmt.Errorf("wbxml: element %q: %w", name, err)
			}
			el.Children = append(el.Children, child)
		}
	}
}

// readCStr reads a NUL-terminated UTF-8 string (the body of a STR_I token).
func (d *decoder) readCStr() (string, error) {
	start := d.pos
	for d.pos < len(d.src) {
		if d.src[d.pos] == 0 {
			s := string(d.src[start:d.pos])
			d.pos++ // consume NUL
			return s, nil
		}
		d.pos++
	}
	return "", io.ErrUnexpectedEOF
}
