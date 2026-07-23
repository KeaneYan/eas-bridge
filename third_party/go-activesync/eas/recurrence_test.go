// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

func TestRecurrence_roundTrip(t *testing.T) {
	r := &Recurrence{
		Type:        RecurrenceWeekly,
		Interval:    1,
		DayOfWeek:   DowMonday | DowWednesday | DowFriday,
		Until:       time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
		Occurrences: 0,
	}
	el := recurrenceToWBXML(r)
	if el == nil {
		t.Fatal("encoded recurrence is nil")
	}
	got := recurrenceFromWBXML(el)
	if got.Type != r.Type {
		t.Errorf("Type: %v vs %v", got.Type, r.Type)
	}
	if got.Interval != r.Interval {
		t.Errorf("Interval: %d vs %d", got.Interval, r.Interval)
	}
	if got.DayOfWeek != r.DayOfWeek {
		t.Errorf("DayOfWeek: %d vs %d", got.DayOfWeek, r.DayOfWeek)
	}
	if !got.Until.Equal(r.Until) {
		t.Errorf("Until: %v vs %v", got.Until, r.Until)
	}
}

func TestException_deletedRoundTrip(t *testing.T) {
	x := Exception{
		ExceptionStartTime: time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC),
		Deleted:            true,
	}
	el := exceptionToWBXML(x)
	got := parseException(el)
	if !got.Deleted || !got.ExceptionStartTime.Equal(x.ExceptionStartTime) {
		t.Errorf("got %+v", got)
	}
}

func TestException_modifiedRoundTrip(t *testing.T) {
	x := Exception{
		ExceptionStartTime: time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC),
		Subject:            "Moved!",
		StartTime:          time.Date(2026, 5, 15, 11, 0, 0, 0, time.UTC),
		EndTime:            time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
		BusyStatus:         2,
	}
	got := parseException(exceptionToWBXML(x))
	if got.Subject != "Moved!" || got.BusyStatus != 2 {
		t.Errorf("got %+v", got)
	}
	if !got.StartTime.Equal(x.StartTime) || !got.EndTime.Equal(x.EndTime) {
		t.Errorf("times: %v vs %v / %v vs %v", got.StartTime, x.StartTime, got.EndTime, x.EndTime)
	}
}

func TestTimeZone_utcRoundTrip(t *testing.T) {
	enc := EncodeTimeZone(time.UTC)
	tz, err := DecodeTimeZone(enc)
	if err != nil {
		t.Fatal(err)
	}
	if tz.BiasMinutes != 0 || tz.StandardName != "" {
		t.Errorf("UTC tz = %+v", tz)
	}
}

func TestTimeZone_namedZoneRoundTrip(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("zoneinfo unavailable: %v", err)
	}
	enc := EncodeTimeZone(loc)
	tz, err := DecodeTimeZone(enc)
	if err != nil {
		t.Fatal(err)
	}
	if tz.StandardName != loc.String() {
		t.Errorf("name = %q, want %q", tz.StandardName, loc.String())
	}
	// Bias should be ±420 (PDT/PST). Don't pin which without checking
	// "now" in that zone — Microsoft sign convention is negated minutes.
	if tz.BiasMinutes != 420 && tz.BiasMinutes != 480 {
		t.Errorf("BiasMinutes = %d, want 420 or 480", tz.BiasMinutes)
	}
}

