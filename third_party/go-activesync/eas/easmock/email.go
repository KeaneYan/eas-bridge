// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// EmailClient is a hand-written test double for [eas.EmailClient].
type EmailClient struct {
	SyncEmailFunc         func(ctx context.Context, folderID string, opts eas.EmailSyncOptions) (*eas.EmailSyncResult, error)
	ApplyEmailChangesFunc func(ctx context.Context, folderID string, changes []eas.EmailChange) ([]eas.EmailChangeResult, error)
	FetchEmailFunc        func(ctx context.Context, folderID, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error)
	SendMailFunc          func(ctx context.Context, opts eas.SendMailOptions) error
	SmartReplyFunc        func(ctx context.Context, opts eas.ReplyForwardOptions) error
	SmartForwardFunc      func(ctx context.Context, opts eas.ReplyForwardOptions) error
	SearchEmailFunc       func(ctx context.Context, query string, opts eas.EmailSearchOptions) (*eas.EmailSearchResult, error)
	SearchEmailQueryFunc  func(ctx context.Context, q eas.Query, opts eas.EmailSearchOptions) (*eas.EmailSearchResult, error)
	FindEmailFunc         func(ctx context.Context, query string, opts eas.FindOptions) (*eas.FindResult, error)
}

func (m *EmailClient) SyncEmail(ctx context.Context, folderID string, opts eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
	if m.SyncEmailFunc != nil {
		return m.SyncEmailFunc(ctx, folderID, opts)
	}
	return nil, errors.New("easmock: SyncEmail not implemented")
}

func (m *EmailClient) ApplyEmailChanges(ctx context.Context, folderID string, changes []eas.EmailChange) ([]eas.EmailChangeResult, error) {
	if m.ApplyEmailChangesFunc != nil {
		return m.ApplyEmailChangesFunc(ctx, folderID, changes)
	}
	return nil, errors.New("easmock: ApplyEmailChanges not implemented")
}

func (m *EmailClient) FetchEmail(ctx context.Context, folderID, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
	if m.FetchEmailFunc != nil {
		return m.FetchEmailFunc(ctx, folderID, serverID, opts)
	}
	return nil, errors.New("easmock: FetchEmail not implemented")
}

func (m *EmailClient) SendMail(ctx context.Context, opts eas.SendMailOptions) error {
	if m.SendMailFunc != nil {
		return m.SendMailFunc(ctx, opts)
	}
	return errors.New("easmock: SendMail not implemented")
}

func (m *EmailClient) SmartReply(ctx context.Context, opts eas.ReplyForwardOptions) error {
	if m.SmartReplyFunc != nil {
		return m.SmartReplyFunc(ctx, opts)
	}
	return errors.New("easmock: SmartReply not implemented")
}

func (m *EmailClient) SmartForward(ctx context.Context, opts eas.ReplyForwardOptions) error {
	if m.SmartForwardFunc != nil {
		return m.SmartForwardFunc(ctx, opts)
	}
	return errors.New("easmock: SmartForward not implemented")
}

func (m *EmailClient) SearchEmail(ctx context.Context, query string, opts eas.EmailSearchOptions) (*eas.EmailSearchResult, error) {
	if m.SearchEmailFunc != nil {
		return m.SearchEmailFunc(ctx, query, opts)
	}
	return nil, errors.New("easmock: SearchEmail not implemented")
}

func (m *EmailClient) SearchEmailQuery(ctx context.Context, q eas.Query, opts eas.EmailSearchOptions) (*eas.EmailSearchResult, error) {
	if m.SearchEmailQueryFunc != nil {
		return m.SearchEmailQueryFunc(ctx, q, opts)
	}
	return nil, errors.New("easmock: SearchEmailQuery not implemented")
}

func (m *EmailClient) FindEmail(ctx context.Context, query string, opts eas.FindOptions) (*eas.FindResult, error) {
	if m.FindEmailFunc != nil {
		return m.FindEmailFunc(ctx, query, opts)
	}
	return nil, errors.New("easmock: FindEmail not implemented")
}

var _ eas.EmailClient = (*EmailClient)(nil)
