// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"sync"
)

// StateStore persists the per-account state that EAS protocol exchanges
// require: the policy key from the most recent Provision and the per-folder
// SyncKey for incremental Sync.
//
// All methods take a context for cancellation; implementations backed by
// remote storage should honor it.
//
// FolderID is the EAS server-assigned folder identifier (an opaque string;
// for the special root FolderSync state, callers use FolderRootID).
type StateStore interface {
	// PolicyKey returns the persisted policy key, or an empty string if the
	// account has not been provisioned yet. A nil error with an empty string
	// is the expected pre-provisioning state.
	PolicyKey(ctx context.Context) (string, error)
	// SetPolicyKey persists the provided key. An empty string clears the
	// stored key (used when re-provisioning is forced by a 449 response).
	SetPolicyKey(ctx context.Context, key string) error

	// SyncKey returns the most recently acknowledged SyncKey for folderID,
	// or "0" if no Sync has succeeded for that folder yet.
	SyncKey(ctx context.Context, folderID string) (string, error)
	// SetSyncKey persists the SyncKey returned by the server.
	SetSyncKey(ctx context.Context, folderID, key string) error
}

// FolderRootID is the conventional folderID used to store the FolderSync
// SyncKey (which is per-account, not per-folder). Callers should not use
// this string as a real folder identifier.
const FolderRootID = "__root__"

// MemoryState is an in-memory StateStore. Useful for tests and one-shot
// CLI tools where state need not survive process exit. Safe for concurrent
// use.
type MemoryState struct {
	mu        sync.Mutex
	policyKey string
	syncKeys  map[string]string
}

// NewMemoryState returns an empty in-memory StateStore.
func NewMemoryState() *MemoryState {
	return &MemoryState{syncKeys: map[string]string{}}
}

// PolicyKey implements StateStore.
func (m *MemoryState) PolicyKey(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.policyKey, nil
}

// SetPolicyKey implements StateStore.
func (m *MemoryState) SetPolicyKey(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policyKey = key
	return nil
}

// SyncKey implements StateStore.
func (m *MemoryState) SyncKey(_ context.Context, folderID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.syncKeys[folderID]; ok {
		return k, nil
	}
	return "0", nil
}

// SetSyncKey implements StateStore.
func (m *MemoryState) SetSyncKey(_ context.Context, folderID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncKeys[folderID] = key
	return nil
}
