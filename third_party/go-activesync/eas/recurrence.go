// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// RecurrenceType matches MS-ASCMD §2.2.3.149 (Type element under Recurrence).
type RecurrenceType int

const (
	RecurrenceDaily        RecurrenceType = 0
	RecurrenceWeekly       RecurrenceType = 1
	RecurrenceMonthlyDate  RecurrenceType = 2 // every Nth day-of-month
	RecurrenceMonthlyByDay RecurrenceType = 3 // 2nd Tuesday etc.
	RecurrenceYearlyDate   RecurrenceType = 5
	RecurrenceYearlyByDay  RecurrenceType = 6
)

// DayOfWeek is a bitmask used by Recurrence_DayOfWeek.
type DayOfWeek int

const (
	DowSunday    DayOfWeek = 1
	DowMonday    DayOfWeek = 2
	DowTuesday   DayOfWeek = 4
	DowWednesday DayOfWeek = 8
	DowThursday  DayOfWeek = 16
	DowFriday    DayOfWeek = 32
	DowSaturday  DayOfWeek = 64
	DowLastDay   DayOfWeek = 127 // for "last day of month" rules
)

// Recurrence describes a calendar event recurrence pattern.
type Recurrence struct {
	Type           RecurrenceType
	Interval       int       // every N days/weeks/months
	Until          time.Time // last instance; zero = forever
	Occurrences    int       // alternative to Until; 0 = unset
	DayOfWeek      DayOfWeek // for Weekly + Monthly/YearlyByDay
	DayOfMonth     int       // for MonthlyDate / YearlyDate
	WeekOfMonth    int       // 1-5 (5 = "last")
	MonthOfYear    int       // 1-12 for Yearly variants
	IsLeapMonth    bool      // 16.0+: lunar calendar quirk
	FirstDayOfWeek int       // 0=Sunday
	CalendarType   int       // 1=Gregorian etc.
}

// Exception is one date override on a recurring event.
type Exception struct {
	// ExceptionStartTime is the original instance time being overridden.
	ExceptionStartTime time.Time
	// Deleted, when true, removes the instance entirely; the other
	// fields are ignored.
	Deleted bool
	// Subject etc. — when non-empty, override the recurring event's
	// values for this instance only.
	Subject     string
	Location    string
	StartTime   time.Time
	EndTime     time.Time
	AllDayEvent bool
	BusyStatus  int
	Reminder    int
	Body        string
}

// recurrenceToWBXML serializes a Recurrence to its WBXML element.
// Returns nil if r is the zero value.
func recurrenceToWBXML(r *Recurrence) *wbxml.Element {
	if r == nil {
		return nil
	}
	e := wbxml.E(wbxml.PageCalendar, "Recurrence",
		wbxml.E(wbxml.PageCalendar, "Recurrence_Type", wbxml.Text(itoa(int(r.Type)))),
	)
	add := func(name string, val int) {
		if val != 0 {
			e.Children = append(e.Children, wbxml.E(wbxml.PageCalendar, name, wbxml.Text(itoa(val))))
		}
	}
	if r.Interval > 0 {
		add("Recurrence_Interval", r.Interval)
	}
	if r.Occurrences > 0 {
		add("Recurrence_Occurrences", r.Occurrences)
	}
	if !r.Until.IsZero() {
		e.Children = append(e.Children, wbxml.E(wbxml.PageCalendar, "Recurrence_Until",
			wbxml.Text(formatEASTime(r.Until))))
	}
	add("Recurrence_DayOfWeek", int(r.DayOfWeek))
	add("Recurrence_DayOfMonth", r.DayOfMonth)
	add("Recurrence_WeekOfMonth", r.WeekOfMonth)
	add("Recurrence_MonthOfYear", r.MonthOfYear)
	if r.CalendarType > 0 {
		add("CalendarType", r.CalendarType)
	}
	if r.FirstDayOfWeek > 0 {
		add("FirstDayOfWeek", r.FirstDayOfWeek)
	}
	if r.IsLeapMonth {
		add("IsLeapMonth", 1)
	}
	return e
}

