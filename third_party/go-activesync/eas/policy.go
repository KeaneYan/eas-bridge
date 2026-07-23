// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"github.com/hstern/go-activesync/wbxml"
)

// Policy is the parsed EASProvisionDoc the server returned during a
// Provision exchange. It captures every field defined by MS-ASPROV §2.2.2
// for protocol versions 12.x–14.x.
//
// Most fields describe constraints a real device would enforce
// (password rules, camera/storage/Bluetooth allowances, attachment
// limits). A library client can rarely enforce them directly; the
// Policy is exposed so callers can inspect what the server expects
// and refuse to operate when policy clashes with their security
// posture.
type Policy struct {
	// Password / lock policy.
	DevicePasswordEnabled              bool
	AlphanumericDevicePasswordRequired bool
	AllowSimpleDevicePassword          bool
	MinDevicePasswordLength            int
	MinDevicePasswordComplexCharacters int
	MaxDevicePasswordFailedAttempts    int
	MaxInactivityTimeDeviceLockSeconds int
	DevicePasswordExpirationDays       int
	DevicePasswordHistory              int
	RequireStorageCardEncryption       bool
	RequireDeviceEncryption            bool

	// Hardware feature toggles.
	AllowCamera           bool
	AllowStorageCard      bool
	AllowWiFi             bool
	AllowTextMessaging    bool
	AllowBluetooth        int // 0 disabled, 1 handsfree-only, 2 allowed
	AllowIrDA             bool
	AllowInternetSharing  bool
	AllowRemoteDesktop    bool
	AllowDesktopSync      bool
	AllowBrowser          bool
	AllowConsumerEmail    bool
	AllowPOPIMAPEmail     bool
	AllowUnsignedApps     bool
	AllowUnsignedInstall  bool
	RequireManualSyncRoam bool

	// Mail / calendar limits.
	AllowHTMLEmail                 bool
	MaxAttachmentSizeBytes         int
	MaxCalendarAgeFilter           int // EAS filter type
	MaxEmailAgeFilter              int
	MaxEmailBodyTruncationSize     int
	MaxEmailHTMLBodyTruncationSize int
	AttachmentsEnabled             bool

	// S/MIME.
	RequireSignedSMIMEMessages               bool
	RequireEncryptedSMIMEMessages            bool
	RequireSignedSMIMEAlgorithm              int
	RequireEncryptionSMIMEAlgorithm          int
	AllowSMIMEEncryptionAlgorithmNegotiation int
	AllowSMIMESoftCerts                      bool

	// Application allow/deny lists (parsed but rarely meaningful for us).
	UnapprovedInROMApplicationList []string
	ApprovedApplicationList        []ApprovedApplication

	// Hash is the policy revision hash; stable until the server changes
	// any field above. Useful for caching parsed policies across calls.
	Hash string
}

// ApprovedApplication is one entry in the policy's ApprovedApplicationList.
type ApprovedApplication struct {
	Name string
	Hash string
}

