// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/wbxml"
)

// MeetingResponseChoice is a UserResponse value per MS-ASCMD §2.2.3.196.
type MeetingResponseChoice int

const (
	MeetingAccept    MeetingResponseChoice = 1
	MeetingTentative MeetingResponseChoice = 2
	MeetingDecline   MeetingResponseChoice = 3
)

// MeetingResponseResult reports a CalendarId assignment if the response
// produced a calendar event (typical for accepts).
type MeetingResponseResult struct {
	CalendarID string
	Status     int
}

// RespondInvite issues a MeetingResponse against an invite item that
// landed in the user's inbox. folderID/serverID identify the invite
// message; choice is accept/tentative/decline.
func (c *httpClient) RespondInvite(ctx context.Context, folderID, serverID string, choice MeetingResponseChoice) (*MeetingResponseResult, error) {
	if folderID == "" || serverID == "" {
		return nil, errors.New("eas: RespondInvite: folderID and serverID are required")
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageMeetingResponse, "MeetingResponse",
			wbxml.E(wbxml.PageMeetingResponse, "Request",
				wbxml.E(wbxml.PageMeetingResponse, "UserResponse", wbxml.Text(itoa(int(choice)))),
				wbxml.E(wbxml.PageMeetingResponse, "CollectionId", wbxml.Text(folderID)),
				wbxml.E(wbxml.PageMeetingResponse, "RequestId", wbxml.Text(serverID)),
			),
		),
	}
	resp, err := c.post(ctx, "MeetingResponse", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: MeetingResponse: empty response")
	}
	result := resp.Root.Find("Result")
	if result == nil {
		// Some servers report only a top-level Status on success.
		if st := topStatus(resp.Root); st != 0 && st != StatusOK {
			return nil, &StatusError{Command: "MeetingResponse", Code: st}
		}
		return &MeetingResponseResult{Status: StatusOK}, nil
	}
	out := &MeetingResponseResult{}
	if cid := result.Find("CalendarId"); cid != nil {
		out.CalendarID = cid.TextContent()
	}
	if st := result.Find("Status"); st != nil {
		out.Status = atoi(st.TextContent())
	}
	if out.Status != 0 && out.Status != StatusOK {
		return out, &StatusError{Command: "MeetingResponse", Code: out.Status}
	}
	if out.Status == 0 {
		out.Status = StatusOK
	}
	return out, nil
}
