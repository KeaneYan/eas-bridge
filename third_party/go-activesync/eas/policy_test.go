// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestProvision_capturesPolicyFields(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Build a Provision response with a real EASProvisionDoc.
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageProvision, "Provision",
				wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageProvision, "Policies",
					wbxml.E(wbxml.PageProvision, "Policy",
						wbxml.E(wbxml.PageProvision, "PolicyType", wbxml.Text("MS-EAS-Provisioning-WBXML")),
						wbxml.E(wbxml.PageProvision, "Status", wbxml.Text("1")),
						wbxml.E(wbxml.PageProvision, "PolicyKey", wbxml.Text("PK1")),
						wbxml.E(wbxml.PageProvision, "Data",
							wbxml.E(wbxml.PageProvision, "EASProvisionDoc",
								wbxml.E(wbxml.PageProvision, "DevicePasswordEnabled", wbxml.Text("1")),
								wbxml.E(wbxml.PageProvision, "MinDevicePasswordLength", wbxml.Text("8")),
								wbxml.E(wbxml.PageProvision, "RequireDeviceEncryption", wbxml.Text("1")),
								wbxml.E(wbxml.PageProvision, "AllowCamera", wbxml.Text("0")),
								wbxml.E(wbxml.PageProvision, "MaxAttachmentSize", wbxml.Text("10485760")),
								wbxml.E(wbxml.PageProvision, "AllowBluetooth", wbxml.Text("2")),
								wbxml.E(wbxml.PageProvision, "Hash", wbxml.Text("policy-hash-1")),
							),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})

	// Phase 1 alone is enough to populate the policy.
	if _, _, err := c.provisionPhase(context.Background(), "0", false); err != nil {
		t.Fatal(err)
	}
	p := c.LastPolicy()
	if p == nil {
		t.Fatal("policy not captured")
	}
	if !p.DevicePasswordEnabled {
		t.Errorf("DevicePasswordEnabled = false")
	}
	if p.MinDevicePasswordLength != 8 {
		t.Errorf("MinDevicePasswordLength = %d", p.MinDevicePasswordLength)
	}
	if !p.RequireDeviceEncryption {
		t.Errorf("RequireDeviceEncryption = false")
	}
	if p.AllowCamera {
		t.Errorf("AllowCamera should be false")
	}
	if p.MaxAttachmentSizeBytes != 10485760 {
		t.Errorf("MaxAttachmentSize = %d", p.MaxAttachmentSizeBytes)
	}
	if p.AllowBluetooth != 2 {
		t.Errorf("AllowBluetooth = %d", p.AllowBluetooth)
	}
	if p.Hash != "policy-hash-1" {
		t.Errorf("Hash = %q", p.Hash)
	}
}

func TestParsePolicy_nilDoc(t *testing.T) {
	if got := parsePolicy(nil); got != nil {
		t.Errorf("parsePolicy(nil) = %v, want nil", got)
	}
}

