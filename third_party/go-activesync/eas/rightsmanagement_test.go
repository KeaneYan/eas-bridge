// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

func TestGetRightsManagementTemplates_parsesList(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "RightsManagementInformation",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageSettings, "Get",
						wbxml.E(wbxml.PageRightsManagement, "RightsManagementTemplates",
							wbxml.E(wbxml.PageRightsManagement, "RightsManagementTemplate",
								wbxml.E(wbxml.PageRightsManagement, "TemplateID", wbxml.Text("tmpl-do-not-forward")),
								wbxml.E(wbxml.PageRightsManagement, "TemplateName", wbxml.Text("Do Not Forward")),
								wbxml.E(wbxml.PageRightsManagement, "TemplateDescription", wbxml.Text("Recipients can read but not forward.")),
							),
							wbxml.E(wbxml.PageRightsManagement, "RightsManagementTemplate",
								wbxml.E(wbxml.PageRightsManagement, "TemplateID", wbxml.Text("tmpl-confidential")),
								wbxml.E(wbxml.PageRightsManagement, "TemplateName", wbxml.Text("Confidential")),
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
	tpls, err := c.GetRightsManagementTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tpls) != 2 {
		t.Fatalf("got %d templates", len(tpls))
	}
	if tpls[0].TemplateID != "tmpl-do-not-forward" || tpls[0].Name != "Do Not Forward" {
		t.Errorf("template 0 = %+v", tpls[0])
	}
	if tpls[1].TemplateID != "tmpl-confidential" || tpls[1].Description != "" {
		t.Errorf("template 1 = %+v", tpls[1])
	}
}

func TestGetRightsManagementTemplates_serverError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("3")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.GetRightsManagementTemplates(context.Background()); err == nil {
		t.Fatal("expected status error")
	}
}

func TestParseRightsLicense_allFields(t *testing.T) {
	lic := wbxml.E(wbxml.PageRightsManagement, "RightsManagementLicense",
		wbxml.E(wbxml.PageRightsManagement, "Owner", wbxml.Text("alice@x")),
		wbxml.E(wbxml.PageRightsManagement, "ContentOwner", wbxml.Text("alice@x")),
		wbxml.E(wbxml.PageRightsManagement, "ContentExpiryDate", wbxml.Text("2030-01-01T00:00:00Z")),
		wbxml.E(wbxml.PageRightsManagement, "TemplateID", wbxml.Text("tmpl-x")),
		wbxml.E(wbxml.PageRightsManagement, "TemplateName", wbxml.Text("Custom")),
		wbxml.E(wbxml.PageRightsManagement, "EditAllowed", wbxml.Text("1")),
		wbxml.E(wbxml.PageRightsManagement, "ReplyAllowed", wbxml.Text("1")),
		wbxml.E(wbxml.PageRightsManagement, "ReplyAllAllowed", wbxml.Text("0")),
		wbxml.E(wbxml.PageRightsManagement, "ForwardAllowed", wbxml.Text("0")),
		wbxml.E(wbxml.PageRightsManagement, "ModifyRecipientsAllowed", wbxml.Text("1")),
		wbxml.E(wbxml.PageRightsManagement, "ExtractAllowed", wbxml.Text("0")),
		wbxml.E(wbxml.PageRightsManagement, "PrintAllowed", wbxml.Text("1")),
		wbxml.E(wbxml.PageRightsManagement, "ExportAllowed", wbxml.Text("0")),
		wbxml.E(wbxml.PageRightsManagement, "ProgrammaticAccessAllowed", wbxml.Text("1")),
		wbxml.E(wbxml.PageRightsManagement, "RemoveRightsManagementProtection", wbxml.Text("0")),
	)
	got := ParseRightsLicense(lic)
	if got.Owner != "alice@x" || got.TemplateName != "Custom" || got.ContentExpiryDate != "2030-01-01T00:00:00Z" {
		t.Errorf("string fields = %+v", got)
	}
	if !got.EditAllowed || !got.ReplyAllowed || got.ReplyAllAllowed || got.ForwardAllowed {
		t.Errorf("permission flags = %+v", got)
	}
	if !got.ModifyRecipientsAllowed || got.ExtractAllowed || !got.PrintAllowed {
		t.Errorf("permission flags 2 = %+v", got)
	}
	if got.ExportAllowed || !got.ProgrammaticAccessAllowed || got.RemoveRightsManagementProtection {
		t.Errorf("permission flags 3 = %+v", got)
	}
}

func TestGetRightsManagementTemplates_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.GetRightsManagementTemplates(context.Background()); err == nil {
		t.Error("want HTTP error")
	}
}

func TestGetRightsManagementTemplates_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → nil resp
	})
	if _, err := c.GetRightsManagementTemplates(context.Background()); err == nil {
		t.Error("want error on empty response")
	}
}

func TestParseRightsLicense_missingFieldsReturnEmpty(t *testing.T) {
	// License element with only Owner; helper's get() must return "" for
	// fields the server didn't include, not panic.
	lic := wbxml.E(wbxml.PageRightsManagement, "RightsManagementLicense",
		wbxml.E(wbxml.PageRightsManagement, "Owner", wbxml.Text("alice@x")),
	)
	got := ParseRightsLicense(lic)
	if got.Owner != "alice@x" {
		t.Errorf("Owner = %q", got.Owner)
	}
	if got.TemplateID != "" || got.ContentExpiryDate != "" || got.EditAllowed {
		t.Errorf("missing fields not zero: %+v", got)
	}
}

func TestParseRightsLicense_nilSafe(t *testing.T) {
	got := ParseRightsLicense(nil)
	if got != (RightsLicense{}) {
		t.Errorf("nil input not safe: %+v", got)
	}
}
