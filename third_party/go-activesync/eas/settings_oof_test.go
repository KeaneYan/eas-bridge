// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

func TestGetOof_parsesConfig(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Oof",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageSettings, "Get",
						wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text("1")),
						wbxml.E(wbxml.PageSettings, "OofMessage",
							wbxml.E(wbxml.PageSettings, "AppliesToInternal"),
							wbxml.E(wbxml.PageSettings, "Enabled", wbxml.Text("1")),
							wbxml.E(wbxml.PageSettings, "ReplyMessage", wbxml.Text("Out today")),
							wbxml.E(wbxml.PageSettings, "BodyType", wbxml.Text("Text")),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	cfg, err := c.GetOof(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State != OofGlobal {
		t.Errorf("state = %v", cfg.State)
	}
	if !cfg.InternalReply.Enabled || cfg.InternalReply.ReplyMessage != "Out today" {
		t.Errorf("internal reply = %+v", cfg.InternalReply)
	}
}

func TestGetOof_unparseableTimeIsError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Oof",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageSettings, "Get",
						wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text("2")),
						wbxml.E(wbxml.PageSettings, "StartTime", wbxml.Text("not-a-timestamp")),
						wbxml.E(wbxml.PageSettings, "EndTime", wbxml.Text("2026-05-14T17:00:00Z")),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.GetOof(context.Background())
	if err == nil {
		t.Fatal("GetOof returned nil error for unparseable StartTime; want hard failure")
	}
	if !strings.Contains(err.Error(), "StartTime") || !strings.Contains(err.Error(), "not-a-timestamp") {
		t.Errorf("err = %q; want it to mention StartTime + the raw value", err)
	}
}

func TestSetOof_emitsTimeBased(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Oof",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	start := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 14, 17, 0, 0, 0, time.UTC)
	err := c.SetOof(context.Background(), OofConfig{
		State:     OofTimeBased,
		StartTime: start,
		EndTime:   end,
		InternalReply: OofMessage{
			Enabled:      true,
			ReplyMessage: "On vacation",
			BodyType:     BodyTypePlain,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if st := req.Root.Find("OofState"); st == nil || st.TextContent() != "2" {
		t.Errorf("OofState = %v", st)
	}
	if start := req.Root.Find("StartTime"); start == nil {
		t.Error("StartTime missing")
	}
	if msg := req.Root.Find("ReplyMessage"); msg == nil || msg.TextContent() != "On vacation" {
		t.Errorf("ReplyMessage = %v", msg)
	}
}

func TestSetDevicePassword_emitsPassword(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "DevicePassword",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.SetDevicePassword(context.Background(), "s3cret"); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	pw := req.Root.Find("Password")
	if pw == nil || pw.TextContent() != "s3cret" {
		t.Errorf("Password = %v", pw)
	}
}

func TestSetDevicePassword_emptyClearsPassword(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.SetDevicePassword(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if pw := req.Root.Find("Password"); pw != nil {
		t.Errorf("empty input should not emit <Password>; got %v", pw)
	}
}

func TestSetDevicePassword_topLevelStatusError(t *testing.T) {
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
	if err := c.SetDevicePassword(context.Background(), "x"); err == nil {
		t.Error("want error for non-OK top-level status")
	}
}

func TestSetDevicePassword_nestedStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "DevicePassword",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("3")),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.SetDevicePassword(context.Background(), "x"); err == nil {
		t.Error("want error for non-OK DevicePassword/Status")
	}
}

func TestGetUserInformation_basic(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "UserInformation",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageSettings, "EmailAddresses",
						wbxml.E(wbxml.PageSettings, "PrimarySmtpAddress", wbxml.Text("henry@example.com")),
						wbxml.E(wbxml.PageSettings, "SmtpAddress", wbxml.Text("henry@example.com")),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	info, err := c.GetUserInformation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.PrimaryEmail != "henry@example.com" {
		t.Errorf("primary email = %q", info.PrimaryEmail)
	}
}

// TestGetUserInformation_accountsList covers the Accounts/Account loop
// that the basic test skips. Real Office 365 mailboxes return one
// Account per delegated mailbox; the parser must round-trip every
// scalar field.
func TestGetUserInformation_accountsList(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{
			Root: wbxml.E(wbxml.PageSettings, "Settings",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "UserInformation",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageSettings, "EmailAddresses",
						wbxml.E(wbxml.PageSettings, "SmtpAddress", wbxml.Text("primary@x")),
					),
					wbxml.E(wbxml.PageSettings, "Accounts",
						wbxml.E(wbxml.PageSettings, "Account",
							wbxml.E(wbxml.PageSettings, "AccountId", wbxml.Text("acct-1")),
							wbxml.E(wbxml.PageSettings, "AccountName", wbxml.Text("Personal")),
							wbxml.E(wbxml.PageSettings, "UserDisplayName", wbxml.Text("Henry")),
							wbxml.E(wbxml.PageSettings, "EmailAddresses",
								wbxml.E(wbxml.PageSettings, "PrimarySmtpAddress", wbxml.Text("h@personal")),
							),
							wbxml.E(wbxml.PageSettings, "SendDisabled", wbxml.Text("0")),
						),
						wbxml.E(wbxml.PageSettings, "Account",
							wbxml.E(wbxml.PageSettings, "AccountId", wbxml.Text("acct-2")),
							wbxml.E(wbxml.PageSettings, "SendDisabled", wbxml.Text("1")),
						),
					),
				),
			),
		}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	info, err := c.GetUserInformation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.PrimaryEmail != "primary@x" {
		t.Errorf("PrimaryEmail = %q", info.PrimaryEmail)
	}
	if len(info.Accounts) != 2 {
		t.Fatalf("got %d accounts", len(info.Accounts))
	}
	a := info.Accounts[0]
	if a.AccountID != "acct-1" || a.AccountName != "Personal" ||
		a.UserDisplayName != "Henry" || a.PrimarySMTP != "h@personal" || a.SendDisabled {
		t.Errorf("account 0 = %+v", a)
	}
	if !info.Accounts[1].SendDisabled {
		t.Errorf("account 1 SendDisabled = %v", info.Accounts[1].SendDisabled)
	}
}

