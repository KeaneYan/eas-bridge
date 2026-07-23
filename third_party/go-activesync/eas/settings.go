// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"

	"github.com/hstern/go-activesync/wbxml"
)

// DeviceInformation is the payload for Settings/DeviceInformation/Set.
//
// Per MS-ASPROV §3.1.5.1, in EAS 14.0+ the client SHOULD send this once
// (per device, per session) before issuing the initial Provision command.
// Strict Exchange servers will reject Provision otherwise; Z-Push and
// SOGo are lenient but accept the call as a no-op.
//
// All fields default to empty strings if unset. The library sends what
// the caller provides; callers typically set Model + FriendlyName at
// minimum so the server can identify the client in its admin tools.
type DeviceInformation struct {
	Model          string // device model name (e.g. "iPhone15,3")
	IMEI           string // device IMEI; rarely meaningful for non-phone clients
	FriendlyName   string // human-friendly device name
	OS             string // device operating system (e.g. "darwin/amd64")
	OSLanguage     string // ISO language tag
	PhoneNumber    string
	MobileOperator string
	UserAgent      string
	// EnableOutboundSMS reports whether the device can send SMS via the
	// EAS server's SMS bridge. False for non-phone clients.
	EnableOutboundSMS bool
}

// SettingsDeviceInformation sends a Settings command with a
// DeviceInformation/Set element.
//
// Returns nil on Status=1, a *StatusError on any other status, or an
// underlying transport error.
func (c *httpClient) SettingsDeviceInformation(ctx context.Context, info DeviceInformation) error {
	set := wbxml.E(wbxml.PageSettings, "Set")
	add := func(name, val string) {
		if val == "" {
			return
		}
		set.Children = append(set.Children, wbxml.E(wbxml.PageSettings, name, wbxml.Text(val)))
	}
	add("Model", info.Model)
	add("IMEI", info.IMEI)
	add("FriendlyName", info.FriendlyName)
	add("OS", info.OS)
	add("OSLanguage", info.OSLanguage)
	add("PhoneNumber", info.PhoneNumber)
	add("MobileOperator", info.MobileOperator)
	add("UserAgent", info.UserAgent)
	if info.EnableOutboundSMS {
		set.Children = append(set.Children, wbxml.E(wbxml.PageSettings, "EnableOutboundSMS", wbxml.Text("1")))
	} else {
		set.Children = append(set.Children, wbxml.E(wbxml.PageSettings, "EnableOutboundSMS", wbxml.Text("0")))
	}

	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "DeviceInformation", set),
		),
	}
	resp, err := c.post(ctx, "Settings", doc)
	if err != nil {
		return err
	}
	if resp == nil || resp.Root == nil {
		// Empty 200 OK is acceptable.
		return nil
	}
	// Top-level Status is always present per MS-ASCMD §2.2.4.124.
	if st := resp.Root.Find("Status"); st != nil {
		if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
			return &StatusError{Command: "Settings", Code: code}
		}
	}
	// DeviceInformation has its own nested Status.
	if di := resp.Root.Find("DeviceInformation"); di != nil {
		if st := di.Find("Status"); st != nil {
			if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
				return &StatusError{Command: "Settings/DeviceInformation", Code: code}
			}
		}
	}
	return nil
}
