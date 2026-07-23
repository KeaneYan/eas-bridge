// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

func calendarSyncResponse(syncKey string, events ...*wbxml.Element) []byte {
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "Class", wbxml.Text("Calendar")),
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(syncKey)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal")),
		wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
	)
	if len(events) > 0 {
		commands := wbxml.E(wbxml.PageAirSync, "Commands")
		for _, e := range events {
			commands.Children = append(commands.Children, e)
		}
		collection.Children = append(collection.Children, commands)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	out, err := wbxml.Marshal(doc, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return out
}

func eventAdd(serverID, subject string, start, end time.Time) *wbxml.Element {
	return wbxml.E(wbxml.PageAirSync, "Add",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text(subject)),
			wbxml.E(wbxml.PageCalendar, "StartTime", wbxml.Text(formatEASTime(start))),
			wbxml.E(wbxml.PageCalendar, "EndTime", wbxml.Text(formatEASTime(end))),
			wbxml.E(wbxml.PageCalendar, "AllDayEvent", wbxml.Text("0")),
			wbxml.E(wbxml.PageCalendar, "BusyStatus", wbxml.Text("2")),
			wbxml.E(wbxml.PageCalendar, "Attendees",
				wbxml.E(wbxml.PageCalendar, "Attendee",
					wbxml.E(wbxml.PageCalendar, "Name", wbxml.Text("Bob")),
					wbxml.E(wbxml.PageCalendar, "Email", wbxml.Text("bob@x")),
					wbxml.E(wbxml.PageCalendar, "AttendeeStatus", wbxml.Text("3")),
				),
			),
		),
	)
}

// TestSyncCalendar_emptyResponse: an empty body decodes to nil resp,
// which the caller treats as "no changes" (return current key).
func TestSyncCalendar_emptyResponse(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		// Empty body → nil resp on decode.
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	res, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SyncKey != "K1" {
		t.Errorf("SyncKey = %q, want K1", res.SyncKey)
	}
}

// TestSyncCalendar_topLevelStatusError: server reports a non-OK status
// at the Sync root → caller gets a StatusError.
func TestSyncCalendar_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("110")), // ServerError
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	if _, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true}); err == nil {
		t.Error("want StatusError")
	}
}

// TestSyncCalendar_perCollectionStatusError: top is OK but the inner
// Collection/Status reports a non-OK code.
func TestSyncCalendar_perCollectionStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("8")), // ObjectNotFound
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	if _, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true}); err == nil {
		t.Error("want StatusError")
	}
}

func TestSyncCalendar_emptyFolderRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit")
	})
	if _, err := c.SyncCalendar(context.Background(), "", CalendarSyncOptions{}); err == nil {
		t.Error("want error for empty folderID")
	}
}

// TestSyncCalendar_invalidKeyRetries: server returns Status=3 once, the
// client must reset the per-folder key to "0" and re-issue the same
// command transparently.
func TestSyncCalendar_invalidKeyRetries(t *testing.T) {
	var calls int32
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		if n == 1 {
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(body)
			return
		}
		w.Write(calendarSyncResponse("KEY-2"))
	})
	// Pre-populate a stale key so the first call sends it.
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "STALE")
	res, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SyncKey != "KEY-2" {
		t.Errorf("SyncKey = %q, want KEY-2", res.SyncKey)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d, want 2 (status 3 → reset → retry)", got)
	}
}

func TestSyncCalendar_parsesEvent(t *testing.T) {
	start := time.Date(2026, 5, 9, 14, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(calendarSyncResponse("CK1", eventAdd("ev1", "Team standup", start, end)))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "CK0")
	res, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("len(Added)=%d", len(res.Added))
	}
	ev := res.Added[0]
	if ev.Subject != "Team standup" || !ev.StartTime.Equal(start) || !ev.EndTime.Equal(end) {
		t.Errorf("event = %+v", ev)
	}
	if ev.BusyStatus != 2 {
		t.Errorf("BusyStatus = %d", ev.BusyStatus)
	}
	if len(ev.Attendees) != 1 || ev.Attendees[0].Email != "bob@x" {
		t.Errorf("attendees = %+v", ev.Attendees)
	}
}

