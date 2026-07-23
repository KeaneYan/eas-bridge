// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"sync"
	"testing"
)

func TestMemoryState_PolicyKey(t *testing.T) {
	s := NewMemoryState()
	ctx := context.Background()
	pk, err := s.PolicyKey(ctx)
	if err != nil || pk != "" {
		t.Errorf("initial: pk=%q err=%v", pk, err)
	}
	if err := s.SetPolicyKey(ctx, "K1"); err != nil {
		t.Fatal(err)
	}
	pk, _ = s.PolicyKey(ctx)
	if pk != "K1" {
		t.Errorf("got %q", pk)
	}
	// Clear by setting empty string.
	_ = s.SetPolicyKey(ctx, "")
	pk, _ = s.PolicyKey(ctx)
	if pk != "" {
		t.Errorf("clear failed: %q", pk)
	}
}

func TestMemoryState_SyncKey(t *testing.T) {
	s := NewMemoryState()
	ctx := context.Background()
	// Unknown folder defaults to "0".
	k, err := s.SyncKey(ctx, "inbox")
	if err != nil || k != "0" {
		t.Errorf("default: k=%q err=%v", k, err)
	}
	_ = s.SetSyncKey(ctx, "inbox", "abc")
	_ = s.SetSyncKey(ctx, "calendar", "xyz")
	k, _ = s.SyncKey(ctx, "inbox")
	if k != "abc" {
		t.Errorf("inbox: %q", k)
	}
	k, _ = s.SyncKey(ctx, "calendar")
	if k != "xyz" {
		t.Errorf("calendar: %q", k)
	}
	k, _ = s.SyncKey(ctx, "other")
	if k != "0" {
		t.Errorf("other (untouched): %q", k)
	}
}

func TestMemoryState_concurrent(t *testing.T) {
	// Detect data races under -race; correctness is not strictly required
	// since stores last-wins, but no panics or torn maps.
	s := NewMemoryState()
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.SetPolicyKey(ctx, "k")
		}()
		go func() {
			defer wg.Done()
			_, _ = s.PolicyKey(ctx)
		}()
	}
	for range 50 {
		wg.Add(2)
		id := "f"
		go func() {
			defer wg.Done()
			_ = s.SetSyncKey(ctx, id, "k")
		}()
		go func() {
			defer wg.Done()
			_, _ = s.SyncKey(ctx, id)
		}()
	}
	wg.Wait()
}
