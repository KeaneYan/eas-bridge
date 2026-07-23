// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

//go:build integration

// Package eas integration tests. Skipped by default; build with the
// "integration" tag and point at a real EAS server via env vars:
//
//	EAS_INTEGRATION_URL    https://host/Microsoft-Server-ActiveSync
//	EAS_INTEGRATION_USER   the account username (often the email address)
//	EAS_INTEGRATION_PASS   the account password
//	EAS_INTEGRATION_DEVICE optional 32-hex device id (default integration00…)
//	EAS_INTEGRATION_VERSION optional EAS version (default: NegotiateVersion result)
//	EAS_INTEGRATION_INSECURE if "1", skip TLS verification (self-signed test cert)
//	EAS_INTEGRATION_VERBOSE  if "1", enable debug logging to stderr
//
// Run from the repo root:
//
//	go test -tags integration -v ./eas
//
// The testenv/ directory contains a Docker Compose stack that provides
// a known-good target; see testenv/README.md.
package eas_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hstern/go-activesync/eas"
)

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("set %s to run integration tests", key)
	}
	return v
}

// integrationClient builds a Client wired for the env-supplied server.
func integrationClient(t *testing.T) eas.Client {
	t.Helper()
	url := mustEnv(t, "EAS_INTEGRATION_URL")
	user := mustEnv(t, "EAS_INTEGRATION_USER")
	pass := mustEnv(t, "EAS_INTEGRATION_PASS")

	deviceID := os.Getenv("EAS_INTEGRATION_DEVICE")
	if deviceID == "" {
		deviceID = "integration00000000000000000000"
	}
	asVersion := os.Getenv("EAS_INTEGRATION_VERSION")
	if asVersion == "" {
		asVersion = "14.1"
	}

	hc := http.DefaultClient
	if os.Getenv("EAS_INTEGRATION_INSECURE") == "1" {
		hc = &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}}
	}

	logLevel := slog.LevelInfo
	if os.Getenv("EAS_INTEGRATION_VERBOSE") == "1" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	c, err := eas.NewClient(eas.Config{
		ServerURL:    url,
		Username:     user,
		Password:     pass,
		DeviceID:     deviceID,
		DeviceType:   "GoActivesyncIntegration",
		ASVersion:    asVersion,
		UserAgent:    "go-activesync-integration-test/0.1",
		HTTPClient:   hc,
		Logger:       logger,
		State:        eas.NewMemoryState(),
		GzipRequests: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntegration_OptionsAndNegotiate(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()

	opts, err := c.Options(ctx)
	mustOK(t, err)
	if len(opts.ProtocolVersions) == 0 {
		t.Fatal("server announced no protocol versions")
	}
	t.Logf("server versions: %v", opts.ProtocolVersions)
	t.Logf("server commands: %v", opts.Commands)

	if !opts.HasCommand("FolderSync") || !opts.HasCommand("Sync") {
		t.Errorf("server missing core commands: %v", opts.Commands)
	}

	v, err := c.NegotiateVersion(ctx)
	mustOK(t, err)
	t.Logf("negotiated: %s", v)
}

func TestIntegration_Provision(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	mustOK(t, c.Provision(ctx))
	if p := c.LastPolicy(); p != nil {
		t.Logf("server policy: passwordEnabled=%v minLen=%d maxAttachKB=%d",
			p.DevicePasswordEnabled, p.MinDevicePasswordLength,
			p.MaxAttachmentSizeBytes/1024)
	}
}

// provisionedClient returns a freshly-provisioned client ready for
// data commands.
func provisionedClient(t *testing.T) eas.Client {
	t.Helper()
	c := integrationClient(t)
	ctx := context.Background()
	if _, err := c.NegotiateVersion(ctx); err != nil {
		t.Logf("negotiate (continuing): %v", err)
	}
	if err := c.SettingsDeviceInformation(ctx, eas.DeviceInformation{
		Model: "GoActivesyncIntegration", FriendlyName: "go-activesync test",
	}); err != nil {
		t.Logf("device info (continuing): %v", err)
	}
	mustOK(t, c.Provision(ctx))
	return c
}

func TestIntegration_FolderSync_HasInbox(t *testing.T) {
	c := provisionedClient(t)
	res, err := c.FolderSync(context.Background())
	mustOK(t, err)
	t.Logf("FolderSync: key=%s added=%d updated=%d deleted=%d",
		res.SyncKey, len(res.Added), len(res.Updated), len(res.Deleted))
	for _, f := range res.Added {
		t.Logf("  [%s] %-30s id=%s", f.Type, f.DisplayName, f.ServerID)
	}
	if findInboxID(res.Added) == "" {
		t.Error("no Inbox folder returned")
	}
}

func TestIntegration_SyncEmail_Bootstrap(t *testing.T) {
	c := provisionedClient(t)
	ctx := context.Background()
	folders, err := c.FolderSync(ctx)
	mustOK(t, err)
	inbox := findInboxID(folders.Added)
	if inbox == "" {
		t.Skip("no Inbox folder")
	}
	res, err := c.SyncEmail(ctx, inbox, eas.EmailSyncOptions{WindowSize: 25})
	mustOK(t, err)
	t.Logf("SyncEmail Inbox: key=%s added=%d more=%v",
		res.SyncKey, len(res.Added), res.MoreAvailable)
	for i, e := range res.Added {
		if i >= 3 {
			break
		}
		t.Logf("  %s — %q  from=%s", e.ServerID, e.Subject, e.From)
	}
}

func TestIntegration_SendMail_Loopback(t *testing.T) {
	c := provisionedClient(t)
	ctx := context.Background()
	user := os.Getenv("EAS_INTEGRATION_USER")
	subject := fmt.Sprintf("go-activesync integration loopback %d", time.Now().UnixNano())
	mime := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
			"This message was sent by the go-activesync integration test.\r\n",
		user, user, subject))
	mustOK(t, c.SendMail(ctx, eas.SendMailOptions{MIME: mime, SkipSaveInSent: true}))
	t.Logf("SendMail succeeded; subject=%q", subject)

	// Best-effort verify via a single Sync after a brief settle period.
	// We don't poll aggressively because some servers (z-push 2.7's
	// LoopDetection in particular) treat repeated Syncs from the same
	// device within seconds as a stuck client and refuse with 449.
	folders, err := c.FolderSync(ctx)
	mustOK(t, err)
	inbox := findInboxID(folders.Added)
	if inbox == "" {
		t.Skip("no Inbox; can't verify loopback")
	}

	time.Sleep(5 * time.Second) // give postfix → dovecot LMTP a moment

	// Bootstrap (first call returns no items per protocol) then sync.
	if _, err := c.SyncEmail(ctx, inbox, eas.EmailSyncOptions{
		WindowSize: 1, NoBootstrap: true,
	}); err != nil {
		t.Logf("bootstrap sync (continuing): %v", err)
	}
	res, err := c.SyncEmail(ctx, inbox, eas.EmailSyncOptions{
		WindowSize: 50, BodyType: eas.BodyTypePlain,
	})
	if err != nil {
		t.Logf("post-send sync: %v — SendMail confirmed at protocol level, delivery unverifiable here", err)
		return
	}
	for _, e := range res.Added {
		if strings.Contains(e.Subject, subject) {
			t.Logf("loopback delivered: id=%s", e.ServerID)
			return
		}
	}
	t.Logf("loopback not in this batch (saw %d items); SendMail confirmed at protocol level", len(res.Added))
}