func TestCreateEvent_returnsServerID(t *testing.T) {
	var (
		mu     sync.Mutex
		calls  int
		bodies [][]byte
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		mu.Lock()
		calls++
		thisCall := calls
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch thisCall {
		case 1:
			// Bootstrap
			w.Write(calendarSyncResponse("CK-BOOT"))
		case 2:
			// Echo back the ClientId with a Server-assigned id.
			req, _ := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
			cid := req.Root.Find("ClientId").TextContent()
			doc := &wbxml.Document{
				Root: wbxml.E(wbxml.PageAirSync, "Sync",
					wbxml.E(wbxml.PageAirSync, "Collections",
						wbxml.E(wbxml.PageAirSync, "Collection",
							wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("CK-DONE")),
							wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal")),
							wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
							wbxml.E(wbxml.PageAirSync, "Responses",
								wbxml.E(wbxml.PageAirSync, "Add",
									wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(cid)),
									wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("cal:NEW")),
									wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
								),
							),
						),
					),
				),
			}
			b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(b)
		}
	})

	id, err := c.CreateEvent(context.Background(), "cal", EventDraft{
		Subject:   "Test",
		StartTime: time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "cal:NEW" {
		t.Errorf("id = %q", id)
	}
	if calls != 2 {
		t.Errorf("calls = %d (bootstrap + change expected)", calls)
	}
	// Body of second call should contain Subject "Test".
	if !strings.Contains(string(bodies[1]), "Test") {
		t.Errorf("body 2 missing subject")
	}
}

func TestUpdateEvent_andDeleteEvent(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(calendarSyncResponse("CK"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "CK-PRIOR")

	if err := c.UpdateEvent(context.Background(), "cal", "ev1", EventDraft{
		Subject:   "Renamed",
		StartTime: time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteEvent(context.Background(), "cal", "ev1"); err != nil {
		t.Fatal(err)
	}
}

func TestRespondInvite_validation(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit")
	})
	if _, err := c.RespondInvite(context.Background(), "", "id", MeetingAccept); err == nil {
		t.Error("want error for empty folderID")
	}
	if _, err := c.RespondInvite(context.Background(), "f", "", MeetingAccept); err == nil {
		t.Error("want error for empty serverID")
	}
}

func TestRespondInvite_topLevelStatusOnly(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// No <Result> — some servers only emit a top-level Status.
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMeetingResponse, "MeetingResponse",
				wbxml.E(wbxml.PageMeetingResponse, "Status", wbxml.Text("1")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.RespondInvite(context.Background(), "inbox", "i-1", MeetingAccept)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusOK {
		t.Errorf("Status = %d", res.Status)
	}
}

func TestRespondInvite_perResultStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMeetingResponse, "MeetingResponse",
				wbxml.E(wbxml.PageMeetingResponse, "Result",
					wbxml.E(wbxml.PageMeetingResponse, "Status", wbxml.Text("3")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.RespondInvite(context.Background(), "inbox", "i-1", MeetingDecline); err == nil {
		t.Error("want error for non-OK Result/Status")
	}
}

func TestRespondInvite_basic(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMeetingResponse, "MeetingResponse",
				wbxml.E(wbxml.PageMeetingResponse, "Result",
					wbxml.E(wbxml.PageMeetingResponse, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageMeetingResponse, "CalendarId", wbxml.Text("cal:42")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.RespondInvite(context.Background(), "inbox", "invite-1", MeetingAccept)
	if err != nil {
		t.Fatal(err)
	}
	if res.CalendarID != "cal:42" || res.Status != 1 {
		t.Errorf("res = %+v", res)
	}
}

func TestRespondInvite_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	})
	if _, err := c.RespondInvite(context.Background(), "inbox", "id", MeetingAccept); err == nil {
		t.Error("want HTTP error")
	}
}

func TestRespondInvite_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → nil resp
	})
	_, err := c.RespondInvite(context.Background(), "inbox", "id", MeetingAccept)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v", err)
	}
}

