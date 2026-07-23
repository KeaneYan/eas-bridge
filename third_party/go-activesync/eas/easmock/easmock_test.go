// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"strings"
	"testing"

	"github.com/hstern/go-activesync/eas"
)

// TestUmbrellaConformance checks that the Client mock satisfies every
// sub-interface and the umbrella eas.Client interface — guards against
// future additions to eas/interfaces.go that don't get mirrored here.
func TestUmbrellaConformance(t *testing.T) {
	var c eas.Client = &Client{}
	if c == nil {
		t.Fatal("Client doesn't satisfy eas.Client")
	}
}

// TestSentinelErrors confirms each unconfigured method returns an
// error mentioning the method name. One representative per
// sub-mock; not exhaustive — the compile-time _ = (*X)(nil)
// assertions in each file already catch missing-method drift.
func TestSentinelErrors(t *testing.T) {
	ctx := context.Background()
	m := &Client{}

	cases := []struct {
		name string
		fn   func() error
	}{
		{"SyncEmail", func() error { _, err := m.SyncEmail(ctx, "f", eas.EmailSyncOptions{}); return err }},
		{"SyncCalendar", func() error { _, err := m.SyncCalendar(ctx, "f", eas.CalendarSyncOptions{}); return err }},
		{"SyncContacts", func() error { _, err := m.SyncContacts(ctx, "f"); return err }},
		{"SyncTasks", func() error { _, err := m.SyncTasks(ctx, "f"); return err }},
		{"SyncNotes", func() error { _, err := m.SyncNotes(ctx, "f"); return err }},
		{"FolderSync", func() error { _, err := m.FolderSync(ctx); return err }},
		{"GetOof", func() error { _, err := m.GetOof(ctx); return err }},
		{"GALSearch", func() error { _, err := m.GALSearch(ctx, "x", 1); return err }},
		{"Provision", func() error { return m.Provision(ctx) }},
		{"Ping", func() error { _, err := m.Ping(ctx, 60, nil); return err }},
	}
	for _, c := range cases {
		err := c.fn()
		if err == nil {
			t.Errorf("%s: nil err, want sentinel", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.name) {
			t.Errorf("%s: %q does not mention method name", c.name, err.Error())
		}
	}
}

// TestFuncRoutes confirms a configured Func is invoked verbatim and
// its arguments / return values flow through. One per sub-mock,
// covering the most-used method per interface.
func TestFuncRoutes(t *testing.T) {
	ctx := context.Background()
	want := &eas.EmailSyncResult{SyncKey: "S2"}
	m := &Client{
		EmailClient: EmailClient{
			SyncEmailFunc: func(_ context.Context, fid string, _ eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				if fid != "inbox" {
					t.Errorf("fid = %q", fid)
				}
				return want, nil
			},
		},
		ProvisionClient: ProvisionClient{
			ProvisionFunc: func(context.Context) error { return nil },
		},
		LastPolicyFunc: func() *eas.Policy { return &eas.Policy{Hash: "h1"} },
	}
	got, err := m.SyncEmail(ctx, "inbox", eas.EmailSyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if err := m.Provision(ctx); err != nil {
		t.Errorf("Provision: %v", err)
	}
	if p := m.LastPolicy(); p == nil || p.Hash != "h1" {
		t.Errorf("LastPolicy: %+v", p)
	}
}
