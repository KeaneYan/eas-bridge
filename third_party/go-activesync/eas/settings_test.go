// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestSettingsDeviceInformation_request(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Empty Settings response with Status=1.
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "DeviceInformation",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})

	err := c.SettingsDeviceInformation(context.Background(), DeviceInformation{
		Model:        "GoTestModel",
		FriendlyName: "go-activesync test",
		OS:           "darwin",
		UserAgent:    "go-activesync/0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.url.Query().Get("Cmd") != "Settings" {
		t.Errorf("Cmd: %q", cap.url.Query().Get("Cmd"))
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	di := req.Root.Find("DeviceInformation")
	if di == nil {
		t.Fatal("DeviceInformation missing")
	}
	set := di.Find("Set")
	if set == nil {
		t.Fatal("Set missing")
	}
	if m := set.Find("Model"); m == nil || m.TextContent() != "GoTestModel" {
		t.Errorf("Model = %v", m)
	}
	if ua := set.Find("UserAgent"); ua == nil || ua.TextContent() != "go-activesync/0.1" {
		t.Errorf("UserAgent = %v", ua)
	}
	if sms := set.Find("EnableOutboundSMS"); sms == nil || sms.TextContent() != "0" {
		t.Errorf("EnableOutboundSMS default = %v", sms)
	}
}

func TestSettingsDeviceInformation_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("110")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	err := c.SettingsDeviceInformation(context.Background(), DeviceInformation{Model: "x"})
	if !IsStatusCode(err, 110) {
		t.Errorf("err = %v", err)
	}
}

func TestSettingsDeviceInformation_emptyResponseOK(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty 200 OK
	})
	if err := c.SettingsDeviceInformation(context.Background(), DeviceInformation{Model: "x"}); err != nil {
		t.Errorf("empty 200 OK should be success, got %v", err)
	}
}

func TestSettingsDeviceInformation_emitsOutboundSMSTrue(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	_ = c.SettingsDeviceInformation(context.Background(), DeviceInformation{
		Model:             "M",
		EnableOutboundSMS: true,
	})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if sms := req.Root.Find("EnableOutboundSMS"); sms == nil || sms.TextContent() != "1" {
		t.Errorf("EnableOutboundSMS = %v, want '1'", sms)
	}
}

func TestSettingsDeviceInformation_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	if err := c.SettingsDeviceInformation(context.Background(), DeviceInformation{Model: "x"}); err == nil {
		t.Error("want error from transport")
	}
}

func TestSettingsDeviceInformation_nestedStatusError(t *testing.T) {
	// Top-level Status=1 (success) but DeviceInformation/Status non-1.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "DeviceInformation",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("4")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	err := c.SettingsDeviceInformation(context.Background(), DeviceInformation{Model: "x"})
	if !IsStatusCode(err, 4) {
		t.Errorf("err = %v, want StatusError code 4", err)
	}
}

func TestSettingsDeviceInformation_omitsEmptyFields(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	_ = c.SettingsDeviceInformation(context.Background(), DeviceInformation{Model: "M"})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	set := req.Root.Find("Set")
	if set.Find("OS") != nil {
		t.Error("OS should be omitted when empty")
	}
	if set.Find("PhoneNumber") != nil {
		t.Error("PhoneNumber should be omitted when empty")
	}
}