func TestGetOof_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.GetOof(context.Background()); err == nil {
		t.Error("want HTTP error")
	}
}

func TestGetOof_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	if _, err := c.GetOof(context.Background()); err == nil {
		t.Error("want error on empty response")
	}
}

func TestGetOof_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("3")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if _, err := c.GetOof(context.Background()); !IsStatusCode(err, 3) {
		t.Errorf("err = %v", err)
	}
}

func TestGetOof_missingOofRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.GetOof(context.Background())
	if err == nil || !strings.Contains(err.Error(), "<Oof>") {
		t.Errorf("err = %v", err)
	}
}

func TestGetOof_valuesUnderOofWithoutGetWrapper(t *testing.T) {
	// Some servers return values directly under Oof without a Get wrapper.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageSettings, "Oof",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text("1")),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	cfg, err := c.GetOof(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State != OofGlobal {
		t.Errorf("state = %v", cfg.State)
	}
}

func TestGetOof_unparseableEndTimeIsError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageSettings, "Oof",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Get",
					wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text("2")),
					wbxml.E(wbxml.PageSettings, "StartTime", wbxml.Text("2026-05-12T09:00:00Z")),
					wbxml.E(wbxml.PageSettings, "EndTime", wbxml.Text("garbage")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_, err := c.GetOof(context.Background())
	if err == nil || !strings.Contains(err.Error(), "EndTime") {
		t.Errorf("err = %v", err)
	}
}

