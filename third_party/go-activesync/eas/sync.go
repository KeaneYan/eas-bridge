// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// EmailSyncOptions controls a SyncEmail request.
//
// Defaults applied when fields are zero:
//   - WindowSize: 50
//   - BodyType:   BodyTypePlain
//   - BodyTruncationSize: 32 KiB
//   - DateFilter: FilterTwoWeek (a sensible default for "list my recent mail")
//
// Set NoBootstrap=true to suppress the transparent two-call bootstrap
// when the persisted SyncKey is "0"; useful for tests that want to
// observe a single round-trip.
type EmailSyncOptions struct {
	WindowSize         int
	BodyType           BodyType
	BodyTruncationSize int
	DateFilter         FilterType
	NoBootstrap        bool
	// MIMESupport controls how the server returns MIME content:
	// 0=never (default), 1=for S/MIME only, 2=always.
	MIMESupport int
	// MIMETruncation, when >0, truncates the MIME blob to that many
	// bytes (different from BodyTruncationSize, which applies to the
	// AirSyncBase Body).
	MIMETruncation int
	// ConversationMode, when true, asks the server to deliver
	// conversation-grouped results (14.0+).
	ConversationMode bool
	// RightsManagementSupport, when true, includes IRM-protected items
	// in the response with the license metadata expanded (14.1+).
	RightsManagementSupport bool
	// BodyPart, when non-zero, requests a body-part preference
	// (a fragment of the body sized to the value, used for previews).
	BodyPartPreviewBytes int
	// Wait is the long-poll interval in minutes (1-59). When non-zero,
	// the server holds the request open until items are available or
	// Wait minutes elapse. Mutually exclusive with HeartbeatSeconds.
	WaitMinutes int
	// HeartbeatSeconds is the long-poll interval in seconds. EAS 14.0+
	// alternative to WaitMinutes with finer granularity. Mutually
	// exclusive with WaitMinutes.
	HeartbeatSeconds int
}

// EmailSyncResult is the parsed output of one SyncEmail round-trip (or
// the second of two round-trips when bootstrap was triggered).
type EmailSyncResult struct {
	SyncKey       string
	MoreAvailable bool
	Added         []EmailItem
	Changed       []EmailItem
	Deleted       []string
}

// SyncEmail issues an AirSync Sync command for the given email folder
// and returns the next batch of changes.
//
// If the persisted SyncKey for the folder is "0" (i.e. this is the first
// sync), SyncEmail issues TWO requests: the bootstrap one to obtain a key
// and the data one to fetch items. Pass NoBootstrap to disable that and
// observe the underlying behavior directly.
//
// On Status=3 (InvalidSyncKey) SyncEmail resets the persisted key to "0"
// and retries once. This is the canonical recovery for server-side state
// resets and matches FolderSync's behavior.
func (c *httpClient) SyncEmail(ctx context.Context, folderID string, opts EmailSyncOptions) (*EmailSyncResult, error) {
	if folderID == "" {
		return nil, errors.New("eas: SyncEmail: folderID is required")
	}
	if opts.WindowSize <= 0 {
		opts.WindowSize = 50
	}
	if opts.BodyType == BodyTypeNone {
		opts.BodyType = BodyTypePlain
	}
	if opts.BodyTruncationSize <= 0 {
		opts.BodyTruncationSize = 32 << 10
	}
	if opts.DateFilter == 0 {
		opts.DateFilter = FilterTwoWeek
	}

	res, err := c.syncEmailOnce(ctx, folderID, opts)
	if err != nil && IsStatusCode(err, StatusInvalidSyncKey) {
		if rerr := c.cfg.State.SetSyncKey(ctx, folderID, "0"); rerr != nil {
			return nil, fmt.Errorf("eas: SyncEmail: reset key: %w", rerr)
		}
		res, err = c.syncEmailOnce(ctx, folderID, opts)
	}
	if err != nil {
		return nil, err
	}

	// First-sync bootstrap: if the prior key was "0" and the response
	// carried no items (typical EAS behavior on initial sync), make a
	// second call to actually fetch data. The new SyncKey from the first
	// call is what's now in storage.
	if !opts.NoBootstrap && len(res.Added) == 0 && len(res.Changed) == 0 && len(res.Deleted) == 0 {
		// Detect "this was the bootstrap" by the fact that storage now
		// contains a non-zero key. Re-call once.
		stored, _ := c.cfg.State.SyncKey(ctx, folderID)
		if stored != "0" && stored != "" {
			res2, err := c.syncEmailOnce(ctx, folderID, opts)
			if err == nil {
				return res2, nil
			}
			// fall through to return the bootstrap result on error so
			// the caller at least sees the new key
		}
	}
	return res, nil
}

func (c *httpClient) syncEmailOnce(ctx context.Context, folderID string, opts EmailSyncOptions) (*EmailSyncResult, error) {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("eas: SyncEmail: read key: %w", err)
	}
	doc := buildSyncRequest(folderID, key, opts)
	resp, err := c.post(ctx, "Sync", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		// Some servers reply with no body when there are no changes and
		// the SyncKey is unchanged. Treat as empty.
		return &EmailSyncResult{SyncKey: key}, nil
	}
	return parseSyncResponse(ctx, c, folderID, resp.Root)
}

