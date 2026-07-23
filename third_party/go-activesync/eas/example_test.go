// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas_test

import (
	"context"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/hstern/go-activesync/eas"
)

// Minimal end-to-end usage: build a Client, provision once, list folders.
//
// These examples are compiled by `go test` (and rendered on
// pkg.go.dev) but not executed — they touch the network. To run them
// for real, point at a live server with the EAS_INTEGRATION_* env
// vars and `go test -tags integration ./eas`.
func ExampleNewClient() {
	c, err := eas.NewClient(eas.Config{
		ServerURL: "https://mail.example.com/Microsoft-Server-ActiveSync",
		Username:  "henry",
		Password:  "secret",
		DeviceID:  "32hexcharsofdeviceidhere00000000",
		State:     eas.NewMemoryState(),
	})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	if _, err := c.NegotiateVersion(ctx); err != nil {
		panic(err)
	}
	if err := c.Provision(ctx); err != nil {
		panic(err)
	}

	folders, err := c.FolderSync(ctx)
	if err != nil {
		panic(err)
	}
	for _, f := range folders.Added {
		fmt.Printf("%s %s\n", f.Type, f.DisplayName)
	}
}

// SyncEmail walks the protocol-mandated bootstrap (first call returns
// no items, only a fresh SyncKey) transparently. Subsequent calls
// return deltas; pagination is implicit via MoreAvailable.
func ExampleClient_SyncEmail() {
	c, _ := eas.NewClient(eas.Config{
		ServerURL: "https://mail.example.com/Microsoft-Server-ActiveSync",
		Username:  "henry",
		Password:  "secret",
		DeviceID:  "32hexcharsofdeviceidhere00000000",
		State:     eas.NewMemoryState(),
	})
	ctx := context.Background()
	_ = c.Provision(ctx)

	for {
		res, err := c.SyncEmail(ctx, "inbox-folder-id", eas.EmailSyncOptions{
			WindowSize: 50,
			BodyType:   eas.BodyTypePlain,
			DateFilter: eas.FilterTwoWeek,
		})
		if err != nil {
			panic(err)
		}
		for _, e := range res.Added {
			fmt.Printf("%s — %q\n", e.From, e.Subject)
		}
		if !res.MoreAvailable {
			break
		}
	}
}

// SearchEmailQuery uses a structured AST instead of free-text. Combine
// And / Or / EqualTo / GreaterThan / LessThan with the prebuilt
// PropEmail* property handles for common comparisons.
func ExampleClient_SearchEmailQuery() {
	c, _ := eas.NewClient(eas.Config{
		ServerURL: "https://mail.example.com/Microsoft-Server-ActiveSync",
		Username:  "henry",
		Password:  "secret",
		DeviceID:  "32hexcharsofdeviceidhere00000000",
		State:     eas.NewMemoryState(),
	})
	ctx := context.Background()
	_ = c.Provision(ctx)

	q := eas.And(
		eas.EmailClass(),
		eas.EqualTo(eas.PropEmailFrom, "alice@example.com"),
		eas.GreaterThan(eas.PropEmailDateReceived,
			time.Now().Add(-30*24*time.Hour).Format("2006-01-02T15:04:05.000Z")),
	)
	res, err := c.SearchEmailQuery(ctx, q, eas.EmailSearchOptions{Range: "0-49"})
	if err != nil {
		panic(err)
	}
	for _, hit := range res.Items {
		fmt.Println(hit.ServerID, hit.Subject)
	}
}

// CreateEvent wires up a recurring weekly meeting. Single-instance
// events skip the Recurrence field; per-instance overrides go in
// Exceptions.
func ExampleClient_CreateEvent() {
	c, _ := eas.NewClient(eas.Config{
		ServerURL: "https://mail.example.com/Microsoft-Server-ActiveSync",
		Username:  "henry",
		Password:  "secret",
		DeviceID:  "32hexcharsofdeviceidhere00000000",
		State:     eas.NewMemoryState(),
	})
	ctx := context.Background()
	_ = c.Provision(ctx)

	start := time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC)
	id, err := c.CreateEvent(ctx, "calendar-folder-id", eas.EventDraft{
		Subject:   "Weekly review",
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		Recurrence: &eas.Recurrence{
			Type:      eas.RecurrenceWeekly,
			Interval:  1,
			DayOfWeek: eas.DowMonday,
			Until:     time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC),
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("created", id)
}

// SignMIME wraps a plain RFC 5322 message in an S/MIME multipart/signed
// envelope. EncryptMIME and SignAndEncryptMIME mirror this for
// recipient-side encryption.
func ExampleSignMIME() {
	var (
		signerCert *x509.Certificate
		signerKey  any
	)
	plain := []byte("From: a@b\r\nTo: c@d\r\nSubject: hi\r\n\r\nhello")

	signed, err := eas.SignMIME(plain, eas.SMIMESigner{
		Certificate: signerCert,
		PrivateKey:  signerKey,
	})
	if err != nil {
		panic(err)
	}
	// `signed` is a complete multipart/signed message body, ready to
	// hand to Client.SendMail as SendMailOptions.MIME.
	fmt.Println(len(signed))
}

// EncodeTimeZone turns a Go *time.Location into the 172-byte
// Microsoft TIME_ZONE_INFORMATION blob that Calendar events use.
// Use the EASTimeZone struct directly for full DST-rule fidelity
// (Go's *time.Location doesn't expose the recurring rule).
func ExampleEncodeTimeZone() {
	utc := eas.EncodeTimeZone(time.UTC)
	fmt.Println("utc encoded length (base64):", len(utc))

	// Round-trip back.
	decoded, err := eas.DecodeTimeZone(utc)
	if err != nil {
		panic(err)
	}
	fmt.Println("bias minutes:", decoded.BiasMinutes)
}