// TestParsePolicy_allFields exercises every branch of parsePolicy with a
// densely-populated EASProvisionDoc so the per-field decode paths are
// covered. Skipping any branch silently degrades the fidelity of the
// snapshot the LastPolicy() caller sees, which is otherwise hard to
// catch in an integration test against a permissive server.
func TestParsePolicy_allFields(t *testing.T) {
	doc := wbxml.E(wbxml.PageProvision, "EASProvisionDoc",
		wbxml.E(wbxml.PageProvision, "DevicePasswordEnabled", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AlphanumericDevicePasswordRequired", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowSimpleDevicePassword", wbxml.Text("0")),
		wbxml.E(wbxml.PageProvision, "MinDevicePasswordLength", wbxml.Text("8")),
		wbxml.E(wbxml.PageProvision, "DevicePasswordExpiration", wbxml.Text("90")),
		wbxml.E(wbxml.PageProvision, "DevicePasswordHistory", wbxml.Text("5")),
		wbxml.E(wbxml.PageProvision, "MaxInactivityTimeDeviceLock", wbxml.Text("300")),
		wbxml.E(wbxml.PageProvision, "MaxDevicePasswordFailedAttempts", wbxml.Text("10")),
		wbxml.E(wbxml.PageProvision, "RequireDeviceEncryption", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowCamera", wbxml.Text("0")),
		wbxml.E(wbxml.PageProvision, "AllowStorageCard", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowWiFi", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowTextMessaging", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowBluetooth", wbxml.Text("2")),
		wbxml.E(wbxml.PageProvision, "AllowIrDA", wbxml.Text("0")),
		wbxml.E(wbxml.PageProvision, "AllowInternetSharing", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowRemoteDesktop", wbxml.Text("0")),
		wbxml.E(wbxml.PageProvision, "AllowDesktopSync", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowBrowser", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowConsumerEmail", wbxml.Text("0")),
		wbxml.E(wbxml.PageProvision, "AllowPOPIMAPEmail", wbxml.Text("0")),
		wbxml.E(wbxml.PageProvision, "AllowUnsignedApplications", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowUnsignedInstallationPackages", wbxml.Text("0")),
		wbxml.E(wbxml.PageProvision, "RequireManualSyncWhenRoaming", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowHTMLEmail", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "MaxAttachmentSize", wbxml.Text("5242880")),
		wbxml.E(wbxml.PageProvision, "MaxCalendarAgeFilter", wbxml.Text("4")),
		wbxml.E(wbxml.PageProvision, "MaxEmailAgeFilter", wbxml.Text("3")),
		wbxml.E(wbxml.PageProvision, "MaxEmailBodyTruncationSize", wbxml.Text("32768")),
		wbxml.E(wbxml.PageProvision, "MaxEmailHTMLBodyTruncationSize", wbxml.Text("65536")),
		wbxml.E(wbxml.PageProvision, "AttachmentsEnabled", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "RequireSignedSMIMEMessages", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "RequireEncryptedSMIMEMessages", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "RequireSignedSMIMEAlgorithm", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "RequireEncryptionSMIMEAlgorithm", wbxml.Text("2")),
		wbxml.E(wbxml.PageProvision, "AllowSMIMEEncryptionAlgorithmNegotiation", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "AllowSMIMESoftCerts", wbxml.Text("1")),
		wbxml.E(wbxml.PageProvision, "UnapprovedInROMApplicationList",
			wbxml.E(wbxml.PageProvision, "ApplicationName", wbxml.Text("BadApp1")),
			wbxml.E(wbxml.PageProvision, "ApplicationName", wbxml.Text("BadApp2")),
		),
		wbxml.E(wbxml.PageProvision, "ApprovedApplicationList",
			wbxml.E(wbxml.PageProvision, "Hash",
				wbxml.E(wbxml.PageProvision, "ApplicationName", wbxml.Text("GoodApp")),
				wbxml.Text("DEADBEEF"),
			),
		),
		wbxml.E(wbxml.PageProvision, "Hash", wbxml.Text("policyhash")),
		// Wrong codepage: should be silently skipped.
		wbxml.E(wbxml.PageAirSync, "Class", wbxml.Text("ignored")),
	)
	p := parsePolicy(doc)
	if p == nil {
		t.Fatal("parsePolicy returned nil")
	}
	if !p.DevicePasswordEnabled || !p.AlphanumericDevicePasswordRequired || p.AllowSimpleDevicePassword {
		t.Errorf("password bools = %+v", p)
	}
	if p.MinDevicePasswordLength != 8 || p.DevicePasswordExpirationDays != 90 ||
		p.DevicePasswordHistory != 5 || p.MaxInactivityTimeDeviceLockSeconds != 300 ||
		p.MaxDevicePasswordFailedAttempts != 10 {
		t.Errorf("password ints = %+v", p)
	}
	if !p.RequireDeviceEncryption || p.AllowCamera || !p.AllowStorageCard ||
		!p.AllowWiFi || !p.AllowTextMessaging || p.AllowBluetooth != 2 ||
		p.AllowIrDA || !p.AllowInternetSharing || p.AllowRemoteDesktop ||
		!p.AllowDesktopSync || !p.AllowBrowser || p.AllowConsumerEmail || p.AllowPOPIMAPEmail {
		t.Errorf("device-feature flags = %+v", p)
	}
	if !p.AllowUnsignedApps || p.AllowUnsignedInstall || !p.RequireManualSyncRoam ||
		!p.AllowHTMLEmail || p.MaxAttachmentSizeBytes != 5242880 ||
		p.MaxCalendarAgeFilter != 4 || p.MaxEmailAgeFilter != 3 ||
		p.MaxEmailBodyTruncationSize != 32768 || p.MaxEmailHTMLBodyTruncationSize != 65536 {
		t.Errorf("misc fields = %+v", p)
	}
	if !p.AttachmentsEnabled || !p.RequireSignedSMIMEMessages || !p.RequireEncryptedSMIMEMessages ||
		p.RequireSignedSMIMEAlgorithm != 1 || p.RequireEncryptionSMIMEAlgorithm != 2 ||
		p.AllowSMIMEEncryptionAlgorithmNegotiation != 1 || !p.AllowSMIMESoftCerts {
		t.Errorf("smime fields = %+v", p)
	}
	if len(p.UnapprovedInROMApplicationList) != 2 ||
		p.UnapprovedInROMApplicationList[0] != "BadApp1" {
		t.Errorf("unapproved = %v", p.UnapprovedInROMApplicationList)
	}
	if len(p.ApprovedApplicationList) != 1 ||
		p.ApprovedApplicationList[0].Name != "GoodApp" ||
		p.ApprovedApplicationList[0].Hash != "DEADBEEF" {
		t.Errorf("approved = %+v", p.ApprovedApplicationList)
	}
	if p.Hash != "policyhash" {
		t.Errorf("Hash = %q", p.Hash)
	}
}

func TestParsePolicy_skipsNonHashChildrenInApprovedList(t *testing.T) {
	// Spec only allows <Hash> children inside <ApprovedApplicationList>; a
	// rogue <ApplicationName> sibling must be ignored, not parsed as a hash.
	doc := wbxml.E(wbxml.PageProvision, "EASProvisionDoc",
		wbxml.E(wbxml.PageProvision, "ApprovedApplicationList",
			wbxml.E(wbxml.PageProvision, "ApplicationName", wbxml.Text("Stray")),
			wbxml.E(wbxml.PageProvision, "Hash",
				wbxml.E(wbxml.PageProvision, "ApplicationName", wbxml.Text("Real")),
				wbxml.Text("HASHBYTES"),
			),
		),
	)
	p := parsePolicy(doc)
	if p == nil {
		t.Fatal("parsePolicy returned nil")
	}
	if len(p.ApprovedApplicationList) != 1 || p.ApprovedApplicationList[0].Name != "Real" {
		t.Errorf("approved = %+v (stray <ApplicationName> leaked through)", p.ApprovedApplicationList)
	}
}

func TestLastPolicy_nilByDefault(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	if p := c.LastPolicy(); p != nil {
		t.Errorf("LastPolicy = %+v, want nil", p)
	}
}
