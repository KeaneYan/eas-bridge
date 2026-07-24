package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
)

// newCalWriteBackend 构造带日历文件夹和一个既有事件的测试后端。
func newCalWriteBackend(t *testing.T, c *easmock.Client) *caldavBackend {
	t.Helper()
	st, err := loadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Folders = []eas.Folder{
		{ServerID: "cal", Type: eas.FolderTypeCalendar, DisplayName: "CALENDAR"},
	}
	st.Events["cal:1"] = eas.EventItem{
		ServerID:       "cal:1",
		UID:            "existing-uid",
		Subject:        "周会",
		StartTime:      time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC),
		BusyStatus:     2,
		OrganizerEmail: "boss@webank.com",
		MeetingStatus:  1,
		TimeZoneRaw:    "raw-tz-blob",
	}
	return &caldavBackend{engine: &syncEngine{st: st, c: c, cfg: &config{User: "me@webank.com"}}}
}

func buildICal(t *testing.T, body string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(body)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

const testVEvent = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:new-uid-123
SUMMARY:评审会
LOCATION:3F会议室
DESCRIPTION:带资料
DTSTART:20260728T060000Z
DTEND:20260728T070000Z
TRANSP:OPAQUE
CLASS:PRIVATE
BEGIN:VALARM
ACTION:DISPLAY
TRIGGER:-PT15M
END:VALARM
END:VEVENT
END:VCALENDAR`

func TestPutCreateEvent(t *testing.T) {
	var gotDraft eas.EventDraft
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			CreateEventFunc: func(_ context.Context, folderID string, draft eas.EventDraft) (string, error) {
				if folderID != "cal" {
					t.Errorf("folderID = %q", folderID)
				}
				gotDraft = draft
				return "cal:99", nil
			},
		},
	}
	b := newCalWriteBackend(t, mock)
	obj, err := b.PutCalendarObject(context.Background(),
		caldavCalendarPath+"new-uid-123.ics", buildICal(t, testVEvent), nil)
	if err != nil {
		t.Fatal(err)
	}
	// draft 字段核对
	if gotDraft.Subject != "评审会" || gotDraft.Location != "3F会议室" || gotDraft.Body != "带资料" {
		t.Fatalf("draft = %+v", gotDraft)
	}
	if gotDraft.Sensitivity != 2 || gotDraft.Reminder != 15 || gotDraft.BusyStatus != 2 {
		t.Fatalf("draft flags = %+v", gotDraft)
	}
	if gotDraft.AllDayEvent {
		t.Fatal("不应是全天事件")
	}
	// 本地落库（EAS 不回显）
	b.engine.st.mu.Lock()
	created := b.engine.st.Events["cal:99"]
	b.engine.st.mu.Unlock()
	if created.Subject != "评审会" || created.UID != "new-uid-123" || created.OrganizerEmail != "me@webank.com" {
		t.Fatalf("created = %+v", created)
	}
	if obj == nil || !strings.Contains(obj.Path, "cal") {
		t.Fatalf("obj = %+v", obj)
	}
}

func TestPutUpdateEvent(t *testing.T) {
	var gotDraft eas.EventDraft
	var gotID string
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			UpdateEventFunc: func(_ context.Context, folderID, serverID string, draft eas.EventDraft) error {
				gotID = serverID
				gotDraft = draft
				return nil
			},
		},
	}
	b := newCalWriteBackend(t, mock)
	body := strings.Replace(testVEvent, "UID:new-uid-123", "UID:existing-uid", 1)
	obj, err := b.PutCalendarObject(context.Background(),
		caldavCalendarPath+"cal%3A1.ics", buildICal(t, body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != "cal:1" {
		t.Fatalf("update serverID = %q", gotID)
	}
	// 时区原文必须保留（byte-identical round-trip）
	if gotDraft.TimeZoneRaw != "raw-tz-blob" {
		t.Fatalf("TimeZoneRaw = %q", gotDraft.TimeZoneRaw)
	}
	// 服务器侧字段保留
	b.engine.st.mu.Lock()
	updated := b.engine.st.Events["cal:1"]
	b.engine.st.mu.Unlock()
	if updated.OrganizerEmail != "boss@webank.com" || updated.MeetingStatus != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.Subject != "评审会" {
		t.Fatalf("subject not updated: %+v", updated)
	}
	if obj == nil {
		t.Fatal("obj nil")
	}
}

func TestDeleteEvent(t *testing.T) {
	var gotID string
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			DeleteEventFunc: func(_ context.Context, folderID, serverID string) error {
				gotID = serverID
				return nil
			},
		},
	}
	b := newCalWriteBackend(t, mock)
	if err := b.DeleteCalendarObject(context.Background(), caldavCalendarPath+"cal%3A1.ics"); err != nil {
		t.Fatal(err)
	}
	if gotID != "cal:1" {
		t.Fatalf("delete serverID = %q", gotID)
	}
	b.engine.st.mu.Lock()
	_, exists := b.engine.st.Events["cal:1"]
	b.engine.st.mu.Unlock()
	if exists {
		t.Fatal("事件应已从本地删除")
	}
}

func TestRRULERoundTrip(t *testing.T) {
	cases := []string{
		"FREQ=DAILY",
		"FREQ=DAILY;INTERVAL=3",
		"FREQ=WEEKLY;BYDAY=MO,WE,FR",
		"FREQ=WEEKLY;BYDAY=TU;COUNT=10",
		"FREQ=MONTHLY;BYMONTHDAY=15",
		"FREQ=MONTHLY;BYDAY=2TU",
		"FREQ=MONTHLY;BYDAY=-1FR",
		"FREQ=YEARLY;BYMONTH=7;BYMONTHDAY=24",
		"FREQ=YEARLY;BYMONTH=3;BYDAY=1MO",
		"FREQ=WEEKLY;BYDAY=MO;UNTIL=20261231T000000Z",
	}
	for _, rr := range cases {
		r := rruleToEASRecurrence(rr)
		if r == nil {
			t.Errorf("解析失败: %s", rr)
			continue
		}
		back := easRecurrenceToRRULE(r)
		// 正规化比较：拆分集合语义（字段顺序固定所以直接比字符串）
		if back != rr {
			t.Errorf("round-trip: %s → %+v → %s", rr, r, back)
		}
	}
}

func TestRRULEUnsupported(t *testing.T) {
	if r := rruleToEASRecurrence("FREQ=HOURLY"); r != nil {
		t.Fatal("HOURLY 应返回 nil")
	}
	if r := rruleToEASRecurrence("FREQ=MONTHLY"); r != nil {
		t.Fatal("无 BYMONTHDAY/BYDAY 的 MONTHLY 应返回 nil")
	}
}

func TestParseICalDateTime(t *testing.T) {
	mk := func(v string, vt ical.ValueType) *ical.Prop {
		p := ical.NewProp("DTSTART")
		p.Value = v
		p.SetValueType(vt)
		return p
	}
	// UTC 定时
	ts, allDay, err := parseICalDateTime(mk("20260728T060000Z", ical.ValueDateTime))
	if err != nil || allDay || ts.Hour() != 6 || ts.Location() != time.UTC {
		t.Fatalf("UTC: %v %v %v", ts, allDay, err)
	}
	// 全天
	ts, allDay, err = parseICalDateTime(mk("20260728", ical.ValueDate))
	if err != nil || !allDay || ts.Day() != 28 {
		t.Fatalf("DATE: %v %v %v", ts, allDay, err)
	}
	// 浮点（本地时区，中国 +8 → UTC 减 8 小时）
	ts, allDay, err = parseICalDateTime(mk("20260728T140000", ical.ValueDateTime))
	if err != nil || allDay {
		t.Fatalf("floating: %v %v %v", ts, allDay, err)
	}
	if ts.UTC().Hour() != 6 {
		t.Fatalf("floating 14:00 +0800 应转 UTC 06:00, got %v", ts.UTC())
	}
}

func TestParseICalDurationMinutes(t *testing.T) {
	cases := map[string]int{
		"-PT15M":   15,
		"-PT1H30M": 90,
		"-P1D":     1440,
		"-P1W":     10080,
		"-PT5M":    5,
	}
	for in, want := range cases {
		if got, ok := parseICalDurationMinutes(in); !ok || got != want {
			t.Errorf("%s = %d,%v want %d", in, got, ok, want)
		}
	}
	if _, ok := parseICalDurationMinutes("PT10M"); ok {
		t.Error("正向触发应返回 false")
	}
}

func TestAllDayEventCreate(t *testing.T) {
	var gotDraft eas.EventDraft
	mock := &easmock.Client{
		CalendarClient: easmock.CalendarClient{
			CreateEventFunc: func(_ context.Context, _ string, draft eas.EventDraft) (string, error) {
				gotDraft = draft
				return "cal:100", nil
			},
		},
	}
	b := newCalWriteBackend(t, mock)
	body := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:allday-1
SUMMARY:年假
DTSTART;VALUE=DATE:20260801
DTEND;VALUE=DATE:20260803
END:VEVENT
END:VCALENDAR`
	if _, err := b.PutCalendarObject(context.Background(),
		caldavCalendarPath+"allday-1.ics", buildICal(t, body), nil); err != nil {
		t.Fatal(err)
	}
	if !gotDraft.AllDayEvent {
		t.Fatal("应是全天事件")
	}
	if gotDraft.StartTime.Day() != 1 || gotDraft.EndTime.Day() != 3 {
		t.Fatalf("allday times = %v ~ %v", gotDraft.StartTime, gotDraft.EndTime)
	}
}
