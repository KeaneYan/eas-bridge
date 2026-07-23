// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// CalendarClient is a hand-written test double for [eas.CalendarClient].
type CalendarClient struct {
	SyncCalendarFunc  func(ctx context.Context, folderID string, opts eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error)
	CreateEventFunc   func(ctx context.Context, folderID string, draft eas.EventDraft) (string, error)
	UpdateEventFunc   func(ctx context.Context, folderID, serverID string, draft eas.EventDraft) error
	DeleteEventFunc   func(ctx context.Context, folderID, serverID string) error
	RespondInviteFunc func(ctx context.Context, folderID, serverID string, choice eas.MeetingResponseChoice) (*eas.MeetingResponseResult, error)
}

func (m *CalendarClient) SyncCalendar(ctx context.Context, folderID string, opts eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
	if m.SyncCalendarFunc != nil {
		return m.SyncCalendarFunc(ctx, folderID, opts)
	}
	return nil, errors.New("easmock: SyncCalendar not implemented")
}

func (m *CalendarClient) CreateEvent(ctx context.Context, folderID string, draft eas.EventDraft) (string, error) {
	if m.CreateEventFunc != nil {
		return m.CreateEventFunc(ctx, folderID, draft)
	}
	return "", errors.New("easmock: CreateEvent not implemented")
}

func (m *CalendarClient) UpdateEvent(ctx context.Context, folderID, serverID string, draft eas.EventDraft) error {
	if m.UpdateEventFunc != nil {
		return m.UpdateEventFunc(ctx, folderID, serverID, draft)
	}
	return errors.New("easmock: UpdateEvent not implemented")
}

func (m *CalendarClient) DeleteEvent(ctx context.Context, folderID, serverID string) error {
	if m.DeleteEventFunc != nil {
		return m.DeleteEventFunc(ctx, folderID, serverID)
	}
	return errors.New("easmock: DeleteEvent not implemented")
}

func (m *CalendarClient) RespondInvite(ctx context.Context, folderID, serverID string, choice eas.MeetingResponseChoice) (*eas.MeetingResponseResult, error) {
	if m.RespondInviteFunc != nil {
		return m.RespondInviteFunc(ctx, folderID, serverID, choice)
	}
	return nil, errors.New("easmock: RespondInvite not implemented")
}

var _ eas.CalendarClient = (*CalendarClient)(nil)
