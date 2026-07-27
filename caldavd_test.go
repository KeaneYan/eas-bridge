package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
)

func TestLogCalDAVFailuresPreservesStatusAndBody(t *testing.T) {
	handler := logCalDAVFailures(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unsupported", http.StatusNotImplemented)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("PROPPATCH", "/user/calendars/default/", nil))

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
	if rec.Body.String() != "unsupported\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestCalDAVPropertyNames(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
		<d:propertyupdate xmlns:d="DAV:" xmlns:a="http://apple.com/ns/ical/">
			<d:set><d:prop><a:calendar-color>#123456FF</a:calendar-color></d:prop></d:set>
		</d:propertyupdate>`)
	got := calDAVPropertyNames(body)
	want := []string{"{http://apple.com/ns/ical/}calendar-color"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("properties = %v, want %v", got, want)
	}
}

func TestHandleAppleCalendarProperties(t *testing.T) {
	nextCalled := false
	handler := handleAppleCalendarProperties(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	body := `<d:propertyupdate xmlns:d="DAV:" xmlns:a="http://apple.com/ns/ical/">
		<d:set><d:prop><a:calendar-color>#123456FF</a:calendar-color><a:calendar-order>1</a:calendar-order></d:prop></d:set>
	</d:propertyupdate>`
	req := httptest.NewRequest("PROPPATCH", caldavCalendarPath, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if nextCalled {
		t.Fatal("supported Apple display properties should not reach the CalDAV handler")
	}
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMultiStatus)
	}
	if !strings.Contains(rec.Body.String(), "<a:calendar-color/>") ||
		!strings.Contains(rec.Body.String(), "<a:calendar-order/>") ||
		!strings.Contains(rec.Body.String(), "HTTP/1.1 200 OK") {
		t.Fatalf("unexpected multistatus response: %s", rec.Body.String())
	}
}

func TestHandleAppleCalendarPropertiesPassesUnknownProperty(t *testing.T) {
	body := `<d:propertyupdate xmlns:d="DAV:"><d:set><d:prop><d:displayname>Work</d:displayname></d:prop></d:set></d:propertyupdate>`
	var forwarded string
	handler := handleAppleCalendarProperties(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		forwarded = string(data)
	}))
	req := httptest.NewRequest("PROPPATCH", caldavCalendarPath, strings.NewReader(body))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if forwarded != body {
		t.Fatalf("forwarded body changed: %q", forwarded)
	}
}

func TestCalendarPollerSyncsEveryIntervalAndStops(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}

	synced := make(chan struct{}, 2)
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(context.Context, string, eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					select {
					case synced <- struct{}{}:
					default:
					}
					return &eas.CalendarSyncResult{}, nil
				},
			},
		},
	}
	backend := &caldavBackend{engine: engine}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		backend.calendarPoller(ctx, 20*time.Millisecond)
		close(done)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-synced:
		case <-time.After(2 * time.Second):
			t.Fatalf("日历轮询器只触发了 %d 次同步，期望连续两个间隔均触发", i)
		}
	}

	deadline := time.After(2 * time.Second)
	for {
		backend.calMu.Lock()
		syncFinished := !backend.lastCalSync.IsZero()
		backend.calMu.Unlock()
		if syncFinished {
			break
		}
		select {
		case <-deadline:
			t.Fatal("成功的定时同步未更新 lastCalSync")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("日历轮询器未在 cancel 后退出")
	}
}

func TestCalendarReadWaitsForInFlightSyncWhenCacheIsEmpty(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	started := make(chan struct{})
	release := make(chan struct{})
	event := eas.EventItem{ServerID: "Event:1", UID: "uid-1", Subject: "loaded"}
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(context.Context, string, eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					close(started)
					<-release
					return &eas.CalendarSyncResult{Added: []eas.EventItem{event}}, nil
				},
			},
		},
	}
	backend := &caldavBackend{engine: engine}

	prewarmDone := make(chan error, 1)
	go func() {
		prewarmDone <- backend.syncCalendar(context.Background())
	}()
	<-started

	readDone := make(chan error, 1)
	go func() {
		readDone <- backend.maybeSyncCalendar(context.Background())
	}()
	select {
	case err := <-readDone:
		t.Fatalf("空缓存查询在预热完成前返回: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-prewarmDone; err != nil {
		t.Fatal(err)
	}
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Events[event.ServerID]; !ok {
		t.Fatal("等待预热完成后仍未加载日历事件")
	}
}

func TestCalendarReadReturns503WhenInitialSyncFails(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(context.Context, string, eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					return nil, errors.New("temporary EAS outage")
				},
			},
		},
	}
	backend := &caldavBackend{engine: engine}

	_, err := backend.ListCalendarObjects(context.Background(), caldavCalendarPath, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "503 Service Unavailable") {
		t.Fatalf("initial sync error = %v, want 503 Service Unavailable", err)
	}
}

func TestCalendarBackgroundRefreshStopsWithLifecycle(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	st.Events["cached"] = eas.EventItem{ServerID: "cached", Subject: "cached"}
	started := make(chan struct{})
	stopped := make(chan struct{})
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(ctx context.Context, _ string, _ eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					close(started)
					<-ctx.Done()
					close(stopped)
					return nil, ctx.Err()
				},
			},
		},
	}
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	backend := &caldavBackend{engine: engine, lifecycleCtx: lifecycleCtx}

	if err := backend.maybeSyncCalendar(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh did not stop after lifecycle cancellation")
	}
}

func TestEmptyCalendarWithPersistedSyncKeyIsUsableCache(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	st.SyncKeys["calendar"] = "synced-empty-calendar"
	backend := &caldavBackend{
		engine:      &syncEngine{st: st},
		lastCalSync: time.Now(),
	}

	objects, err := backend.ListCalendarObjects(context.Background(), caldavCalendarPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("empty synced calendar returned %d objects", len(objects))
	}
}
