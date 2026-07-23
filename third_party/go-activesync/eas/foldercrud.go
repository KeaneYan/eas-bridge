// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// FolderCreateResult is the parsed result of FolderCreate.
type FolderCreateResult struct {
	// ServerID is the new folder's server-assigned identifier.
	ServerID string
	// SyncKey is the new FolderSync key the server returns. Already
	// persisted in the StateStore by the helper.
	SyncKey string
	// Status is 1 on success.
	Status int
}

// FolderCreate creates a new folder with the given parent and type.
// parentID="0" creates a top-level folder.
//
// folderType matches MS-ASCMD §2.2.3.171 (the same enum as
// FolderSync's Folder.Type). Common values: 12=UserMail, 13=UserCalendar,
// 14=UserContacts, 15=UserTasks, 17=UserNotes, 1=UserGeneric.
func (c *httpClient) FolderCreate(ctx context.Context, parentID, displayName string, folderType FolderType) (*FolderCreateResult, error) {
	if displayName == "" {
		return nil, errors.New("eas: FolderCreate: displayName is required")
	}
	key, err := c.cfg.State.SyncKey(ctx, FolderRootID)
	if err != nil {
		return nil, fmt.Errorf("eas: FolderCreate: read sync key: %w", err)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderCreate",
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text(key)),
			wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text(parentID)),
			wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text(displayName)),
			wbxml.E(wbxml.PageFolderHierarchy, "Type", wbxml.Text(itoa(int(folderType)))),
		),
	}
	resp, err := c.post(ctx, "FolderCreate", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: FolderCreate: empty response")
	}
	out := &FolderCreateResult{}
	if st := resp.Root.Find("Status"); st != nil {
		out.Status = atoi(st.TextContent())
	}
	if out.Status != 0 && out.Status != StatusOK {
		return out, &StatusError{Command: "FolderCreate", Code: out.Status}
	}
	if k := resp.Root.Find("SyncKey"); k != nil {
		out.SyncKey = k.TextContent()
		_ = c.cfg.State.SetSyncKey(ctx, FolderRootID, out.SyncKey)
	}
	if id := resp.Root.Find("ServerId"); id != nil {
		out.ServerID = id.TextContent()
	}
	return out, nil
}

// FolderUpdate renames a folder and/or moves it under a new parent.
// Pass an empty newParentID to leave the parent unchanged.
func (c *httpClient) FolderUpdate(ctx context.Context, serverID, newParentID, newDisplayName string) error {
	if serverID == "" || newDisplayName == "" {
		return errors.New("eas: FolderUpdate: serverID and newDisplayName are required")
	}
	key, err := c.cfg.State.SyncKey(ctx, FolderRootID)
	if err != nil {
		return fmt.Errorf("eas: FolderUpdate: read sync key: %w", err)
	}
	root := wbxml.E(wbxml.PageFolderHierarchy, "FolderUpdate",
		wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text(serverID)),
	)
	if newParentID != "" {
		root.Children = append(root.Children,
			wbxml.E(wbxml.PageFolderHierarchy, "ParentId", wbxml.Text(newParentID)))
	}
	root.Children = append(root.Children,
		wbxml.E(wbxml.PageFolderHierarchy, "DisplayName", wbxml.Text(newDisplayName)))
	resp, err := c.post(ctx, "FolderUpdate", &wbxml.Document{Root: root})
	if err != nil {
		return err
	}
	return c.applyFolderHierarchyResp(ctx, resp, "FolderUpdate")
}

// FolderDelete removes a folder by its server-assigned id.
func (c *httpClient) FolderDelete(ctx context.Context, serverID string) error {
	if serverID == "" {
		return errors.New("eas: FolderDelete: serverID is required")
	}
	key, err := c.cfg.State.SyncKey(ctx, FolderRootID)
	if err != nil {
		return fmt.Errorf("eas: FolderDelete: read sync key: %w", err)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageFolderHierarchy, "FolderDelete",
			wbxml.E(wbxml.PageFolderHierarchy, "SyncKey", wbxml.Text(key)),
			wbxml.E(wbxml.PageFolderHierarchy, "ServerId", wbxml.Text(serverID)),
		),
	}
	resp, err := c.post(ctx, "FolderDelete", doc)
	if err != nil {
		return err
	}
	return c.applyFolderHierarchyResp(ctx, resp, "FolderDelete")
}

// applyFolderHierarchyResp parses Status + SyncKey from a FolderUpdate
// or FolderDelete response and persists the new sync key.
func (c *httpClient) applyFolderHierarchyResp(ctx context.Context, resp *wbxml.Document, cmd string) error {
	if resp == nil || resp.Root == nil {
		return fmt.Errorf("eas: %s: empty response", cmd)
	}
	if st := resp.Root.Find("Status"); st != nil {
		if code := atoi(st.TextContent()); code != 0 && code != StatusOK {
			return &StatusError{Command: cmd, Code: code}
		}
	}
	if k := resp.Root.Find("SyncKey"); k != nil {
		_ = c.cfg.State.SetSyncKey(ctx, FolderRootID, k.TextContent())
	}
	return nil
}
