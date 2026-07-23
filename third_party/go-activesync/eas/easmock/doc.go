// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

// Package easmock provides hand-written test doubles for the
// [github.com/hstern/go-activesync/eas] interface set.
//
// Each interface in eas has a sibling struct here with one func-typed
// field per method. Tests set the fields they care about; methods
// whose Func is nil return a sentinel error so a misbehaving handler
// fails loudly rather than silently calling a no-op.
//
//	mock := &easmock.Client{
//	    EmailClient: easmock.EmailClient{
//	        SyncEmailFunc: func(ctx context.Context, fid string, opts eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
//	            return &eas.EmailSyncResult{SyncKey: "1", Added: []eas.EmailItem{{Subject: "hi"}}}, nil
//	        },
//	    },
//	}
//
//	// mock implements eas.Client; pass it where the consumer expects
//	// an eas.Client.
//
// The mocks are intentionally hand-written: zero codegen, zero deps,
// and easy to extend when [eas] adds methods (the only edits are an
// extra Func field and a one-method stub).
package easmock
