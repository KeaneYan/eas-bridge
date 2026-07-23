// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
)

// commandTokens maps EAS command names to the single-byte command codes
// used by the base64-encoded query format ([MS-ASHTTP] §2.2.1.1.2).
//
// Order matters: the value is what goes on the wire as the
// "command code" byte after the protocol version.
var commandTokens = map[string]byte{
	"Sync":              0,
	"SendMail":          1,
	"SmartForward":      2,
	"SmartReply":        3,
	"GetAttachment":     4,
	"FolderSync":        9,
	"FolderCreate":      10,
	"FolderDelete":      11,
	"FolderUpdate":      12,
	"MoveItems":         13,
	"GetItemEstimate":   14,
	"MeetingResponse":   15,
	"Search":            16,
	"Settings":          17,
	"Ping":              18,
	"ItemOperations":    19,
	"Provision":         20,
	"ResolveRecipients": 21,
	"ValidateCert":      22,
	"Find":              23,
}

// commandToken returns the single-byte command code for cmd, or an
// error if cmd is unknown to the base64 encoding.
func commandToken(cmd string) (byte, error) {
	tok, ok := commandTokens[cmd]
	if !ok {
		return 0, errors.New("unknown command for base64 URL encoding: " + cmd)
	}
	return tok, nil
}

// buildBase64Query packs the request parameters into a single
// base64-encoded query value. The wire layout is:
//
//	1 byte:  protocol version (e.g. 141 = "14.1")
//	1 byte:  command code
//	2 bytes: locale (little-endian; 0 if unset)
//	1 byte:  device id length, then device id bytes
//	1 byte:  device type length, then device type bytes
//	1 byte:  policy key length (always 0 for our use), then key bytes
//	1 byte:  command parameters length (always 0 for our use), then params
//
// The result is base64-URL-safe and stripped of "=" padding so it
// embeds cleanly in a query string.
func buildBase64Query(asVersion, cmd, deviceID, deviceType, policyKey string) (string, error) {
	cmdByte, err := commandToken(cmd)
	if err != nil {
		return "", err
	}
	if len(deviceID) > 255 || len(deviceType) > 255 || len(policyKey) > 255 {
		return "", errors.New("base64URL: deviceID/deviceType/policyKey > 255 bytes")
	}

	var buf []byte
	buf = append(buf, encodeVersion(asVersion))
	buf = append(buf, cmdByte)
	buf = append(buf, 0, 0) // locale = unset
	buf = append(buf, byte(len(deviceID)))
	buf = append(buf, deviceID...)
	buf = append(buf, byte(len(deviceType)))
	buf = append(buf, deviceType...)
	buf = append(buf, byte(len(policyKey)))
	buf = append(buf, policyKey...)
	buf = append(buf, 0) // no command parameters

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// encodeVersion converts an EAS version string ("14.1") to the single
// byte the base64 encoding uses (141). Falls back to 141 for any
// unparseable input — that's the modern default.
func encodeVersion(s string) byte {
	switch s {
	case "12.0":
		return 120
	case "12.1":
		return 121
	case "14.0":
		return 140
	case "14.1":
		return 141
	case "16.0":
		return 160
	case "16.1":
		return 161
	}
	// Best-effort: strip the dot, parse as int.
	v := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			v = v*10 + int(s[i]-'0')
		}
	}
	if v > 0 && v <= 255 {
		return byte(v)
	}
	return 141
}

// applyB64ToURL replaces the standard query string in u with a single
// base64-encoded query value when Base64URL is enabled.
func (c *httpClient) applyB64ToURL(cmd, plainURL string) (string, error) {
	pk, err := c.cfg.State.PolicyKey(context.Background())
	if err != nil {
		return "", err
	}
	encoded, err := buildBase64Query(c.cfg.ASVersion, cmd, c.cfg.DeviceID, c.cfg.DeviceType, pk)
	if err != nil {
		return "", err
	}
	// Drop any existing query and append our single param.
	if i := strings.IndexByte(plainURL, '?'); i >= 0 {
		plainURL = plainURL[:i]
	}
	return plainURL + "?" + encoded, nil
}
