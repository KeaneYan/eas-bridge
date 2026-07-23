// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/wbxml"
)

// RightsTemplate is one IRM (Information Rights Management) template
// the server reports as available for new outbound messages.
type RightsTemplate struct {
	TemplateID  string
	Name        string
	Description string
}

// GetRightsManagementTemplates lists the IRM templates the server
// supports. Returned via Settings/RightsManagementInformation/Get
// in MS-ASRM §3.1.5.1; supported on Exchange 2010+ when AD-RMS is
// configured.
//
// Templates are passed by ID to ItemOperations Move or Sync ApplicationData
// when sending IRM-protected messages (out of scope here; library users
// can build the WBXML directly using PageRightsManagement).
func (c *httpClient) GetRightsManagementTemplates(ctx context.Context) ([]RightsTemplate, error) {
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "RightsManagementInformation",
				wbxml.E(wbxml.PageSettings, "Get"),
			),
		),
	}
	resp, err := c.post(ctx, "Settings", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: GetRightsManagementTemplates: empty response")
	}
	if st := resp.Root.Find("Status"); st != nil {
		if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Settings/RightsManagementInformation/Get", Code: code}
		}
	}
	var out []RightsTemplate
	for _, tpl := range resp.Root.FindAll("RightsManagementTemplate") {
		t := RightsTemplate{}
		if id := tpl.Find("TemplateID"); id != nil {
			t.TemplateID = id.TextContent()
		}
		if n := tpl.Find("TemplateName"); n != nil {
			t.Name = n.TextContent()
		}
		if d := tpl.Find("TemplateDescription"); d != nil {
			t.Description = d.TextContent()
		}
		out = append(out, t)
	}
	return out, nil
}

// RightsLicense describes the IRM-imposed constraints on an item, as
// reported in the AirSyncBase Body / RightsManagementLicense element
// when an item is fetched with RightsManagementSupport=1.
type RightsLicense struct {
	Owner                            string
	ContentOwner                     string
	ContentExpiryDate                string // ISO 8601
	TemplateID                       string
	TemplateName                     string
	EditAllowed                      bool
	ReplyAllowed                     bool
	ReplyAllAllowed                  bool
	ForwardAllowed                   bool
	ModifyRecipientsAllowed          bool
	ExtractAllowed                   bool
	PrintAllowed                     bool
	ExportAllowed                    bool
	ProgrammaticAccessAllowed        bool
	RemoveRightsManagementProtection bool
}

// ParseRightsLicense extracts a RightsLicense from a
// <RightsManagementLicense> element (typically nested inside an item's
// Body). Exposed as a helper so callers that fetch IRM-protected items
// via Sync or ItemOperations can interpret the license metadata.
func ParseRightsLicense(license *wbxml.Element) RightsLicense {
	out := RightsLicense{}
	if license == nil {
		return out
	}
	get := func(name string) string {
		if e := license.Find(name); e != nil {
			return e.TextContent()
		}
		return ""
	}
	getBool := func(name string) bool {
		return get(name) == "1"
	}
	out.Owner = get("Owner")
	out.ContentOwner = get("ContentOwner")
	out.ContentExpiryDate = get("ContentExpiryDate")
	out.TemplateID = get("TemplateID")
	out.TemplateName = get("TemplateName")
	out.EditAllowed = getBool("EditAllowed")
	out.ReplyAllowed = getBool("ReplyAllowed")
	out.ReplyAllAllowed = getBool("ReplyAllAllowed")
	out.ForwardAllowed = getBool("ForwardAllowed")
	out.ModifyRecipientsAllowed = getBool("ModifyRecipientsAllowed")
	out.ExtractAllowed = getBool("ExtractAllowed")
	out.PrintAllowed = getBool("PrintAllowed")
	out.ExportAllowed = getBool("ExportAllowed")
	out.ProgrammaticAccessAllowed = getBool("ProgrammaticAccessAllowed")
	out.RemoveRightsManagementProtection = getBool("RemoveRightsManagementProtection")
	return out
}
