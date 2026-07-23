// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// NotesClient is a hand-written test double for [eas.NotesClient].
type NotesClient struct {
	SyncNotesFunc  func(ctx context.Context, folderID string) (*eas.NotesSyncResult, error)
	CreateNoteFunc func(ctx context.Context, folderID string, draft eas.NoteDraft) (string, error)
	UpdateNoteFunc func(ctx context.Context, folderID, serverID string, draft eas.NoteDraft) error
	DeleteNoteFunc func(ctx context.Context, folderID, serverID string) error
}

func (m *NotesClient) SyncNotes(ctx context.Context, folderID string) (*eas.NotesSyncResult, error) {
	if m.SyncNotesFunc != nil {
		return m.SyncNotesFunc(ctx, folderID)
	}
	return nil, errors.New("easmock: SyncNotes not implemented")
}

func (m *NotesClient) CreateNote(ctx context.Context, folderID string, draft eas.NoteDraft) (string, error) {
	if m.CreateNoteFunc != nil {
		return m.CreateNoteFunc(ctx, folderID, draft)
	}
	return "", errors.New("easmock: CreateNote not implemented")
}

func (m *NotesClient) UpdateNote(ctx context.Context, folderID, serverID string, draft eas.NoteDraft) error {
	if m.UpdateNoteFunc != nil {
		return m.UpdateNoteFunc(ctx, folderID, serverID, draft)
	}
	return errors.New("easmock: UpdateNote not implemented")
}

func (m *NotesClient) DeleteNote(ctx context.Context, folderID, serverID string) error {
	if m.DeleteNoteFunc != nil {
		return m.DeleteNoteFunc(ctx, folderID, serverID)
	}
	return errors.New("easmock: DeleteNote not implemented")
}

var _ eas.NotesClient = (*NotesClient)(nil)