// parsePolicy walks an EASProvisionDoc element and returns a Policy.
func parsePolicy(doc *wbxml.Element) *Policy {
	if doc == nil {
		return nil
	}
	p := &Policy{}
	for _, c := range doc.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Codepage != wbxml.PageProvision {
			continue
		}
		v := el.TextContent()
		switch el.Name {
		case "DevicePasswordEnabled":
			p.DevicePasswordEnabled = v == "1"
		case "AlphanumericDevicePasswordRequired":
			p.AlphanumericDevicePasswordRequired = v == "1"
		case "AllowSimpleDevicePassword":
			p.AllowSimpleDevicePassword = v == "1"
		case "MinDevicePasswordLength":
			p.MinDevicePasswordLength = atoi(v)
		case "DevicePasswordExpiration":
			p.DevicePasswordExpirationDays = atoi(v)
		case "DevicePasswordHistory":
			p.DevicePasswordHistory = atoi(v)
		case "MaxInactivityTimeDeviceLock":
			p.MaxInactivityTimeDeviceLockSeconds = atoi(v)
		case "MaxDevicePasswordFailedAttempts":
			p.MaxDevicePasswordFailedAttempts = atoi(v)
		case "RequireDeviceEncryption":
			p.RequireDeviceEncryption = v == "1"
		case "AllowCamera":
			p.AllowCamera = v == "1"
		case "AllowStorageCard":
			p.AllowStorageCard = v == "1"
		case "AllowWiFi":
			p.AllowWiFi = v == "1"
		case "AllowTextMessaging":
			p.AllowTextMessaging = v == "1"
		case "AllowBluetooth":
			p.AllowBluetooth = atoi(v)
		case "AllowIrDA":
			p.AllowIrDA = v == "1"
		case "AllowInternetSharing":
			p.AllowInternetSharing = v == "1"
		case "AllowRemoteDesktop":
			p.AllowRemoteDesktop = v == "1"
		case "AllowDesktopSync":
			p.AllowDesktopSync = v == "1"
		case "AllowBrowser":
			p.AllowBrowser = v == "1"
		case "AllowConsumerEmail":
			p.AllowConsumerEmail = v == "1"
		case "AllowPOPIMAPEmail":
			p.AllowPOPIMAPEmail = v == "1"
		case "AllowUnsignedApplications":
			p.AllowUnsignedApps = v == "1"
		case "AllowUnsignedInstallationPackages":
			p.AllowUnsignedInstall = v == "1"
		case "RequireManualSyncWhenRoaming":
			p.RequireManualSyncRoam = v == "1"
		case "AllowHTMLEmail":
			p.AllowHTMLEmail = v == "1"
		case "MaxAttachmentSize":
			p.MaxAttachmentSizeBytes = atoi(v)
		case "MaxCalendarAgeFilter":
			p.MaxCalendarAgeFilter = atoi(v)
		case "MaxEmailAgeFilter":
			p.MaxEmailAgeFilter = atoi(v)
		case "MaxEmailBodyTruncationSize":
			p.MaxEmailBodyTruncationSize = atoi(v)
		case "MaxEmailHTMLBodyTruncationSize":
			p.MaxEmailHTMLBodyTruncationSize = atoi(v)
		case "AttachmentsEnabled":
			p.AttachmentsEnabled = v == "1"
		case "RequireSignedSMIMEMessages":
			p.RequireSignedSMIMEMessages = v == "1"
		case "RequireEncryptedSMIMEMessages":
			p.RequireEncryptedSMIMEMessages = v == "1"
		case "RequireSignedSMIMEAlgorithm":
			p.RequireSignedSMIMEAlgorithm = atoi(v)
		case "RequireEncryptionSMIMEAlgorithm":
			p.RequireEncryptionSMIMEAlgorithm = atoi(v)
		case "AllowSMIMEEncryptionAlgorithmNegotiation":
			p.AllowSMIMEEncryptionAlgorithmNegotiation = atoi(v)
		case "AllowSMIMESoftCerts":
			p.AllowSMIMESoftCerts = v == "1"
		case "UnapprovedInROMApplicationList":
			for _, sub := range el.Children {
				if se, ok := sub.(*wbxml.Element); ok && se.Name == "ApplicationName" {
					p.UnapprovedInROMApplicationList = append(p.UnapprovedInROMApplicationList, se.TextContent())
				}
			}
		case "ApprovedApplicationList":
			for _, sub := range el.Children {
				se, ok := sub.(*wbxml.Element)
				if !ok || se.Name != "Hash" {
					continue
				}
				app := ApprovedApplication{Hash: se.TextContent()}
				if name := se.Find("ApplicationName"); name != nil {
					app.Name = name.TextContent()
				}
				p.ApprovedApplicationList = append(p.ApprovedApplicationList, app)
			}
		case "Hash":
			p.Hash = v
		}
	}
	return p
}

// LastPolicy returns the most recently parsed Policy from a Provision
// exchange on this client, or nil if no policy has been received yet.
//
// The Policy is populated by Provision after a successful handshake.
// It is read-only; callers should treat the returned pointer as a
// snapshot.
func (c *httpClient) LastPolicy() *Policy {
	c.policyMu.Lock()
	defer c.policyMu.Unlock()
	return c.lastPolicy
}