func TestRespondInvite_topLevelStatusErrorWithoutResult(t *testing.T) {
	// Some servers reject the request with only a top-level <Status>; the
	// fall-through path (no <Result>) must surface that as a StatusError.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMeetingResponse, "MeetingResponse",
				wbxml.E(wbxml.PageMeetingResponse, "Status", wbxml.Text("4")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.RespondInvite(context.Background(), "inbox", "id", MeetingDecline); !IsStatusCode(err, 4) {
		t.Errorf("err = %v", err)
	}
}

func TestRespondInvite_resultWithoutStatusFillsOK(t *testing.T) {
	// <Result> present but with no <Status> child: caller should still see
	// a StatusOK result (and any CalendarID echoed back).
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageMeetingResponse, "MeetingResponse",
				wbxml.E(wbxml.PageMeetingResponse, "Result",
					wbxml.E(wbxml.PageMeetingResponse, "CalendarId", wbxml.Text("cal:7")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.RespondInvite(context.Background(), "inbox", "id", MeetingAccept)
	if err != nil {
		t.Fatal(err)
	}
	if res.CalendarID != "cal:7" || res.Status != StatusOK {
		t.Errorf("res = %+v", res)
	}
}

func TestParseCalendarField_allFields(t *testing.T) {
	out := EventItem{}
	cases := []struct {
		name, text string
		check      func(*EventItem) bool
	}{
		{"Subject", "Standup", func(e *EventItem) bool { return e.Subject == "Standup" }},
		{"Location", "Conf 5", func(e *EventItem) bool { return e.Location == "Conf 5" }},
		{"AllDayEvent", "1", func(e *EventItem) bool { return e.AllDayEvent }},
		{"BusyStatus", "2", func(e *EventItem) bool { return e.BusyStatus == 2 }},
		{"Sensitivity", "1", func(e *EventItem) bool { return e.Sensitivity == 1 }},
		{"MeetingStatus", "3", func(e *EventItem) bool { return e.MeetingStatus == 3 }},
		{"Reminder", "15", func(e *EventItem) bool { return e.Reminder == 15 }},
		{"OrganizerName", "Alice", func(e *EventItem) bool { return e.OrganizerName == "Alice" }},
		{"OrganizerEmail", "alice@x", func(e *EventItem) bool { return e.OrganizerEmail == "alice@x" }},
		{"UID", "uid-123", func(e *EventItem) bool { return e.UID == "uid-123" }},
	}
	for _, tc := range cases {
		parseCalendarField(&out, wbxml.E(wbxml.PageCalendar, tc.name, wbxml.Text(tc.text)))
		if !tc.check(&out) {
			t.Errorf("%s: %+v", tc.name, out)
		}
	}
	// StartTime / EndTime go through parseEASTime.
	parseCalendarField(&out, wbxml.E(wbxml.PageCalendar, "StartTime", wbxml.Text("2026-05-12T14:00:00Z")))
	parseCalendarField(&out, wbxml.E(wbxml.PageCalendar, "EndTime", wbxml.Text("2026-05-12T15:00:00Z")))
	if out.StartTime.IsZero() || out.EndTime.IsZero() {
		t.Errorf("times = %+v", out)
	}
	// Attendees is a structured nested field.
	parseCalendarField(&out, wbxml.E(wbxml.PageCalendar, "Attendees",
		wbxml.E(wbxml.PageCalendar, "Attendee",
			wbxml.E(wbxml.PageCalendar, "Name", wbxml.Text("Bob")),
			wbxml.E(wbxml.PageCalendar, "Email", wbxml.Text("bob@x")),
			wbxml.E(wbxml.PageCalendar, "AttendeeStatus", wbxml.Text("3")),
			wbxml.E(wbxml.PageCalendar, "AttendeeType", wbxml.Text("1")),
		),
	))
	if len(out.Attendees) != 1 || out.Attendees[0].Email != "bob@x" ||
		out.Attendees[0].AttendeeStatus != 3 || out.Attendees[0].AttendeeType != 1 {
		t.Errorf("Attendees = %+v", out.Attendees)
	}
}

func TestParseCalendarField_timezone(t *testing.T) {
	out := EventItem{}
	// EST: UTC-5 (StandardBias = +300 minutes since EAS bias is offset
	// from UTC, not the IANA-style negative). UTC's all-zero blob is
	// indistinguishable from the EASTimeZone zero value, so use a real
	// offset to verify decode actually happened.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("zoneinfo unavailable")
	}
	tz := EncodeTimeZone(loc)
	parseCalendarField(&out, wbxml.E(wbxml.PageCalendar, "TimeZone", wbxml.Text(tz)))
	if out.TimeZoneRaw != tz {
		t.Error("TimeZoneRaw not stored")
	}
	if out.TimeZone == (EASTimeZone{}) {
		t.Error("TimeZone not decoded (still zero)")
	}
}

func TestParseCalendarField_invalidTimezoneIsIgnored(t *testing.T) {
	out := EventItem{}
	parseCalendarField(&out, wbxml.E(wbxml.PageCalendar, "TimeZone", wbxml.Text("not-base64-and-not-the-right-length")))
	if out.TimeZoneRaw == "" {
		t.Error("Raw should still be stored even when decode fails")
	}
	if out.TimeZone != (EASTimeZone{}) {
		t.Error("TimeZone should remain zero when decode fails")
	}
}

func TestBuildEventApp_richDraft(t *testing.T) {
	start := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	app := buildEventApp(EventDraft{
		Subject:     "Planning",
		Location:    "Conf 5",
		StartTime:   start,
		EndTime:     end,
		AllDayEvent: false,
		BusyStatus:  2,
		Sensitivity: 1,
		Reminder:    15,
		Body:        "agenda",
		Attendees: []EventAttendee{
			{Name: "Bob", Email: "bob@x"},
			{Email: "carol@x"}, // name omitted intentionally
		},
		TimeZone: &EASTimeZone{StandardBias: 0, DaylightBias: -60},
		Recurrence: &Recurrence{
			Type:     RecurrenceWeekly,
			Interval: 1,
		},
	})
	wantText := map[string]string{
		"Subject":     "Planning",
		"Location":    "Conf 5",
		"StartTime":   formatEASTime(start),
		"EndTime":     formatEASTime(end),
		"BusyStatus":  "2",
		"Sensitivity": "1",
		"Reminder":    "15",
	}
	for name, want := range wantText {
		if el := app.Find(name); el == nil || el.TextContent() != want {
			t.Errorf("%s = %v, want %q", name, el, want)
		}
	}
	if app.Find("AllDayEvent") != nil {
		t.Error("AllDayEvent=false should be omitted")
	}
	atts := app.Find("Attendees")
	if atts == nil || len(atts.Children) != 2 {
		t.Fatalf("Attendees = %v", atts)
	}
	if app.Find("TimeZone") == nil {
		t.Error("TimeZone missing")
	}
	if app.Find("Recurrence") == nil {
		t.Error("Recurrence missing")
	}
	if body := app.Find("Body"); body == nil {
		t.Error("Body missing")
	} else if data := body.Find("Data"); data == nil || data.TextContent() != "agenda" {
		t.Errorf("Body/Data = %v", data)
	}
}

func TestBuildEventApp_allDayAndExceptions(t *testing.T) {
	start := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	app := buildEventApp(EventDraft{
		Subject:     "Conference",
		StartTime:   start,
		EndTime:     end,
		AllDayEvent: true,
		Exceptions: []Exception{
			{ExceptionStartTime: start.Add(48 * time.Hour), Deleted: true},
		},
	})
	if ad := app.Find("AllDayEvent"); ad == nil || ad.TextContent() != "1" {
		t.Errorf("AllDayEvent = %v", ad)
	}
	exc := app.Find("Exceptions")
	if exc == nil || len(exc.Children) != 1 {
		t.Fatalf("Exceptions = %v", exc)
	}
}

func TestBuildEventApp_timeZoneRawWinsOverStruct(t *testing.T) {
	start := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	app := buildEventApp(EventDraft{
		Subject:     "X",
		StartTime:   start,
		EndTime:     end,
		TimeZoneRaw: "RAW-TZ-BLOB",
		TimeZone:    &EASTimeZone{}, // should be ignored when Raw is set
	})
	tz := app.Find("TimeZone")
	if tz == nil || tz.TextContent() != "RAW-TZ-BLOB" {
		t.Errorf("TimeZone = %v, want RAW-TZ-BLOB", tz)
	}
}

func TestParseEventBody_textAndOpaque(t *testing.T) {
	textBody := wbxml.E(wbxml.PageAirSyncBase, "Body",
		wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
		wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("plain body")),
	)
	out := EventItem{}
	parseEventBody(&out, textBody)
	if out.BodyType != BodyTypePlain || out.Body != "plain body" {
		t.Errorf("text body: got %+v", out)
	}

	opaqueBody := wbxml.E(wbxml.PageAirSyncBase, "Body",
		wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("4")),
		wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Opaque([]byte("MIME bytes"))),
	)
	out2 := EventItem{}
	parseEventBody(&out2, opaqueBody)
	if out2.BodyType != BodyTypeMIME || out2.Body != "MIME bytes" {
		t.Errorf("opaque body: got %+v", out2)
	}
}

