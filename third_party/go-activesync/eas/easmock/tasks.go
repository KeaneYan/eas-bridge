// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// TasksClient is a hand-written test double for [eas.TasksClient].
type TasksClient struct {
	SyncTasksFunc    func(ctx context.Context, folderID string) (*eas.TasksSyncResult, error)
	CreateTaskFunc   func(ctx context.Context, folderID string, draft eas.TaskDraft) (string, error)
	UpdateTaskFunc   func(ctx context.Context, folderID, serverID string, draft eas.TaskDraft) error
	CompleteTaskFunc func(ctx context.Context, folderID, serverID string) error
	DeleteTaskFunc   func(ctx context.Context, folderID, serverID string) error
}

func (m *TasksClient) SyncTasks(ctx context.Context, folderID string) (*eas.TasksSyncResult, error) {
	if m.SyncTasksFunc != nil {
		return m.SyncTasksFunc(ctx, folderID)
	}
	return nil, errors.New("easmock: SyncTasks not implemented")
}

func (m *TasksClient) CreateTask(ctx context.Context, folderID string, draft eas.TaskDraft) (string, error) {
	if m.CreateTaskFunc != nil {
		return m.CreateTaskFunc(ctx, folderID, draft)
	}
	return "", errors.New("easmock: CreateTask not implemented")
}

func (m *TasksClient) UpdateTask(ctx context.Context, folderID, serverID string, draft eas.TaskDraft) error {
	if m.UpdateTaskFunc != nil {
		return m.UpdateTaskFunc(ctx, folderID, serverID, draft)
	}
	return errors.New("easmock: UpdateTask not implemented")
}

func (m *TasksClient) CompleteTask(ctx context.Context, folderID, serverID string) error {
	if m.CompleteTaskFunc != nil {
		return m.CompleteTaskFunc(ctx, folderID, serverID)
	}
	return errors.New("easmock: CompleteTask not implemented")
}

func (m *TasksClient) DeleteTask(ctx context.Context, folderID, serverID string) error {
	if m.DeleteTaskFunc != nil {
		return m.DeleteTaskFunc(ctx, folderID, serverID)
	}
	return errors.New("easmock: DeleteTask not implemented")
}

var _ eas.TasksClient = (*TasksClient)(nil)
