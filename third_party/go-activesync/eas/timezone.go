// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

// EAS uses Microsoft's TIME_ZONE_INFORMATION struct, base64-encoded as
// the Calendar TimeZone element's text content. Layout (172 bytes):
//
//	int32  Bias               -- minutes east of UTC, sign-flipped
//	wchar[32] StandardName    -- UTF-16-LE, NUL-padded (64 bytes)
//	SYSTEMTIME StandardDate   -- 16 bytes, year=0 means recurring rule
//	int32  StandardBias       -- usually 0
//	wchar[32] DaylightName    -- UTF-16-LE, NUL-padded (64 bytes)
//	SYSTEMTIME DaylightDate   -- 16 bytes
//	int32  DaylightBias       -- usually -60 (one hour DST)
//
// SYSTEMTIME is 8 little-endian uint16s:
//
//	wYear, wMonth, wDayOfWeek, wDay, wHour, wMinute, wSecond, wMilliseconds
//
// For year-agnostic DST rules, wYear=0 and wDay is "the Nth occurrence
// of wDayOfWeek in wMonth" (1-5; 5 = "last").

// EASTimeZone is a parsed/constructed TIME_ZONE_INFORMATION blob.
//
// Most callers will use EncodeTimeZone(loc) for the common case
// (UTC or a Go *time.Location with simple DST rules). For full Outlook
// compatibility (named zone preserved across round-trip) construct
// the struct directly.
type EASTimeZone struct {
	BiasMinutes  int32  // signed; the value Microsoft stores (UTC offset in negated minutes)
	StandardName string // up to 32 UTF-16 code units
	StandardDate SystemTime
	StandardBias int32
	DaylightName string
	DaylightDate SystemTime
	DaylightBias int32
}

// SystemTime is the Windows SYSTEMTIME struct.
type SystemTime struct {
	Year         uint16
	Month        uint16
	DayOfWeek    uint16
	Day          uint16
	Hour         uint16
	Minute       uint16
	Second       uint16
	Milliseconds uint16
}

// EncodeTimeZone returns the base64-encoded text content for a Calendar
// TimeZone element representing loc. UTC produces an all-zero blob.
//
// For non-UTC locations the encoding sets only the Bias field; DST
// rules are deliberately left empty because Go's *time.Location has no
// public API to extract them. Servers that need the full DST
// information should construct an EASTimeZone manually.
func EncodeTimeZone(loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	tz := EASTimeZone{}
	if loc != time.UTC {
		// Probe the offset for "now". This is a best-effort
		// approximation: stable in standard time but won't shift across
		// the DST boundary in the encoded blob.
		_, offsetSeconds := time.Now().In(loc).Zone()
		tz.BiasMinutes = int32(-offsetSeconds / 60)
		tz.StandardName = loc.String()
	}
	return tz.Encode()
}

// Encode returns the base64-encoded text content for this EASTimeZone.
func (t EASTimeZone) Encode() string {
	var buf [172]byte
	binary.LittleEndian.PutUint32(buf[0:4], uint32(t.BiasMinutes))
	writeWCharFixed(buf[4:68], t.StandardName)
	writeSystemTime(buf[68:84], t.StandardDate)
	binary.LittleEndian.PutUint32(buf[84:88], uint32(t.StandardBias))
	writeWCharFixed(buf[88:152], t.DaylightName)
	writeSystemTime(buf[152:168], t.DaylightDate)
	binary.LittleEndian.PutUint32(buf[168:172], uint32(t.DaylightBias))
	return base64.StdEncoding.EncodeToString(buf[:])
}

// DecodeTimeZone parses a base64-encoded TIME_ZONE_INFORMATION blob.
func DecodeTimeZone(s string) (EASTimeZone, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return EASTimeZone{}, err
	}
	if len(raw) != 172 {
		return EASTimeZone{}, errors.New("eas: timezone blob must be 172 bytes")
	}
	var t EASTimeZone
	t.BiasMinutes = int32(binary.LittleEndian.Uint32(raw[0:4]))
	t.StandardName = readWCharFixed(raw[4:68])
	t.StandardDate = readSystemTime(raw[68:84])
	t.StandardBias = int32(binary.LittleEndian.Uint32(raw[84:88]))
	t.DaylightName = readWCharFixed(raw[88:152])
	t.DaylightDate = readSystemTime(raw[152:168])
	t.DaylightBias = int32(binary.LittleEndian.Uint32(raw[168:172]))
	return t, nil
}

func writeWCharFixed(dst []byte, s string) {
	// 32 UTF-16 code units = 64 bytes. Truncate at 31 chars to leave
	// room for an implicit NUL terminator (Windows quirks).
	out := make([]uint16, 32)
	enc := utf16Encode(s)
	copy(out, enc)
	for i, w := range out {
		binary.LittleEndian.PutUint16(dst[i*2:], w)
	}
}

func readWCharFixed(src []byte) string {
	codes := make([]uint16, len(src)/2)
	for i := range codes {
		codes[i] = binary.LittleEndian.Uint16(src[i*2:])
	}
	// Trim trailing NULs.
	for len(codes) > 0 && codes[len(codes)-1] == 0 {
		codes = codes[:len(codes)-1]
	}
	return utf16Decode(codes)
}

func writeSystemTime(dst []byte, st SystemTime) {
	binary.LittleEndian.PutUint16(dst[0:2], st.Year)
	binary.LittleEndian.PutUint16(dst[2:4], st.Month)
	binary.LittleEndian.PutUint16(dst[4:6], st.DayOfWeek)
	binary.LittleEndian.PutUint16(dst[6:8], st.Day)
	binary.LittleEndian.PutUint16(dst[8:10], st.Hour)
	binary.LittleEndian.PutUint16(dst[10:12], st.Minute)
	binary.LittleEndian.PutUint16(dst[12:14], st.Second)
	binary.LittleEndian.PutUint16(dst[14:16], st.Milliseconds)
}

func readSystemTime(src []byte) SystemTime {
	return SystemTime{
		Year:         binary.LittleEndian.Uint16(src[0:2]),
		Month:        binary.LittleEndian.Uint16(src[2:4]),
		DayOfWeek:    binary.LittleEndian.Uint16(src[4:6]),
		Day:          binary.LittleEndian.Uint16(src[6:8]),
		Hour:         binary.LittleEndian.Uint16(src[8:10]),
		Minute:       binary.LittleEndian.Uint16(src[10:12]),
		Second:       binary.LittleEndian.Uint16(src[12:14]),
		Milliseconds: binary.LittleEndian.Uint16(src[14:16]),
	}
}

// utf16Encode encodes s as UTF-16 code units (LE-friendly; the
// conversion to bytes happens at the caller).
func utf16Encode(s string) []uint16 {
	out := make([]uint16, 0, len(s))
	for _, r := range s {
		if r < 0x10000 {
			out = append(out, uint16(r))
		} else {
			r -= 0x10000
			out = append(out, 0xD800+uint16(r>>10), 0xDC00+uint16(r&0x3FF))
		}
	}
	return out
}

// utf16Decode is the inverse of utf16Encode.
func utf16Decode(in []uint16) string {
	var out []rune
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(in) {
			c2 := in[i+1]
			if c2 >= 0xDC00 && c2 <= 0xDFFF {
				r := rune(c-0xD800)<<10 | rune(c2-0xDC00) + 0x10000
				out = append(out, r)
				i++
				continue
			}
		}
		out = append(out, rune(c))
	}
	return string(out)
}
