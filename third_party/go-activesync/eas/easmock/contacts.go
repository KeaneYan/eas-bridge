// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// ContactsClient is a hand-written test double for [eas.ContactsClient].
type ContactsClient struct {
	SyncContactsFunc  func(ctx context.Context, folderID string) (*eas.ContactsSyncResult, error)
	CreateContactFunc func(ctx context.Context, folderID string, draft eas.ContactDraft) (string, error)
	UpdateContactFunc func(ctx context.Context, folderID, serverID string, draft eas.ContactDraft) error
	DeleteContactFunc func(ctx context.Context, folderID, serverID string) error
}

func (m *ContactsClient) SyncContacts(ctx context.Context, folderID string) (*eas.ContactsSyncResult, error) {
	if m.SyncContactsFunc != nil {
		return m.SyncContactsFunc(ctx, folderID)
	}
	return nil, errors.New("easmock: SyncContacts not implemented")
}

func (m *ContactsClient) CreateContact(ctx context.Context, folderID string, draft eas.ContactDraft) (string, error) {
	if m.CreateContactFunc != nil {
		return m.CreateContactFunc(ctx, folderID, draft)
	}
	return "", errors.New("easmock: CreateContact not implemented")
}

func (m *ContactsClient) UpdateContact(ctx context.Context, folderID, serverID string, draft eas.ContactDraft) error {
	if m.UpdateContactFunc != nil {
		return m.UpdateContactFunc(ctx, folderID, serverID, draft)
	}
	return errors.New("easmock: UpdateContact not implemented")
}

func (m *ContactsClient) DeleteContact(ctx context.Context, folderID, serverID string) error {
	if m.DeleteContactFunc != nil {
		return m.DeleteContactFunc(ctx, folderID, serverID)
	}
	return errors.New("easmock: DeleteContact not implemented")
}

var _ eas.ContactsClient = (*ContactsClient)(nil)