func TestGetOof_parsesTimeBasedWindow(t *testing.T) {
	// Successful StartTime + EndTime parse populates the OofConfig fields.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageSettings, "Oof",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Get",
					wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text("2")),
					wbxml.E(wbxml.PageSettings, "StartTime", wbxml.Text("2026-05-12T09:00:00Z")),
					wbxml.E(wbxml.PageSettings, "EndTime", wbxml.Text("2026-05-14T17:00:00Z")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	cfg, err := c.GetOof(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StartTime.IsZero() || cfg.EndTime.IsZero() {
		t.Errorf("times = %v / %v", cfg.StartTime, cfg.EndTime)
	}
}

func TestSetOof_emitsHTMLBodyType(t *testing.T) {
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	_ = c.SetOof(context.Background(), OofConfig{
		State: OofGlobal,
		InternalReply: OofMessage{
			Enabled:      true,
			ReplyMessage: "<p>HTML reply</p>",
			BodyType:     BodyTypeHTML,
		},
	})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if bt := req.Root.Find("BodyType"); bt == nil || bt.TextContent() != "HTML" {
		t.Errorf("BodyType = %v, want HTML", bt)
	}
}

func TestGetOof_externalRepliesAndHTML(t *testing.T) {
	// Cover AppliesToExternalKnown / AppliesToExternalUnknown branches and
	// the BodyType=HTML case.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageSettings, "Oof",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Get",
					wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text("1")),
					wbxml.E(wbxml.PageSettings, "OofMessage",
						wbxml.E(wbxml.PageSettings, "AppliesToExternalKnown"),
						wbxml.E(wbxml.PageSettings, "Enabled", wbxml.Text("1")),
						wbxml.E(wbxml.PageSettings, "ReplyMessage", wbxml.Text("ext known")),
						wbxml.E(wbxml.PageSettings, "BodyType", wbxml.Text("HTML")),
					),
					wbxml.E(wbxml.PageSettings, "OofMessage",
						wbxml.E(wbxml.PageSettings, "AppliesToExternalUnknown"),
						wbxml.E(wbxml.PageSettings, "Enabled", wbxml.Text("1")),
						wbxml.E(wbxml.PageSettings, "ReplyMessage", wbxml.Text("ext unknown")),
						wbxml.E(wbxml.PageSettings, "BodyType", wbxml.Text("Text")),
					),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	cfg, err := c.GetOof(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExternalKnownReply.ReplyMessage != "ext known" || cfg.ExternalKnownReply.BodyType != BodyTypeHTML {
		t.Errorf("external known = %+v", cfg.ExternalKnownReply)
	}
	if cfg.ExternalUnknownReply.ReplyMessage != "ext unknown" {
		t.Errorf("external unknown = %+v", cfg.ExternalUnknownReply)
	}
}

func TestSetOof_skipsEmptyDisabledMessages(t *testing.T) {
	// A message with Enabled=false and empty ReplyMessage is dropped.
	c, cap, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	})
	_ = c.SetOof(context.Background(), OofConfig{
		State:                OofDisabled,
		InternalReply:        OofMessage{}, // skipped
		ExternalKnownReply:   OofMessage{Enabled: true, ReplyMessage: "ext"},
		ExternalUnknownReply: OofMessage{}, // skipped
	})
	req, _ := wbxml.Unmarshal(cap.body, wbxml.DefaultRegistry())
	if msgs := req.Root.FindAll("OofMessage"); len(msgs) != 1 {
		t.Errorf("OofMessage count = %d, want 1 (the Enabled one)", len(msgs))
	}
}

func TestSetOof_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := c.SetOof(context.Background(), OofConfig{State: OofDisabled}); err == nil {
		t.Error("want HTTP error")
	}
}

func TestSetOof_emptyResponseIsSuccess(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → success
	})
	if err := c.SetOof(context.Background(), OofConfig{State: OofDisabled}); err != nil {
		t.Errorf("want nil err, got %v", err)
	}
}

func TestSetOof_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("3")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	if err := c.SetOof(context.Background(), OofConfig{State: OofDisabled}); !IsStatusCode(err, 3) {
		t.Errorf("err = %v", err)
	}
}

func TestSetDevicePassword_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if err := c.SetDevicePassword(context.Background(), "x"); err == nil {
		t.Error("want HTTP error")
	}
}

func TestSetDevicePassword_emptyResponseIsSuccess(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body → success
	})
	if err := c.SetDevicePassword(context.Background(), "x"); err != nil {
		t.Errorf("want nil err, got %v", err)
	}
}

func TestGetUserInformation_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.GetUserInformation(context.Background()); err == nil {
		t.Error("want HTTP error")
	}
}

func TestGetUserInformation_emptyResponseRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	if _, err := c.GetUserInformation(context.Background()); err == nil {
		t.Error("want error on empty response")
	}
}

func TestGetUserInformation_skipsNonAccountChildren(t *testing.T) {
	// Stray child inside <Accounts> that isn't an <Account> must be skipped.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageSettings, "UserInformation",
				wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")),
				wbxml.E(wbxml.PageSettings, "Accounts",
					wbxml.E(wbxml.PageSettings, "Status", wbxml.Text("1")), // not an Account
					wbxml.E(wbxml.PageSettings, "Account",
						wbxml.E(wbxml.PageSettings, "AccountId", wbxml.Text("a")),
					),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	info, err := c.GetUserInformation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Accounts) != 1 {
		t.Errorf("Accounts = %d, want 1", len(info.Accounts))
	}
}

func TestGetUserInformation_topLevelStatusError(t *testing.T) {
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
	if _, err := c.GetUserInformation(context.Background()); err == nil {
		t.Error("want error for non-OK status")
	}
}