func TestIntegration_Calendar_CRUD(t *testing.T) {
	c := provisionedClient(t)
	ctx := context.Background()
	folders, err := c.FolderSync(ctx)
	mustOK(t, err)
	calID := findFirstByType(folders.Added, eas.FolderTypeCalendar, eas.FolderTypeUserCalendar)
	if calID == "" {
		t.Skip("no Calendar folder")
	}

	start := time.Now().Add(24 * time.Hour).Truncate(time.Hour).UTC()
	end := start.Add(time.Hour)
	subject := fmt.Sprintf("go-activesync integration event %d", time.Now().UnixNano())

	id, err := c.CreateEvent(ctx, calID, eas.EventDraft{
		Subject:    subject,
		Location:   "test",
		StartTime:  start,
		EndTime:    end,
		BusyStatus: 2,
	})
	mustOK(t, err)
	t.Logf("created event id=%s", id)
	t.Cleanup(func() {
		if err := c.DeleteEvent(context.Background(), calID, id); err != nil {
			t.Logf("cleanup DeleteEvent: %v", err)
		}
	})

	// Verify via a fresh client so the bootstrap-then-Sync dance returns
	// the new event in Added. The original client's per-folder state has
	// already advanced past the create (EAS doesn't echo a client's own
	// adds back on subsequent syncs of the same connection). Use a
	// distinct DeviceID so the verifier's syncs don't invalidate the
	// original client's server-side sync keys — Z-Push tracks state per
	// (user, device) and a second client on the same DeviceID would
	// rotate the keys out from under our DeleteEvent cleanup.
	t.Setenv("EAS_INTEGRATION_DEVICE", "verifier000000000000000000000000")
	c2 := provisionedClient(t)
	if _, err := c2.FolderSync(ctx); err != nil {
		t.Fatalf("verify FolderSync: %v", err)
	}
	res, err := c2.SyncCalendar(ctx, calID, eas.CalendarSyncOptions{})
	mustOK(t, err)
	var found *eas.EventItem
	for i := range res.Added {
		if res.Added[i].Subject == subject {
			found = &res.Added[i]
			t.Logf("event visible: id=%s start=%v", found.ServerID, found.StartTime)
			break
		}
	}
	if found == nil {
		t.Fatalf("created event %q not visible after Sync (Added=%d)",
			subject, len(res.Added))
	}
	if found.StartTime.IsZero() {
		t.Errorf("event StartTime parsed to zero — server emitted an unrecognised time format")
	} else if !found.StartTime.Equal(start) {
		t.Errorf("event StartTime = %v, want %v", found.StartTime, start)
	}
}

