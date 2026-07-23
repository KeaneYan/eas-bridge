// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

// EAS status codes that show up across the protocol-common path. Per-command
// codes (Provision, FolderSync, Sync) overlap heavily, and statusName returns
// the most generally accurate description.
//
// MS-ASCMD §2.2.4 enumerates codes per command; we only name the ones we
// actually inspect or surface to users.
const (
	StatusOK             int = 1
	StatusInvalidContent int = 101
	StatusInvalidWBXML   int = 102
	StatusServerError    int = 110
	// FolderSync / Sync
	StatusInvalidSyncKey int = 3
	// Provision
	StatusPolicyAcknowledged int = 1 // same numeric value as OK
)

// statusName maps a raw EAS status to a short label. Returns "" for unknown
// codes so the StatusError formatter can fall back to just the number.
func statusName(code int) string {
	switch code {
	case 1:
		return "OK"
	case 2:
		return "ProtocolError"
	case 3:
		return "InvalidSyncKey"
	case 4:
		return "ProtocolError"
	case 5:
		return "ServerError"
	case 6:
		return "ConversionError"
	case 7:
		return "ConflictMatchingClientServer"
	case 8:
		return "ObjectNotFound"
	case 9:
		return "OutOfSpace"
	case 12:
		return "ServerError"
	case 13:
		return "ServerError"
	case 14:
		return "InvalidArguments"
	case 101:
		return "InvalidContent"
	case 102:
		return "InvalidWBXML"
	case 103:
		return "InvalidXML"
	case 110:
		return "ServerError"
	case 141:
		return "DeviceNotProvisioned"
	case 142:
		return "PolicyRefresh"
	case 143:
		return "InvalidPolicyKey"
	case 144:
		return "ExternallyManagedDevicesNotAllowed"
	case 145:
		return "NoRecurrenceInCalendar"
	case 177:
		return "ItemNotFoundInCollection"
	default:
		return ""
	}
}