func buildSyncRequest(folderID, key string, opts EmailSyncOptions) *wbxml.Document {
	options := wbxml.E(wbxml.PageAirSync, "Options",
		wbxml.E(wbxml.PageAirSync, "FilterType", wbxml.Text(itoa(int(opts.DateFilter)))),
	)
	if opts.MIMESupport > 0 {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageAirSync, "MIMESupport", wbxml.Text(itoa(opts.MIMESupport))))
	}
	if opts.MIMETruncation > 0 {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageAirSync, "MIMETruncation", wbxml.Text(itoa(opts.MIMETruncation))))
	}
	options.Children = append(options.Children,
		wbxml.E(wbxml.PageAirSyncBase, "BodyPreference",
			wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text(itoa(int(opts.BodyType)))),
			wbxml.E(wbxml.PageAirSyncBase, "TruncationSize", wbxml.Text(itoa(opts.BodyTruncationSize))),
		),
	)
	if opts.BodyPartPreviewBytes > 0 {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageAirSyncBase, "BodyPartPreference",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "TruncationSize", wbxml.Text(itoa(opts.BodyPartPreviewBytes))),
			),
		)
	}
	if opts.RightsManagementSupport {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageRightsManagement, "RightsManagementSupport", wbxml.Text("1")))
	}
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		wbxml.E(wbxml.PageAirSync, "DeletesAsMoves", wbxml.Text("1")),
		wbxml.E(wbxml.PageAirSync, "GetChanges", wbxml.Text("1")),
		wbxml.E(wbxml.PageAirSync, "WindowSize", wbxml.Text(itoa(opts.WindowSize))),
	)
	if opts.ConversationMode {
		collection.Children = append(collection.Children,
			wbxml.E(wbxml.PageAirSync, "ConversationMode", wbxml.Text("1")))
	}
	collection.Children = append(collection.Children, options)

	root := wbxml.E(wbxml.PageAirSync, "Sync",
		wbxml.E(wbxml.PageAirSync, "Collections", collection),
	)
	// Wait/HeartbeatInterval live at the top of the Sync element, not
	// inside Options. Spec mandates only one or the other.
	if opts.WaitMinutes > 0 {
		root.Children = append([]wbxml.Node{
			wbxml.E(wbxml.PageAirSync, "Wait", wbxml.Text(itoa(opts.WaitMinutes))),
		}, root.Children...)
	} else if opts.HeartbeatSeconds > 0 {
		root.Children = append([]wbxml.Node{
			wbxml.E(wbxml.PageAirSync, "HeartbeatInterval", wbxml.Text(itoa(opts.HeartbeatSeconds))),
		}, root.Children...)
	}
	return &wbxml.Document{Root: root}
}

func parseSyncResponse(ctx context.Context, c *httpClient, folderID string, root *wbxml.Element) (*EmailSyncResult, error) {
	if st := topStatus(root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Sync", Code: st}
	}
	collection := root.Find("Collection")
	if collection == nil {
		return nil, errors.New("eas: Sync: response missing <Collection>")
	}
	if cs := findShallow(collection, "Status", 1); cs != nil {
		if code := atoi(cs.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Sync", Code: code}
		}
	}
	res := &EmailSyncResult{}
	if k := findShallow(collection, "SyncKey", 1); k != nil {
		res.SyncKey = k.TextContent()
	}
	if findShallow(collection, "MoreAvailable", 1) != nil {
		res.MoreAvailable = true
	}
	if cmds := findShallow(collection, "Commands", 1); cmds != nil {
		for _, c := range cmds.Children {
			el, ok := c.(*wbxml.Element)
			if !ok {
				continue
			}
			switch el.Name {
			case "Add":
				res.Added = append(res.Added, parseAddOrChange(el))
			case "Change":
				res.Changed = append(res.Changed, parseAddOrChange(el))
			case "Delete", "SoftDelete":
				if id := el.Find("ServerId"); id != nil {
					res.Deleted = append(res.Deleted, id.TextContent())
				}
			}
		}
	}
	if res.SyncKey == "" {
		return nil, errors.New("eas: Sync: response missing <SyncKey>")
	}
	if err := c.cfg.State.SetSyncKey(ctx, folderID, res.SyncKey); err != nil {
		return nil, fmt.Errorf("eas: Sync: persist key: %w", err)
	}
	return res, nil
}

func parseAddOrChange(el *wbxml.Element) EmailItem {
	var serverID string
	if id := el.Find("ServerId"); id != nil {
		serverID = id.TextContent()
	}
	app := el.Find("ApplicationData")
	return parseEmailItem(serverID, app)
}

// itoa is a tiny stdlib-free int-to-decimal converter. Exists so we can
// keep the eas package import list lean.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