func TestParseEventBody_skipsOtherCodepages(t *testing.T) {
	body := wbxml.E(wbxml.PageAirSyncBase, "Body",
		// Wrong codepage — must be ignored.
		wbxml.E(wbxml.PageCalendar, "Type", wbxml.Text("99")),
	)
	out := EventItem{}
	parseEventBody(&out, body)
	if out.BodyType != 0 {
		t.Errorf("expected zero BodyType, got %v", out.BodyType)
	}
}

func TestSyncCalendar_invalidKeyResetFails(t *testing.T) {
	// Status=3 → SetSyncKey reset fails → error wraps "reset key".
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), "cal", "STALE")
	es.setSyncKeyErr = errSentinel("ro")
	_, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true})
	if err == nil || !strings.Contains(err.Error(), "reset key") {
		t.Errorf("err = %v", err)
	}
}

func TestSyncCalendar_bootstrapTwoCallsWhenEmpty(t *testing.T) {
	// First call returns empty result (no items), second call (bootstrap
	// re-call) returns the actual events.
	calls := 0
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		if calls == 1 {
			// Empty bootstrap reply.
			w.Write(calendarSyncResponse("CK-BOOT"))
			return
		}
		w.Write(calendarSyncResponse("CK-DATA",
			eventAdd("ev-1", "Real Event",
				time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
			),
		))
	})
	res, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if len(res.Added) != 1 {
		t.Errorf("added = %+v", res.Added)
	}
}

