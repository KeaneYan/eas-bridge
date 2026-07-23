// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// OofState matches MS-ASCMD §2.2.3.139 (OofState).
type OofState int

const (
	OofDisabled  OofState = 0
	OofGlobal    OofState = 1
	OofTimeBased OofState = 2
)

// OofMessage is one of the three OOF reply variants (internal,
// external-known, external-unknown).
type OofMessage struct {
	Enabled      bool
	ReplyMessage string
	BodyType     BodyType // BodyTypePlain or BodyTypeHTML
}

// OofConfig is the user's full Out-of-Office configuration.
type OofConfig struct {
	State                OofState
	StartTime            time.Time
	EndTime              time.Time
	InternalReply        OofMessage
	ExternalKnownReply   OofMessage
	ExternalUnknownReply OofMessage
}

// GetOof reads the current Out-of-Office configuration via the
// Settings/Oof/Get command.
func (c *httpClient) GetOof(ctx context.Context) (*OofConfig, error) {
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Oof",
				wbxml.E(wbxml.PageSettings, "Get",
					wbxml.E(wbxml.PageSettings, "BodyType", wbxml.Text("Text")),
				),
			),
		),
	}
	resp, err := c.post(ctx, "Settings", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: GetOof: empty response")
	}
	if st := resp.Root.Find("Status"); st != nil {
		if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Settings/Oof/Get", Code: code}
		}
	}
	oof := resp.Root.Find("Oof")
	if oof == nil {
		return nil, errors.New("eas: GetOof: response missing <Oof>")
	}
	out := &OofConfig{}
	getEl := oof.Find("Get")
	if getEl == nil {
		// Some servers return values directly under Oof.
		getEl = oof
	}
	if st := getEl.Find("OofState"); st != nil {
		out.State = OofState(atoi(st.TextContent()))
	}
	// OOF is a scheduled feature; if the server gave us a window we can't
	// understand, returning a zero time would silently degrade the user's
	// schedule. Treat unparseable timestamps as a hard error so callers
	// don't act on bogus data.
	if t := getEl.Find("StartTime"); t != nil {
		raw := t.TextContent()
		parsed, ok := parseEASTime(raw)
		if !ok && raw != "" {
			return nil, fmt.Errorf("eas: GetOof: unparseable StartTime %q", raw)
		}
		out.StartTime = parsed
	}
	if t := getEl.Find("EndTime"); t != nil {
		raw := t.TextContent()
		parsed, ok := parseEASTime(raw)
		if !ok && raw != "" {
			return nil, fmt.Errorf("eas: GetOof: unparseable EndTime %q", raw)
		}
		out.EndTime = parsed
	}
	for _, c := range getEl.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Name != "OofMessage" {
			continue
		}
		msg := OofMessage{}
		if e := el.Find("Enabled"); e != nil {
			msg.Enabled = e.TextContent() == "1"
		}
		if r := el.Find("ReplyMessage"); r != nil {
			msg.ReplyMessage = r.TextContent()
		}
		if bt := el.Find("BodyType"); bt != nil {
			switch bt.TextContent() {
			case "HTML":
				msg.BodyType = BodyTypeHTML
			default:
				msg.BodyType = BodyTypePlain
			}
		}
		switch {
		case el.Find("AppliesToInternal") != nil:
			out.InternalReply = msg
		case el.Find("AppliesToExternalKnown") != nil:
			out.ExternalKnownReply = msg
		case el.Find("AppliesToExternalUnknown") != nil:
			out.ExternalUnknownReply = msg
		}
	}
	return out, nil
}

// SetOof updates the user's Out-of-Office configuration via the
// Settings/Oof/Set command.
func (c *httpClient) SetOof(ctx context.Context, cfg OofConfig) error {
	set := wbxml.E(wbxml.PageSettings, "Set",
		wbxml.E(wbxml.PageSettings, "OofState", wbxml.Text(itoa(int(cfg.State)))),
	)
	if cfg.State == OofTimeBased {
		set.Children = append(set.Children,
			wbxml.E(wbxml.PageSettings, "StartTime", wbxml.Text(formatEASTime(cfg.StartTime))),
			wbxml.E(wbxml.PageSettings, "EndTime", wbxml.Text(formatEASTime(cfg.EndTime))),
		)
	}
	addMsg := func(applies string, msg OofMessage) {
		if !msg.Enabled && msg.ReplyMessage == "" {
			return
		}
		btName := "Text"
		if msg.BodyType == BodyTypeHTML {
			btName = "HTML"
		}
		set.Children = append(set.Children, wbxml.E(wbxml.PageSettings, "OofMessage",
			wbxml.E(wbxml.PageSettings, applies),
			wbxml.E(wbxml.PageSettings, "Enabled", wbxml.Text(boolNumString(msg.Enabled))),
			wbxml.E(wbxml.PageSettings, "ReplyMessage", wbxml.Text(msg.ReplyMessage)),
			wbxml.E(wbxml.PageSettings, "BodyType", wbxml.Text(btName)),
		))
	}
	addMsg("AppliesToInternal", cfg.InternalReply)
	addMsg("AppliesToExternalKnown", cfg.ExternalKnownReply)
	addMsg("AppliesToExternalUnknown", cfg.ExternalUnknownReply)

	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "Oof", set),
		),
	}
	resp, err := c.post(ctx, "Settings", doc)
	if err != nil {
		return err
	}
	if resp == nil || resp.Root == nil {
		return nil // empty success
	}
	if st := resp.Root.Find("Status"); st != nil {
		if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
			return &StatusError{Command: "Settings/Oof/Set", Code: code}
		}
	}
	return nil
}

