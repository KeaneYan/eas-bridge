// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestHTTPError_message(t *testing.T) {
	e := &HTTPError{StatusCode: 449, Status: "449 Retry After Provisioning", URL: "https://x", Body: []byte("oops")}
	if !strings.Contains(e.Error(), "449") {
		t.Errorf("err = %q", e.Error())
	}
	if !strings.Contains(e.Error(), "https://x") {
		t.Errorf("err = %q", e.Error())
	}
}

func TestIsHTTPStatus(t *testing.T) {
	e := &HTTPError{StatusCode: 401}
	if !IsHTTPStatus(e, 401) {
		t.Error("direct match")
	}
	if IsHTTPStatus(e, 200) {
		t.Error("wrong code matched")
	}
	wrapped := fmt.Errorf("wrap: %w", e)
	if !IsHTTPStatus(wrapped, 401) {
		t.Error("wrapped match")
	}
	if IsHTTPStatus(errors.New("plain"), 401) {
		t.Error("plain error matched")
	}
}

func TestStatusError_message(t *testing.T) {
	e := &StatusError{Command: "FolderSync", Code: 3}
	if !strings.Contains(e.Error(), "InvalidSyncKey") {
		t.Errorf("err = %q", e.Error())
	}

	// Unknown code: no name, just number.
	e2 := &StatusError{Command: "X", Code: 9999}
	if strings.Contains(e2.Error(), "(") {
		t.Errorf("unexpected paren: %q", e2.Error())
	}
}

func TestStatusName_allMapped(t *testing.T) {
	cases := map[int]string{
		1: "OK", 2: "ProtocolError", 3: "InvalidSyncKey", 4: "ProtocolError",
		5: "ServerError", 6: "ConversionError", 7: "ConflictMatchingClientServer",
		8: "ObjectNotFound", 9: "OutOfSpace", 12: "ServerError", 13: "ServerError",
		14: "InvalidArguments", 101: "InvalidContent", 102: "InvalidWBXML",
		103: "InvalidXML", 110: "ServerError", 141: "DeviceNotProvisioned",
		142: "PolicyRefresh", 143: "InvalidPolicyKey",
		144: "ExternallyManagedDevicesNotAllowed", 145: "NoRecurrenceInCalendar",
		177: "ItemNotFoundInCollection",
		// Unknown code: empty so the StatusError formatter falls back to just the number.
		9999: "",
	}
	for code, want := range cases {
		if got := statusName(code); got != want {
			t.Errorf("statusName(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestIsStatusCode(t *testing.T) {
	e := &StatusError{Code: 143}
	if !IsStatusCode(e, 143) {
		t.Error("direct")
	}
	w := fmt.Errorf("wrap: %w", e)
	if !IsStatusCode(w, 143) {
		t.Error("wrapped")
	}
	if IsStatusCode(e, 1) {
		t.Error("wrong code matched")
	}
}
