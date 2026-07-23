// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// PingFolder describes one folder + class to subscribe to in a Ping
// request. Class is "Email", "Calendar", "Contacts", "Tasks", or "Notes".
type PingFolder struct {
	ID    string
	Class string
}

// PingResult is the parsed Ping response.
type PingResult struct {
	// Status is the EAS Ping status code (1=OK no changes, 2=changes
	// available, 3=heartbeat too short, 4=heartbeat too long, 5=too many
	// folders, 6=missing parameters, 7=folder hierarchy out of date,
	// 8=server error).
	Status int
	// ChangedFolders lists the folder IDs the server reports have
	// pending changes; populated when Status=2.
	ChangedFolders []string
	// HeartbeatInterval is the server-recommended heartbeat in seconds;
	// non-zero on Status 3 or 4 to indicate the acceptable range.
	HeartbeatInterval int
}

// Ping issues a Ping command. The HTTP request blocks on the server for
// up to heartbeatSeconds before returning. The caller is responsible for
// scheduling the next Ping after this one returns; use a heartbeat of
// 60-470 seconds (the typical server cap is ~470).
func (c *httpClient) Ping(ctx context.Context, heartbeatSeconds int, folders []PingFolder) (*PingResult, error) {
	if len(folders) == 0 {
		return nil, errors.New("eas: Ping: at least one folder required")
	}
	if heartbeatSeconds <= 0 {
		heartbeatSeconds = 60
	}
	foldersEl := wbxml.E(wbxml.PagePing, "Folders")
	for _, f := range folders {
		foldersEl.Children = append(foldersEl.Children, wbxml.E(wbxml.PagePing, "Folder",
			wbxml.E(wbxml.PagePing, "Id", wbxml.Text(f.ID)),
			wbxml.E(wbxml.PagePing, "Class", wbxml.Text(f.Class)),
		))
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PagePing, "Ping",
			wbxml.E(wbxml.PagePing, "HeartbeatInterval", wbxml.Text(itoa(heartbeatSeconds))),
			foldersEl,
		),
	}
	resp, err := c.post(ctx, "Ping", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: Ping: empty response")
	}
	out := &PingResult{}
	if st := resp.Root.Find("Status"); st != nil {
		out.Status = atoi(st.TextContent())
	}
	if hb := resp.Root.Find("HeartbeatInterval"); hb != nil {
		out.HeartbeatInterval = atoi(hb.TextContent())
	}
	if folders := resp.Root.Find("Folders"); folders != nil {
		for _, c := range folders.Children {
			el, ok := c.(*wbxml.Element)
			if !ok {
				continue
			}
			// MS-ASCMD §2.2.2.11.2 says <Folder> is a leaf with the
			// folder ID as its text. Some implementations (rare) wrap
			// the ID in an explicit <Id> child instead. Handle both.
			switch el.Name {
			case "Folder":
				if id := el.Find("Id"); id != nil {
					out.ChangedFolders = append(out.ChangedFolders, id.TextContent())
				} else if t := el.TextContent(); t != "" {
					out.ChangedFolders = append(out.ChangedFolders, t)
				}
			case "Id":
				out.ChangedFolders = append(out.ChangedFolders, el.TextContent())
			}
		}
	}
	switch out.Status {
	case 1, 2:
		// Success cases.
	case 0:
		return nil, errors.New("eas: Ping: response missing Status")
	default:
		return out, fmt.Errorf("eas: Ping: status %d", out.Status)
	}
	return out, nil
}