func TestTimeZone_explicitBlobRoundTrip(t *testing.T) {
	in := EASTimeZone{
		BiasMinutes:  300, // -5h offset (EST)
		StandardName: "Eastern Standard Time",
		StandardDate: SystemTime{Year: 0, Month: 11, DayOfWeek: 0, Day: 1, Hour: 2},
		DaylightName: "Eastern Daylight Time",
		DaylightDate: SystemTime{Year: 0, Month: 3, DayOfWeek: 0, Day: 2, Hour: 2},
		DaylightBias: -60,
	}
	out, err := DecodeTimeZone(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.BiasMinutes != in.BiasMinutes {
		t.Errorf("bias: %d vs %d", out.BiasMinutes, in.BiasMinutes)
	}
	if out.StandardName != in.StandardName {
		t.Errorf("std name: %q vs %q", out.StandardName, in.StandardName)
	}
	if out.DaylightName != in.DaylightName {
		t.Errorf("dst name: %q vs %q", out.DaylightName, in.DaylightName)
	}
	if out.DaylightBias != in.DaylightBias {
		t.Errorf("dst bias: %d vs %d", out.DaylightBias, in.DaylightBias)
	}
	if out.StandardDate != in.StandardDate || out.DaylightDate != in.DaylightDate {
		t.Errorf("system time round-trip mismatch")
	}
}

// TestRecurrenceFromWBXML_allFields walks every branch of
// recurrenceFromWBXML so calendar-type and locale-specific fields
// (MonthOfYear, FirstDayOfWeek, IsLeapMonth) don't silently regress.
func TestRecurrenceFromWBXML_allFields(t *testing.T) {
	if got := recurrenceFromWBXML(nil); got != nil {
		t.Errorf("nil input: %v", got)
	}
	in := wbxml.E(wbxml.PageCalendar, "Recurrence",
		wbxml.E(wbxml.PageCalendar, "Recurrence_Type", wbxml.Text("1")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_Interval", wbxml.Text("2")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_Occurrences", wbxml.Text("10")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_Until", wbxml.Text("2026-12-31T00:00:00Z")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_DayOfWeek", wbxml.Text("4")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_DayOfMonth", wbxml.Text("15")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_WeekOfMonth", wbxml.Text("3")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_MonthOfYear", wbxml.Text("6")),
		wbxml.E(wbxml.PageCalendar, "CalendarType", wbxml.Text("1")),
		wbxml.E(wbxml.PageCalendar, "FirstDayOfWeek", wbxml.Text("1")),
		wbxml.E(wbxml.PageCalendar, "IsLeapMonth", wbxml.Text("1")),
	)
	got := recurrenceFromWBXML(in)
	if got == nil {
		t.Fatal("nil result")
	}
	if got.Type != RecurrenceWeekly || got.Interval != 2 || got.Occurrences != 10 ||
		got.DayOfWeek != 4 || got.DayOfMonth != 15 || got.WeekOfMonth != 3 ||
		got.MonthOfYear != 6 || got.CalendarType != 1 || got.FirstDayOfWeek != 1 ||
		!got.IsLeapMonth {
		t.Errorf("recurrence = %+v", got)
	}
	if got.Until.IsZero() {
		t.Error("Until not parsed")
	}
}

// TestParseException_allFields covers the Exception field branches that
// the existing roundtrip tests touch only via encode-decode symmetry.
func TestParseException_allFields(t *testing.T) {
	e := wbxml.E(wbxml.PageCalendar, "Exception",
		wbxml.E(wbxml.PageCalendar, "ExceptionStartTime", wbxml.Text("2026-05-12T14:00:00Z")),
		wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text("S")),
		wbxml.E(wbxml.PageCalendar, "Location", wbxml.Text("L")),
		wbxml.E(wbxml.PageCalendar, "StartTime", wbxml.Text("2026-05-12T15:00:00Z")),
		wbxml.E(wbxml.PageCalendar, "EndTime", wbxml.Text("2026-05-12T16:00:00Z")),
		wbxml.E(wbxml.PageCalendar, "AllDayEvent", wbxml.Text("1")),
		wbxml.E(wbxml.PageCalendar, "BusyStatus", wbxml.Text("2")),
		wbxml.E(wbxml.PageCalendar, "Reminder", wbxml.Text("15")),
		wbxml.E(wbxml.PageAirSyncBase, "Body",
			wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("body text")),
		),
	)
	got := parseException(e)
	if got.Subject != "S" || got.Location != "L" || !got.AllDayEvent ||
		got.BusyStatus != 2 || got.Reminder != 15 || got.Body != "body text" {
		t.Errorf("got %+v", got)
	}
	if got.ExceptionStartTime.IsZero() || got.StartTime.IsZero() || got.EndTime.IsZero() {
		t.Errorf("dates = %+v", got)
	}
}

func TestRecurrence_extendedFieldsRoundTrip(t *testing.T) {
	r := &Recurrence{
		Type:           RecurrenceMonthlyByDay,
		Interval:       1,
		Occurrences:    12,
		DayOfWeek:      DowMonday,
		WeekOfMonth:    2,
		MonthOfYear:    6,
		IsLeapMonth:    true,
		FirstDayOfWeek: 1,
		CalendarType:   1,
	}
	el := recurrenceToWBXML(r)
	got := recurrenceFromWBXML(el)
	if got.Occurrences != 12 || got.WeekOfMonth != 2 || got.MonthOfYear != 6 ||
		!got.IsLeapMonth || got.FirstDayOfWeek != 1 || got.CalendarType != 1 {
		t.Errorf("got %+v", got)
	}
}

func TestRecurrence_nilSafe(t *testing.T) {
	if recurrenceToWBXML(nil) != nil {
		t.Error("recurrenceToWBXML(nil) should be nil")
	}
	if recurrenceFromWBXML(nil) != nil {
		t.Error("recurrenceFromWBXML(nil) should be nil")
	}
}

func TestRecurrenceFromWBXML_skipsNonElement(t *testing.T) {
	// Stray text alongside the real fields must be ignored.
	el := wbxml.E(wbxml.PageCalendar, "Recurrence",
		wbxml.Text("stray"),
		wbxml.E(wbxml.PageCalendar, "Recurrence_Type", wbxml.Text("1")),
		wbxml.E(wbxml.PageCalendar, "Recurrence_Interval", wbxml.Text("2")),
	)
	got := recurrenceFromWBXML(el)
	if got.Type != RecurrenceWeekly || got.Interval != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestException_encodesAllDayReminderAndBody(t *testing.T) {
	x := Exception{
		ExceptionStartTime: time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC),
		AllDayEvent:        true,
		Reminder:           30,
		Body:               "exception body",
	}
	el := exceptionToWBXML(x)
	got := parseException(el)
	if !got.AllDayEvent || got.Reminder != 30 || got.Body != "exception body" {
		t.Errorf("got %+v", got)
	}
}

func TestParseException_skipsNonElement(t *testing.T) {
	el := wbxml.E(wbxml.PageCalendar, "Exception",
		wbxml.Text("stray"),
		wbxml.E(wbxml.PageCalendar, "ExceptionStartTime", wbxml.Text("2026-05-15T10:00:00Z")),
		wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text("S")),
	)
	got := parseException(el)
	if got.Subject != "S" || got.ExceptionStartTime.IsZero() {
		t.Errorf("got %+v", got)
	}
}

func TestUTF16RoundTrip(t *testing.T) {
	in := "héllo 世界 🌍"
	enc := utf16Encode(in)
	got := utf16Decode(enc)
	if got != in {
		t.Errorf("utf16 round-trip: %q vs %q", got, in)
	}
}