func TestSyncCalendar_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("read fail")}
	if _, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true}); err == nil {
		t.Error("want SyncKey read error")
	}
}

func TestSyncCalendar_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true}); err == nil {
		t.Error("want HTTP error")
	}
}

func TestSyncCalendar_missingCollection(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	if _, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true}); err == nil {
		t.Error("want missing-Collection error")
	}
}

func TestSyncCalendar_moreAvailableAndChangeDelete(t *testing.T) {
	change := wbxml.E(wbxml.PageAirSync, "Change",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("ev-2")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text("Renamed")),
		),
	)
	del := wbxml.E(wbxml.PageAirSync, "Delete",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("ev-3")),
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Add MoreAvailable to the canned response.
		collection := wbxml.E(wbxml.PageAirSync, "Collection",
			wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("K2")),
			wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal")),
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSync, "MoreAvailable"),
			wbxml.E(wbxml.PageAirSync, "Commands",
				wbxml.Text("stray"), // exercise non-element skip
				change,
				del,
			),
		)
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	res, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.MoreAvailable {
		t.Error("MoreAvailable not set")
	}
	if len(res.Changed) != 1 || res.Changed[0].Subject != "Renamed" {
		t.Errorf("changed = %+v", res.Changed)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "ev-3" {
		t.Errorf("deleted = %v", res.Deleted)
	}
}