// UserInformation is the result of Settings/UserInformation/Get.
type UserInformation struct {
	PrimaryEmail string
	Accounts     []UserAccount
}

// UserAccount describes one of the user's mail accounts as known to
// the EAS server (Exchange typically reports just the primary).
type UserAccount struct {
	AccountID       string
	AccountName     string
	UserDisplayName string
	PrimarySMTP     string
	SendDisabled    bool
}

// SetDevicePassword sends a Settings/DevicePassword/Set command,
// reporting (or rotating) the password the device uses to enforce a
// device-lock policy. EAS uses this to record password compliance
// when the policy requires DevicePasswordEnabled.
//
// Pass an empty newPassword to clear (Reset). For non-device clients
// (sync daemons, headless agents) this is mostly a no-op compliance
// signal — the caller can't actually lock the host — but reporting
// compliance keeps strict servers happy.
func (c *httpClient) SetDevicePassword(ctx context.Context, newPassword string) error {
	set := wbxml.E(wbxml.PageSettings, "Set")
	if newPassword != "" {
		set.Children = append(set.Children, wbxml.E(wbxml.PageSettings, "Password", wbxml.Text(newPassword)))
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "DevicePassword", set),
		),
	}
	resp, err := c.post(ctx, "Settings", doc)
	if err != nil {
		return err
	}
	if resp == nil || resp.Root == nil {
		return nil
	}
	if st := resp.Root.Find("Status"); st != nil {
		if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
			return &StatusError{Command: "Settings/DevicePassword/Set", Code: code}
		}
	}
	if dp := resp.Root.Find("DevicePassword"); dp != nil {
		if st := dp.Find("Status"); st != nil {
			if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
				return &StatusError{Command: "Settings/DevicePassword/Set", Code: code}
			}
		}
	}
	return nil
}

// GetUserInformation reads Settings/UserInformation, returning the
// primary email address and any additional account info the server
// exposes.
func (c *httpClient) GetUserInformation(ctx context.Context) (*UserInformation, error) {
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageSettings, "Settings",
			wbxml.E(wbxml.PageSettings, "UserInformation",
				wbxml.E(wbxml.PageSettings, "Get"),
			),
		),
	}
	resp, err := c.post(ctx, "Settings", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: GetUserInformation: empty response")
	}
	if st := resp.Root.Find("Status"); st != nil {
		if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Settings/UserInformation/Get", Code: code}
		}
	}
	out := &UserInformation{}
	if ui := resp.Root.Find("UserInformation"); ui != nil {
		if emails := ui.Find("EmailAddresses"); emails != nil {
			if smtp := emails.Find("SmtpAddress"); smtp != nil {
				out.PrimaryEmail = smtp.TextContent()
			}
			if pri := emails.Find("PrimarySmtpAddress"); pri != nil {
				out.PrimaryEmail = pri.TextContent()
			}
		}
		if accts := ui.Find("Accounts"); accts != nil {
			for _, a := range accts.Children {
				ae, ok := a.(*wbxml.Element)
				if !ok || ae.Name != "Account" {
					continue
				}
				acct := UserAccount{}
				if id := ae.Find("AccountId"); id != nil {
					acct.AccountID = id.TextContent()
				}
				if n := ae.Find("AccountName"); n != nil {
					acct.AccountName = n.TextContent()
				}
				if dn := ae.Find("UserDisplayName"); dn != nil {
					acct.UserDisplayName = dn.TextContent()
				}
				if e := ae.Find("EmailAddresses"); e != nil {
					if pri := e.Find("PrimarySmtpAddress"); pri != nil {
						acct.PrimarySMTP = pri.TextContent()
					}
				}
				if sd := ae.Find("SendDisabled"); sd != nil {
					acct.SendDisabled = sd.TextContent() == "1"
				}
				out.Accounts = append(out.Accounts, acct)
			}
		}
	}
	return out, nil
}
