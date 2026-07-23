// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hstern/go-activesync/eas"
)

// probe carries shared state across the step sequence.
type probe struct {
	client      eas.Client
	email       string
	pingSeconds int

	folders    *eas.FolderSyncResult
	inboxID    string
	calID      string
	contactsID string
	tasksID    string
	notesID    string
	firstMsgID string
}

func (p *probe) run(ctx context.Context, results *[]stepResult) {
	do := func(name string, fn func(context.Context) (string, error)) {
		t0 := time.Now()
		detail, err := fn(ctx)
		r := stepResult{Name: name, ElapsedMs: time.Since(t0).Milliseconds()}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.OK = true
			r.Detail = detail
		}
		*results = append(*results, r)
	}

	do("Provision", func(ctx context.Context) (string, error) {
		if err := p.client.Provision(ctx); err != nil {
			return "", err
		}
		if p.client.LastPolicy() == nil {
			return "no policy", nil
		}
		return "policyKey set", nil
	})

	do("FolderSync", func(ctx context.Context) (string, error) {
		r, err := p.client.FolderSync(ctx)
		if err != nil {
			return "", err
		}
		p.folders = r
		return fmt.Sprintf("%d folders", len(r.Added)), nil
	})

	if p.folders == nil {
		// Provision or FolderSync failed; nothing else can run sanely.
		return
	}
	p.classifyFolders()

	if p.inboxID != "" {
		do("SyncEmail (inbox, prime)", func(ctx context.Context) (string, error) {
			r, err := p.client.SyncEmail(ctx, p.inboxID, eas.EmailSyncOptions{})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("syncKey=%s adds=%d", r.SyncKey, len(r.Added)), nil
		})
		do("SyncEmail (inbox, fetch)", func(ctx context.Context) (string, error) {
			r, err := p.client.SyncEmail(ctx, p.inboxID, eas.EmailSyncOptions{WindowSize: 5})
			if err != nil {
				return "", err
			}
			if len(r.Added) > 0 {
				p.firstMsgID = r.Added[0].ServerID
			}
			return fmt.Sprintf("adds=%d", len(r.Added)), nil
		})
		do("GetItemEstimate (inbox)", func(ctx context.Context) (string, error) {
			est, err := p.client.GetItemEstimate(ctx, []string{p.inboxID})
			if err != nil {
				return "", err
			}
			if len(est) == 0 {
				return "no estimate returned", nil
			}
			return fmt.Sprintf("estimate=%d", est[0].Estimate), nil
		})
		if p.firstMsgID != "" {
			do("FetchEmail (full body)", func(ctx context.Context) (string, error) {
				m, err := p.client.FetchEmail(ctx, p.inboxID, p.firstMsgID, eas.FetchEmailOptions{})
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("subject=%q bodySize=%d", m.Subject, len(m.Body)), nil
			})
		}
		do("SearchEmail", func(ctx context.Context) (string, error) {
			r, err := p.client.SearchEmail(ctx, "test", eas.EmailSearchOptions{})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("hits=%d total=%d", len(r.Items), r.Total), nil
		})
	}

	if p.calID != "" {
		do("SyncCalendar", func(ctx context.Context) (string, error) {
			if _, err := p.client.SyncCalendar(ctx, p.calID, eas.CalendarSyncOptions{}); err != nil {
				return "", err
			}
			r, err := p.client.SyncCalendar(ctx, p.calID, eas.CalendarSyncOptions{WindowSize: 5})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("events=%d", len(r.Added)), nil
		})
	}

	if p.contactsID != "" {
		do("SyncContacts", func(ctx context.Context) (string, error) {
			if _, err := p.client.SyncContacts(ctx, p.contactsID); err != nil {
				return "", err
			}
			r, err := p.client.SyncContacts(ctx, p.contactsID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("contacts=%d", len(r.Added)), nil
		})
	}

	if p.tasksID != "" {
		do("SyncTasks", func(ctx context.Context) (string, error) {
			if _, err := p.client.SyncTasks(ctx, p.tasksID); err != nil {
				return "", err
			}
			r, err := p.client.SyncTasks(ctx, p.tasksID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("tasks=%d", len(r.Added)), nil
		})
	}

	if p.notesID != "" {
		do("SyncNotes", func(ctx context.Context) (string, error) {
			if _, err := p.client.SyncNotes(ctx, p.notesID); err != nil {
				return "", err
			}
			r, err := p.client.SyncNotes(ctx, p.notesID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("notes=%d", len(r.Added)), nil
		})
	}

	do("GetUserInformation", func(ctx context.Context) (string, error) {
		u, err := p.client.GetUserInformation(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("primary=%s accounts=%d", u.PrimaryEmail, len(u.Accounts)), nil
	})

	do("GetOof", func(ctx context.Context) (string, error) {
		o, err := p.client.GetOof(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("state=%v", o.State), nil
	})

	do("ResolveRecipients (self)", func(ctx context.Context) (string, error) {
		r, err := p.client.ResolveRecipients(ctx, []string{p.email}, eas.ResolveOptions{})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("responses=%d", len(r)), nil
	})

	if p.pingSeconds > 0 && p.inboxID != "" {
		do(fmt.Sprintf("Ping (heartbeat=%ds)", p.pingSeconds), func(ctx context.Context) (string, error) {
			res, err := p.client.Ping(ctx, p.pingSeconds, []eas.PingFolder{{ID: p.inboxID, Class: "Email"}})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("status=%v changed=%v", res.Status, res.ChangedFolders), nil
		})
	}
}

func (p *probe) classifyFolders() {
	pick := func(types ...eas.FolderType) string {
		for _, f := range p.folders.Added {
			for _, t := range types {
				if f.Type == t {
					return f.ServerID
				}
			}
		}
		return ""
	}
	p.inboxID = pick(eas.FolderTypeInbox)
	p.calID = pick(eas.FolderTypeCalendar, eas.FolderTypeUserCalendar)
	p.contactsID = pick(eas.FolderTypeContacts, eas.FolderTypeUserContacts)
	p.tasksID = pick(eas.FolderTypeTasks, eas.FolderTypeUserTasks)
	p.notesID = pick(eas.FolderTypeNotes, eas.FolderTypeUserNotes)
}