func TestIntegration_Tasks_CRUD(t *testing.T) {
	c := provisionedClient(t)
	ctx := context.Background()
	folders, err := c.FolderSync(ctx)
	mustOK(t, err)
	taskID := findFirstByType(folders.Added, eas.FolderTypeTasks, eas.FolderTypeUserTasks)
	if taskID == "" {
		t.Skip("no Tasks folder")
	}

	due := time.Now().Add(48 * time.Hour).Truncate(time.Hour).UTC()
	subject := fmt.Sprintf("go-activesync integration task %d", time.Now().UnixNano())

	id, err := c.CreateTask(ctx, taskID, eas.TaskDraft{
		Subject:    subject,
		Importance: 1,
		DueDate:    due,
		UTCDueDate: due,
	})
	mustOK(t, err)
	t.Logf("created task id=%s", id)
	t.Cleanup(func() {
		if err := c.DeleteTask(context.Background(), taskID, id); err != nil {
			t.Logf("cleanup DeleteTask: %v", err)
		}
	})

	// Verify via a fresh client (separate DeviceID) — same EAS sync
	// semantics as Calendar_CRUD: the creating client's state has
	// already advanced past the add.
	t.Setenv("EAS_INTEGRATION_DEVICE", "tasks-verifier000000000000000000")
	c2 := provisionedClient(t)
	if _, err := c2.FolderSync(ctx); err != nil {
		t.Fatalf("verify FolderSync: %v", err)
	}
	res, err := c2.SyncTasks(ctx, taskID)
	mustOK(t, err)
	var found *eas.TaskItem
	for i := range res.Added {
		if res.Added[i].Subject == subject {
			found = &res.Added[i]
			t.Logf("task visible: id=%s utcdue=%v complete=%v",
				found.ServerID, found.UTCDueDate, found.Complete)
			break
		}
	}
	if found == nil {
		t.Fatalf("created task %q not visible after Sync (Added=%d)",
			subject, len(res.Added))
	}
	// Z-Push BackendCalDAV writes the iCalendar VTODO DUE field as UTC
	// and only echoes back UtcDueDate on subsequent syncs (DueDate
	// requires the client's local timezone, which the server lacks).
	// Asserting on UTCDueDate documents this behaviour.
	if !found.UTCDueDate.Equal(due) {
		t.Errorf("UTCDueDate = %v, want %v", found.UTCDueDate, due)
	}
}

