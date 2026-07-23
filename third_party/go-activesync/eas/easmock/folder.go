// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// FolderClient is a hand-written test double for [eas.FolderClient].
type FolderClient struct {
	FolderSyncFunc            func(ctx context.Context) (*eas.FolderSyncResult, error)
	FolderCreateFunc          func(ctx context.Context, parentID, displayName string, folderType eas.FolderType) (*eas.FolderCreateResult, error)
	FolderUpdateFunc          func(ctx context.Context, serverID, newParentID, newDisplayName string) error
	FolderDeleteFunc          func(ctx context.Context, serverID string) error
	GetItemEstimateFunc       func(ctx context.Context, folderIDs []string) ([]eas.ItemEstimate, error)
	MoveItemsFunc             func(ctx context.Context, srcFolder, dstFolder string, ids []string) ([]eas.MoveItemResult, error)
	MoveViaItemOperationsFunc func(ctx context.Context, srcFolder, srcID, dstFolder string, moveAlways bool) (string, error)
	EmptyFolderContentsFunc   func(ctx context.Context, folderID string, deleteSubfolders bool) error
	FetchAttachmentFunc       func(ctx context.Context, fileReference string, rangeStart, rangeEnd int64) (*eas.FetchAttachmentResult, error)
	FetchDocumentLibraryFunc  func(ctx context.Context, linkID string, rangeStart, rangeEnd int64) ([]byte, error)
}

func (m *FolderClient) FolderSync(ctx context.Context) (*eas.FolderSyncResult, error) {
	if m.FolderSyncFunc != nil {
		return m.FolderSyncFunc(ctx)
	}
	return nil, errors.New("easmock: FolderSync not implemented")
}

func (m *FolderClient) FolderCreate(ctx context.Context, parentID, displayName string, folderType eas.FolderType) (*eas.FolderCreateResult, error) {
	if m.FolderCreateFunc != nil {
		return m.FolderCreateFunc(ctx, parentID, displayName, folderType)
	}
	return nil, errors.New("easmock: FolderCreate not implemented")
}

func (m *FolderClient) FolderUpdate(ctx context.Context, serverID, newParentID, newDisplayName string) error {
	if m.FolderUpdateFunc != nil {
		return m.FolderUpdateFunc(ctx, serverID, newParentID, newDisplayName)
	}
	return errors.New("easmock: FolderUpdate not implemented")
}

func (m *FolderClient) FolderDelete(ctx context.Context, serverID string) error {
	if m.FolderDeleteFunc != nil {
		return m.FolderDeleteFunc(ctx, serverID)
	}
	return errors.New("easmock: FolderDelete not implemented")
}

func (m *FolderClient) GetItemEstimate(ctx context.Context, folderIDs []string) ([]eas.ItemEstimate, error) {
	if m.GetItemEstimateFunc != nil {
		return m.GetItemEstimateFunc(ctx, folderIDs)
	}
	return nil, errors.New("easmock: GetItemEstimate not implemented")
}

func (m *FolderClient) MoveItems(ctx context.Context, srcFolder, dstFolder string, ids []string) ([]eas.MoveItemResult, error) {
	if m.MoveItemsFunc != nil {
		return m.MoveItemsFunc(ctx, srcFolder, dstFolder, ids)
	}
	return nil, errors.New("easmock: MoveItems not implemented")
}

func (m *FolderClient) MoveViaItemOperations(ctx context.Context, srcFolder, srcID, dstFolder string, moveAlways bool) (string, error) {
	if m.MoveViaItemOperationsFunc != nil {
		return m.MoveViaItemOperationsFunc(ctx, srcFolder, srcID, dstFolder, moveAlways)
	}
	return "", errors.New("easmock: MoveViaItemOperations not implemented")
}

func (m *FolderClient) EmptyFolderContents(ctx context.Context, folderID string, deleteSubfolders bool) error {
	if m.EmptyFolderContentsFunc != nil {
		return m.EmptyFolderContentsFunc(ctx, folderID, deleteSubfolders)
	}
	return errors.New("easmock: EmptyFolderContents not implemented")
}

func (m *FolderClient) FetchAttachment(ctx context.Context, fileReference string, rangeStart, rangeEnd int64) (*eas.FetchAttachmentResult, error) {
	if m.FetchAttachmentFunc != nil {
		return m.FetchAttachmentFunc(ctx, fileReference, rangeStart, rangeEnd)
	}
	return nil, errors.New("easmock: FetchAttachment not implemented")
}

func (m *FolderClient) FetchDocumentLibrary(ctx context.Context, linkID string, rangeStart, rangeEnd int64) ([]byte, error) {
	if m.FetchDocumentLibraryFunc != nil {
		return m.FetchDocumentLibraryFunc(ctx, linkID, rangeStart, rangeEnd)
	}
	return nil, errors.New("easmock: FetchDocumentLibrary not implemented")
}

var _ eas.FolderClient = (*FolderClient)(nil)
