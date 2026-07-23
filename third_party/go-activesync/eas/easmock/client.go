// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import "github.com/hstern/go-activesync/eas"

// Client is the umbrella mock for [eas.Client]. Embed and override
// only the sub-mocks (or specific Func fields on them) the test
// cares about. Methods not configured return a sentinel error.
//
//	mock := &easmock.Client{
//	    EmailClient: easmock.EmailClient{
//	        SyncEmailFunc: func(ctx context.Context, fid string, opts eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
//	            return &eas.EmailSyncResult{SyncKey: "1"}, nil
//	        },
//	    },
//	}
//	var _ eas.Client = mock
type Client struct {
	EmailClient
	CalendarClient
	ContactsClient
	TasksClient
	NotesClient
	FolderClient
	SettingsClient
	SearchClient
	ProvisionClient
	PingClient

	LastPolicyFunc func() *eas.Policy
}

// LastPolicy returns the canned Policy from LastPolicyFunc, or nil
// if unset.
func (m *Client) LastPolicy() *eas.Policy {
	if m.LastPolicyFunc != nil {
		return m.LastPolicyFunc()
	}
	return nil
}

var _ eas.Client = (*Client)(nil)