// recurrenceFromWBXML parses a Recurrence element. Returns nil for nil input.
func recurrenceFromWBXML(e *wbxml.Element) *Recurrence {
	if e == nil {
		return nil
	}
	r := &Recurrence{}
	for _, c := range e.Children {
		ce, ok := c.(*wbxml.Element)
		if !ok {
			continue
		}
		v := ce.TextContent()
		switch ce.Name {
		case "Recurrence_Type":
			r.Type = RecurrenceType(atoi(v))
		case "Recurrence_Interval":
			r.Interval = atoi(v)
		case "Recurrence_Occurrences":
			r.Occurrences = atoi(v)
		case "Recurrence_Until":
			r.Until, _ = parseEASTime(v)
		case "Recurrence_DayOfWeek":
			r.DayOfWeek = DayOfWeek(atoi(v))
		case "Recurrence_DayOfMonth":
			r.DayOfMonth = atoi(v)
		case "Recurrence_WeekOfMonth":
			r.WeekOfMonth = atoi(v)
		case "Recurrence_MonthOfYear":
			r.MonthOfYear = atoi(v)
		case "CalendarType":
			r.CalendarType = atoi(v)
		case "FirstDayOfWeek":
			r.FirstDayOfWeek = atoi(v)
		case "IsLeapMonth":
			r.IsLeapMonth = v == "1"
		}
	}
	return r
}

// exceptionToWBXML serializes an Exception to its <Exception> element.
func exceptionToWBXML(x Exception) *wbxml.Element {
	e := wbxml.E(wbxml.PageCalendar, "Exception",
		wbxml.E(wbxml.PageCalendar, "ExceptionStartTime", wbxml.Text(formatEASTime(x.ExceptionStartTime))),
	)
	if x.Deleted {
		e.Children = append(e.Children, wbxml.E(wbxml.PageCalendar, "Deleted", wbxml.Text("1")))
		return e
	}
	addText := func(name, val string) {
		if val != "" {
			e.Children = append(e.Children, wbxml.E(wbxml.PageCalendar, name, wbxml.Text(val)))
		}
	}
	addText("Subject", x.Subject)
	addText("Location", x.Location)
	if !x.StartTime.IsZero() {
		addText("StartTime", formatEASTime(x.StartTime))
	}
	if !x.EndTime.IsZero() {
		addText("EndTime", formatEASTime(x.EndTime))
	}
	if x.AllDayEvent {
		e.Children = append(e.Children, wbxml.E(wbxml.PageCalendar, "AllDayEvent", wbxml.Text("1")))
	}
	if x.BusyStatus > 0 {
		e.Children = append(e.Children, wbxml.E(wbxml.PageCalendar, "BusyStatus", wbxml.Text(itoa(x.BusyStatus))))
	}
	if x.Reminder > 0 {
		e.Children = append(e.Children, wbxml.E(wbxml.PageCalendar, "Reminder", wbxml.Text(itoa(x.Reminder))))
	}
	if x.Body != "" {
		e.Children = append(e.Children,
			wbxml.E(wbxml.PageAirSyncBase, "Body",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text(x.Body)),
			))
	}
	return e
}

// parseException reads one <Exception> element.
func parseException(e *wbxml.Element) Exception {
	x := Exception{}
	for _, c := range e.Children {
		ce, ok := c.(*wbxml.Element)
		if !ok {
			continue
		}
		v := ce.TextContent()
		switch ce.Name {
		case "ExceptionStartTime":
			x.ExceptionStartTime, _ = parseEASTime(v)
		case "Deleted":
			x.Deleted = v == "1"
		case "Subject":
			x.Subject = v
		case "Location":
			x.Location = v
		case "StartTime":
			x.StartTime, _ = parseEASTime(v)
		case "EndTime":
			x.EndTime, _ = parseEASTime(v)
		case "AllDayEvent":
			x.AllDayEvent = v == "1"
		case "BusyStatus":
			x.BusyStatus = atoi(v)
		case "Reminder":
			x.Reminder = atoi(v)
		case "Body":
			for _, bc := range ce.Children {
				if be, ok := bc.(*wbxml.Element); ok && be.Codepage == wbxml.PageAirSyncBase && be.Name == "Data" {
					x.Body = be.TextContent()
				}
			}
		}
	}
	return x
}
