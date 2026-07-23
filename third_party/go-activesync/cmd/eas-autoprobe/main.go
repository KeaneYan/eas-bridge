// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

// eas-autoprobe runs the canonical EAS protocol surface against one
// account and reports per-command results. Intended as a one-shot
// diagnostic for contributors, server admins, and CI smoke tests.
//
// Read-only by design; never sends mail, never creates or deletes
// server-side state beyond the policy key persisted by Provision.
//
// Usage:
//
//	eas-autoprobe -email user@example.com
//	EAS_PROBE_PASSWORD=… eas-autoprobe -email user@example.com -server https://mail/Microsoft-Server-ActiveSync
//	pass show mail/work | eas-autoprobe -email user@example.com -password-stdin
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/hstern/go-activesync/eas"
)

type flags struct {
	email         string
	server        string
	passwordEnv   string
	passwordStdin bool
	deviceID      string
	asVersion     string
	emitJSON      bool
	verbose       bool
	pingSeconds   int
	timeout       time.Duration
}

type stepResult struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"`
	Error     string `json:"error,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms"`
}

type summary struct {
	Total     int   `json:"total"`
	OK        int   `json:"ok"`
	Fail      int   `json:"fail"`
	ElapsedMs int64 `json:"elapsed_ms"`
}

type runResult struct {
	Server    string       `json:"server"`
	Account   string       `json:"account"`
	ASVersion string       `json:"as_version"`
	Steps     []stepResult `json:"steps"`
	Summary   summary      `json:"summary"`
}

func main() {
	var f flags
	parseFlags(&f)
	os.Exit(run(f))
}

func parseFlags(f *flags) {
	flag.StringVar(&f.email, "email", os.Getenv("EAS_PROBE_USER"), "account email or username (default $EAS_PROBE_USER; required)")
	flag.StringVar(&f.server, "server", os.Getenv("EAS_PROBE_SERVER"), "EAS endpoint URL; if set, skip Autodiscover (default $EAS_PROBE_SERVER)")
	flag.StringVar(&f.passwordEnv, "password-env", "EAS_PROBE_PASSWORD", "env var holding the password")
	flag.BoolVar(&f.passwordStdin, "password-stdin", false, "read password from stdin (one line)")
	flag.StringVar(&f.deviceID, "device-id", "", "32-hex device identifier (default: deterministic per email)")
	flag.StringVar(&f.asVersion, "as-version", "14.0", "EAS protocol version to negotiate")
	flag.BoolVar(&f.emitJSON, "json", false, "emit one JSON record summarising the run")
	flag.BoolVar(&f.verbose, "verbose", false, "log every HTTP exchange at DEBUG")
	flag.IntVar(&f.pingSeconds, "ping", 0, "if > 0, run Ping with this heartbeat (long-poll; default off)")
	flag.DurationVar(&f.timeout, "timeout", 2*time.Minute, "overall context timeout")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "eas-autoprobe — one-shot EAS protocol probe\n\nUsage of %s:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
}

func run(f flags) int {
	if f.email == "" {
		fmt.Fprintln(os.Stderr, "error: -email is required")
		return 2
	}
	pw, err := resolvePassword(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()

	level := slog.LevelWarn
	if f.verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	var steps []stepResult
	serverURL := f.server

	// Autodiscover step (if no -server pinned).
	if serverURL == "" {
		t0 := time.Now()
		res, adErr := eas.Autodiscover(ctx, f.email, pw, eas.AutodiscoverOptions{Logger: logger})
		r := stepResult{Name: "Autodiscover", ElapsedMs: time.Since(t0).Milliseconds()}
		if adErr != nil {
			r.Error = adErr.Error()
			steps = append(steps, r)
			emit(f, runResult{Account: f.email, ASVersion: f.asVersion, Steps: steps, Summary: summarize(steps)})
			return 1
		}
		r.OK = true
		r.Detail = "URL=" + res.URL
		steps = append(steps, r)
		serverURL = res.URL
	}

	deviceID := f.deviceID
	if deviceID == "" {
		deviceID = deterministicDeviceID(f.email)
	}

	client, err := eas.NewClient(eas.Config{
		ServerURL: serverURL,
		Username:  f.email,
		Password:  pw,
		DeviceID:  deviceID,
		ASVersion: f.asVersion,
		Logger:    logger,
		State:     eas.NewMemoryState(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewClient: %v\n", err)
		return 1
	}

	p := &probe{
		client:      client,
		email:       f.email,
		pingSeconds: f.pingSeconds,
	}
	p.run(ctx, &steps)

	result := runResult{
		Server:    serverURL,
		Account:   f.email,
		ASVersion: f.asVersion,
		Steps:     steps,
		Summary:   summarize(steps),
	}
	emit(f, result)
	if result.Summary.Fail > 0 {
		return 1
	}
	return 0
}

func summarize(steps []stepResult) summary {
	var s summary
	s.Total = len(steps)
	for _, r := range steps {
		if r.OK {
			s.OK++
		} else {
			s.Fail++
		}
		s.ElapsedMs += r.ElapsedMs
	}
	return s
}

func emit(f flags, r runResult) {
	if f.emitJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return
	}
	fmt.Printf("Probing %s as %s (EAS %s)\n\n", r.Server, r.Account, r.ASVersion)
	for _, s := range r.Steps {
		if s.OK {
			fmt.Printf("  %-30s OK    %s  (%dms)\n", s.Name, s.Detail, s.ElapsedMs)
		} else {
			fmt.Printf("  %-30s FAIL  %s  (%dms)\n", s.Name, s.Error, s.ElapsedMs)
		}
	}
	fmt.Printf("\n=== Summary: %d ok, %d fail (%dms total) ===\n",
		r.Summary.OK, r.Summary.Fail, r.Summary.ElapsedMs)
}

func resolvePassword(f flags) (string, error) {
	if f.passwordStdin {
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return "", fmt.Errorf("read password from stdin: %w", err)
			}
			return "", errors.New("no password on stdin")
		}
		return strings.TrimRight(sc.Text(), "\r\n"), nil
	}
	pw := os.Getenv(f.passwordEnv)
	if pw == "" {
		return "", fmt.Errorf("env var %s is not set; either set it or use -password-stdin", f.passwordEnv)
	}
	return pw, nil
}

// deterministicDeviceID returns a stable 32-hex DeviceID derived from
// the email address. Same account → same DeviceID across runs, so EAS
// servers don't treat each probe as a new device.
func deterministicDeviceID(email string) string {
	h := sha256.Sum256([]byte("eas-autoprobe:" + email))
	return hex.EncodeToString(h[:])[:32]
}