func TestSyncCalendar_missingSyncKey(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					// no SyncKey
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	_, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true})
	if err == nil || !strings.Contains(err.Error(), "SyncKey") {
		t.Errorf("err = %v", err)
	}
}

func TestSyncCalendar_persistKeyError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(calendarSyncResponse("NEW"))
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), "cal", "K1")
	es.setSyncKeyErr = errSentinel("ro")
	_, err := c.SyncCalendar(context.Background(), "cal", CalendarSyncOptions{NoBootstrap: true})
	if err == nil || !strings.Contains(err.Error(), "persist") {
		t.Errorf("err = %v", err)
	}
}

func TestParseEventFromCommand_nilApp(t *testing.T) {
	el := wbxml.E(wbxml.PageAirSync, "Add",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("ev-1")),
		// no ApplicationData
	)
	got := parseEventFromCommand(el)
	if got.ServerID != "ev-1" {
		t.Errorf("got %+v", got)
	}
}

func TestParseEventFromCommand_skipsNonElementAndForeignBody(t *testing.T) {
	el := wbxml.E(wbxml.PageAirSync, "Add",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("ev-1")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.Text("stray"), // non-element child
			wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text("S")),
			wbxml.E(wbxml.PageAirSyncBase, "Body",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text("body")),
			),
		),
	)
	got := parseEventFromCommand(el)
	if got.Subject != "S" || got.Body != "body" {
		t.Errorf("got %+v", got)
	}
}

func TestParseCalendarField_recurrenceAndExceptions(t *testing.T) {
	out := EventItem{}
	rec := wbxml.E(wbxml.PageCalendar, "Recurrence",
		wbxml.E(wbxml.PageCalendar, "Recurrence_Type", wbxml.Text("1")),
	)
	parseCalendarField(&out, rec)
	if out.Recurrence == nil || out.Recurrence.Type != RecurrenceWeekly {
		t.Errorf("recurrence = %+v", out.Recurrence)
	}
	exc := wbxml.E(wbxml.PageCalendar, "Exceptions",
		wbxml.Text("stray"), // non-element skipped
		wbxml.E(wbxml.PageCalendar, "Exception",
			wbxml.E(wbxml.PageCalendar, "ExceptionStartTime", wbxml.Text("2026-05-15T10:00:00Z")),
			wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text("changed")),
		),
	)
	parseCalendarField(&out, exc)
	if len(out.Exceptions) != 1 || out.Exceptions[0].Subject != "changed" {
		t.Errorf("exceptions = %+v", out.Exceptions)
	}
}

func TestParseCalendarField_skipsNonAttendee(t *testing.T) {
	out := EventItem{}
	atts := wbxml.E(wbxml.PageCalendar, "Attendees",
		wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text("ignored")), // not Attendee
		wbxml.E(wbxml.PageCalendar, "Attendee",
			wbxml.E(wbxml.PageCalendar, "Name", wbxml.Text("Bob")),
			wbxml.E(wbxml.PageCalendar, "Email", wbxml.Text("bob@x")),
			wbxml.E(wbxml.PageCalendar, "AttendeeStatus", wbxml.Text("3")),
			wbxml.E(wbxml.PageCalendar, "AttendeeType", wbxml.Text("1")),
		),
	)
	parseCalendarField(&out, atts)
	if len(out.Attendees) != 1 || out.Attendees[0].Email != "bob@x" {
		t.Errorf("attendees = %+v", out.Attendees)
	}
}

