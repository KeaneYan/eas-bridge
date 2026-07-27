package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
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
