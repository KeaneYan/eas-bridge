// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestEncodeTimeZone_nilDefaultsToUTC(t *testing.T) {
	// Nil location is treated as UTC: all-zero blob.
	got := EncodeTimeZone(nil)
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 172 {
		t.Fatalf("len = %d, want 172", len(raw))
	}
	for i, b := range raw {
		if b != 0 {
			t.Errorf("raw[%d] = 0x%02X, want 0 (nil should encode like UTC)", i, b)
			break
		}
	}
}

func TestEncodeTimeZone_namedZoneSetsBias(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("zoneinfo unavailable")
	}
	got := EncodeTimeZone(loc)
	tz, err := DecodeTimeZone(got)
	if err != nil {
		t.Fatal(err)
	}
	// New York is 5h or 4h behind UTC; bias is the negated offset in minutes.
	if tz.BiasMinutes != 240 && tz.BiasMinutes != 300 {
		t.Errorf("BiasMinutes = %d, want 240 (EDT) or 300 (EST)", tz.BiasMinutes)
	}
	if tz.StandardName == "" {
		t.Error("StandardName should be populated for named zone")
	}
}

func TestDecodeTimeZone_rejectsWrongLength(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 100))
	_, err := DecodeTimeZone(short)
	if err == nil || !strings.Contains(err.Error(), "172 bytes") {
		t.Errorf("err = %v", err)
	}
}

func TestDecodeTimeZone_rejectsBadBase64(t *testing.T) {
	if _, err := DecodeTimeZone("not!valid!base64"); err == nil {
		t.Error("want error for invalid base64")
	}
}

func TestEncodeTimeZone_roundTrip(t *testing.T) {
	src := EASTimeZone{
		BiasMinutes:  300,
		StandardName: "Pacific Standard Time",
		StandardDate: SystemTime{Month: 11, DayOfWeek: 0, Day: 1, Hour: 2},
		StandardBias: 0,
		DaylightName: "Pacific Daylight Time",
		DaylightDate: SystemTime{Month: 3, DayOfWeek: 0, Day: 2, Hour: 2},
		DaylightBias: -60,
	}
	encoded := src.Encode()
	got, err := DecodeTimeZone(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != src {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, src)
	}
}

func TestUTF16RoundTrip_supplementaryPlane(t *testing.T) {
	// U+1F600 (😀) requires a surrogate pair; verify utf16Encode/utf16Decode
	// preserve it.
	in := "abc 😀 é"
	enc := utf16Encode(in)
	out := utf16Decode(enc)
	if out != in {
		t.Errorf("round trip: %q -> %q", in, out)
	}
}