func TestCreateEvent_validation(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	if _, err := c.CreateEvent(context.Background(), "", EventDraft{}); err == nil {
		t.Error("want error for empty folderID")
	}
	if _, err := c.CreateEvent(context.Background(), "cal", EventDraft{}); err == nil {
		t.Error("want error for missing StartTime/EndTime")
	}
}

func TestCreateEvent_ensureSyncedFails(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("boom")}
	_, err := c.CreateEvent(context.Background(), "cal", EventDraft{
		StartTime: time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Error("want ensureSynced error")
	}
}

func TestCreateEvent_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	_, err := c.CreateEvent(context.Background(), "cal", EventDraft{
		StartTime: time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Error("want HTTP error")
	}
}

func TestCreateEvent_responseFallbackToClientID(t *testing.T) {
	// Server replies with a stray non-Add Response — CreateEvent must
	// fall back to returning the client-generated id.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("CK-DONE")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageAirSync, "Responses",
						wbxml.E(wbxml.PageAirSync, "Change",
							wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("ignored")),
						),
					),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	id, err := c.CreateEvent(context.Background(), "cal", EventDraft{
		StartTime: time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Error("expected client-generated id fallback")
	}
}

func TestUpdateEvent_validation(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	if err := c.UpdateEvent(context.Background(), "", "id", EventDraft{}); err == nil {
		t.Error("want error for empty folderID")
	}
	if err := c.UpdateEvent(context.Background(), "cal", "", EventDraft{}); err == nil {
		t.Error("want error for empty serverID")
	}
}

func TestUpdateEvent_ensureSyncedFails(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("boom")}
	if err := c.UpdateEvent(context.Background(), "cal", "id", EventDraft{}); err == nil {
		t.Error("want ensureSynced error")
	}
}

func TestDeleteEvent_validation(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	if err := c.DeleteEvent(context.Background(), "", "id"); err == nil {
		t.Error("want error for empty folderID")
	}
	if err := c.DeleteEvent(context.Background(), "cal", ""); err == nil {
		t.Error("want error for empty serverID")
	}
}

func TestDeleteEvent_ensureSyncedFails(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("boom")}
	if err := c.DeleteEvent(context.Background(), "cal", "id"); err == nil {
		t.Error("want ensureSynced error")
	}
}

func TestSendSyncCommands_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("read fail")}
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Delete",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("x")),
		),
	)
	if _, err := c.sendSyncCommands(context.Background(), "cal", cmds); err == nil ||
		!strings.Contains(err.Error(), "read key") {
		t.Errorf("err = %v", err)
	}
}

func TestSendSyncCommands_emptyResponseIsNil(t *testing.T) {
	// Empty 200 OK from the server. sendSyncCommands returns (nil, nil)
	// — UpdateEvent surfaces no error since (_, err) is nil.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	if err := c.UpdateEvent(context.Background(), "cal", "id", EventDraft{}); err != nil {
		t.Errorf("empty response should not error, got %v", err)
	}
}

func TestSendSyncCommands_collectionStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("K2")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("cal")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("8")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "cal", "K1")
	if err := c.UpdateEvent(context.Background(), "cal", "id", EventDraft{}); !IsStatusCode(err, 8) {
		t.Errorf("err = %v", err)
	}
}

func TestSendSyncCommands_persistKeyError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(calendarSyncResponse("NEW"))
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), "cal", "K1")
	es.setSyncKeyErr = errSentinel("ro")
	if err := c.UpdateEvent(context.Background(), "cal", "id", EventDraft{}); err == nil ||
		!strings.Contains(err.Error(), "persist") {
		t.Errorf("err = %v", err)
	}
}

func TestFormatEASTime(t *testing.T) {
	t1 := time.Date(2024, 1, 15, 12, 34, 56, 0, time.UTC)
	got := formatEASTime(t1)
	want := "2024-01-15T12:34:56.000Z"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