func TestIntegration_Ping_NotifiesOnNewEmail(t *testing.T) {
	c := provisionedClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	folders, err := c.FolderSync(ctx)
	mustOK(t, err)
	inbox := findInboxID(folders.Added)
	if inbox == "" {
		t.Skip("no Inbox folder")
	}

	// Bootstrap the inbox sync state so Z-Push has a baseline to detect
	// changes against. Without this, Ping returns Status=7 (folder
	// hierarchy out of date) on the first call.
	if _, err := c.SyncEmail(ctx, inbox, eas.EmailSyncOptions{
		WindowSize: 1, NoBootstrap: true,
	}); err != nil {
		t.Fatalf("bootstrap sync: %v", err)
	}

	// Run Ping in a goroutine with a heartbeat that exceeds the time we
	// expect the loopback mail to take (~5s for postfix → dovecot LMTP)
	// but stays well under the test deadline.
	pingDone := make(chan struct{})
	var pingRes *eas.PingResult
	var pingErr error
	go func() {
		defer close(pingDone)
		pingRes, pingErr = c.Ping(ctx, 60, []eas.PingFolder{
			{ID: inbox, Class: "Email"},
		})
	}()

	// Give the server a moment to register the Ping subscription before
	// generating the change it should report.
	time.Sleep(2 * time.Second)

	user := os.Getenv("EAS_INTEGRATION_USER")
	subject := fmt.Sprintf("go-activesync ping notify %d", time.Now().UnixNano())
	mime := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+
			"Triggering Ping notification.\r\n",
		user, user, subject))
	mustOK(t, c.SendMail(ctx, eas.SendMailOptions{MIME: mime, SkipSaveInSent: true}))

	select {
	case <-pingDone:
	case <-ctx.Done():
		t.Fatalf("Ping did not return within deadline (still in flight)")
	}
	if pingErr != nil {
		t.Fatalf("Ping error: %v", pingErr)
	}
	if pingRes.Status != 2 {
		t.Fatalf("Ping returned Status=%d, want 2 (changes available); ChangedFolders=%v",
			pingRes.Status, pingRes.ChangedFolders)
	}
	matched := false
	for _, id := range pingRes.ChangedFolders {
		if id == inbox {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("Ping reported changes in %v, want Inbox %q", pingRes.ChangedFolders, inbox)
	}
	t.Logf("Ping notified after %s; ChangedFolders=%v", "loopback delivery", pingRes.ChangedFolders)
}

func TestIntegration_GAL_Search(t *testing.T) {
	c := provisionedClient(t)
	ctx := context.Background()
	user := os.Getenv("EAS_INTEGRATION_USER")
	q := user
	if i := strings.IndexByte(q, '@'); i >= 0 {
		q = q[:i]
	}
	res, err := c.GALSearch(ctx, q, 5)
	if err != nil {
		t.Skipf("GAL search unsupported on this server: %v", err)
	}
	t.Logf("GAL: %d entries (total %d)", len(res.Entries), res.Total)
}

func TestIntegration_Settings_OOF_RoundTrip(t *testing.T) {
	c := provisionedClient(t)
	ctx := context.Background()
	cfg, err := c.GetOof(ctx)
	if err != nil {
		t.Skipf("OOF unsupported on this server: %v", err)
	}
	t.Logf("current OOF state: %v", cfg.State)
	// Don't actually set OOF — would surprise the user. Just confirm
	// the read path works.
}

// --- helpers ---------------------------------------------------------------

func findInboxID(folders []eas.Folder) string {
	for _, f := range folders {
		if f.Type == eas.FolderTypeInbox {
			return f.ServerID
		}
	}
	return ""
}

func findFirstByType(folders []eas.Folder, types ...eas.FolderType) string {
	for _, t := range types {
		for _, f := range folders {
			if f.Type == t {
				return f.ServerID
			}
		}
	}
	return ""
}
