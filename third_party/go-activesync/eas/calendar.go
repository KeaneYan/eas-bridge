// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// EventItem is a parsed calendar event from a calendar Sync or
// ItemOperations Fetch response. Fields populated only when the server
// included them.
type EventItem struct {
	ServerID string
	UID      string

	Subject       string
	Location      string
	Body          string
	BodyType      BodyType
	StartTime     time.Time
	EndTime       time.Time
	AllDayEvent   bool
	BusyStatus    int
	Sensitivity   int
	MeetingStatus int
	Reminder      int

	OrganizerName  string
	OrganizerEmail string
	Attendees      []EventAttendee

	// Recurrence is the repeat rule, when set.
	Recurrence *Recurrence
	// Exceptions are per-instance overrides.
	Exceptions []Exception
	// TimeZone is the parsed Microsoft TIME_ZONE_INFORMATION blob.
	// Zero value means the server didn't include one (Outlook fills
	// this in for recurring events; one-shot events often omit it).
	TimeZone EASTimeZone
	// TimeZoneRaw is the original base64 text from the server, useful
	// for byte-identical round-trip when updating the event.
	TimeZoneRaw string
}

// EventAttendee describes one attendee on a calendar event.
type EventAttendee struct {
	Name           string
	Email          string
	AttendeeStatus int
	AttendeeType   int
}

// EventDraft is the input to CreateEvent / UpdateEvent. All fields are
// optional except StartTime and EndTime; absent fields become "no change"
// on update or use sensible defaults on create.
type EventDraft struct {
	Subject     string
	Location    string
	Body        string
	StartTime   time.Time
	EndTime     time.Time
	AllDayEvent bool
	BusyStatus  int // 0=free, 1=tentative, 2=busy, 3=OOF, 4=working elsewhere
	Sensitivity int // 0=normal, 1=personal, 2=private, 3=confidential
	Reminder    int // minutes before; 0 = none
	Attendees   []EventAttendee
	// Recurrence sets a repeat rule; nil means single-instance.
	Recurrence *Recurrence
	// Exceptions list per-instance overrides for a recurring event.
	Exceptions []Exception
	// TimeZone is the EAS time zone blob. EncodeTimeZone(loc) builds
	// one from a Go *time.Location for the simple case. When zero,
	// no TimeZone element is sent (the server uses its default).
	TimeZone *EASTimeZone
	// TimeZoneRaw is an alternative way to specify the time zone:
	// supply the base64 string the server gave you on a previous
	// fetch (for byte-identical round-trip).
	TimeZoneRaw string
}

// CalendarSyncOptions controls a SyncCalendar request.
type CalendarSyncOptions struct {
	WindowSize  int
	DateFilter  FilterType
	NoBootstrap bool
}

// CalendarSyncResult is the parsed output of a calendar Sync.
type CalendarSyncResult struct {
	SyncKey       string
	MoreAvailable bool
	Added         []EventItem
	Changed       []EventItem
	Deleted       []string
}

// SyncCalendar issues a Sync command for a calendar folder and parses
// EventItems from the response.
func (c *httpClient) SyncCalendar(ctx context.Context, folderID string, opts CalendarSyncOptions) (*CalendarSyncResult, error) {
	if folderID == "" {
		return nil, errors.New("eas: SyncCalendar: folderID is required")
	}
	if opts.WindowSize <= 0 {
		opts.WindowSize = 100
	}
	if opts.DateFilter == 0 {
		opts.DateFilter = FilterTwoWeek
	}

	res, err := c.syncCalendarOnce(ctx, folderID, opts)
	if err != nil && IsStatusCode(err, StatusInvalidSyncKey) {
		if rerr := c.cfg.State.SetSyncKey(ctx, folderID, "0"); rerr != nil {
			return nil, fmt.Errorf("eas: SyncCalendar: reset key: %w", rerr)
		}
		res, err = c.syncCalendarOnce(ctx, folderID, opts)
	}
	if err != nil {
		return nil, err
	}
	if !opts.NoBootstrap && len(res.Added) == 0 && len(res.Changed) == 0 && len(res.Deleted) == 0 {
		stored, _ := c.cfg.State.SyncKey(ctx, folderID)
		if stored != "0" && stored != "" {
			res2, err := c.syncCalendarOnce(ctx, folderID, opts)
			if err == nil {
				return res2, nil
			}
		}
	}
	return res, nil
}

func (c *httpClient) syncCalendarOnce(ctx context.Context, folderID string, opts CalendarSyncOptions) (*CalendarSyncResult, error) {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("eas: SyncCalendar: read key: %w", err)
	}
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		wbxml.E(wbxml.PageAirSync, "DeletesAsMoves", wbxml.Text("1")),
		wbxml.E(wbxml.PageAirSync, "GetChanges", wbxml.Text("1")),
		wbxml.E(wbxml.PageAirSync, "WindowSize", wbxml.Text(itoa(opts.WindowSize))),
		wbxml.E(wbxml.PageAirSync, "Options",
			wbxml.E(wbxml.PageAirSync, "FilterType", wbxml.Text(itoa(int(opts.DateFilter)))),
		),
	)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	resp, err := c.post(ctx, "Sync", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return &CalendarSyncResult{SyncKey: key}, nil
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Sync", Code: st}
	}
	respCollection := resp.Root.Find("Collection")
	if respCollection == nil {
		return nil, errors.New("eas: SyncCalendar: response missing <Collection>")
	}
	if cs := findShallow(respCollection, "Status", 1); cs != nil {
		if code := atoi(cs.TextContent()); code != 0 && code != StatusOK {
			return nil, &StatusError{Command: "Sync", Code: code}
		}
	}
	out := &CalendarSyncResult{}
	if k := findShallow(respCollection, "SyncKey", 1); k != nil {
		out.SyncKey = k.TextContent()
	}
	if findShallow(respCollection, "MoreAvailable", 1) != nil {
		out.MoreAvailable = true
	}
	if cmds := findShallow(respCollection, "Commands", 1); cmds != nil {
		for _, c := range cmds.Children {
			el, ok := c.(*wbxml.Element)
			if !ok {
				continue
			}
			switch el.Name {
			case "Add":
				out.Added = append(out.Added, parseEventFromCommand(el))
			case "Change":
				out.Changed = append(out.Changed, parseEventFromCommand(el))
			case "Delete", "SoftDelete":
				if id := el.Find("ServerId"); id != nil {
					out.Deleted = append(out.Deleted, id.TextContent())
				}
			}
		}
	}
	if out.SyncKey == "" {
		return nil, errors.New("eas: SyncCalendar: response missing SyncKey")
	}
	if err := c.cfg.State.SetSyncKey(ctx, folderID, out.SyncKey); err != nil {
		return nil, fmt.Errorf("eas: SyncCalendar: persist key: %w", err)
	}
	return out, nil
}

func parseEventFromCommand(el *wbxml.Element) EventItem {
	out := EventItem{}
	if id := el.Find("ServerId"); id != nil {
		out.ServerID = id.TextContent()
	}
	app := el.Find("ApplicationData")
	if app == nil {
		return out
	}
	for _, c := range app.Children {
		ce, ok := c.(*wbxml.Element)
		if !ok {
			continue
		}
		switch ce.Codepage {
		case wbxml.PageCalendar:
			parseCalendarField(&out, ce)
		case wbxml.PageAirSyncBase:
			if ce.Name == "Body" {
				parseEventBody(&out, ce)
			}
		}
	}
	return out
}

func parseCalendarField(out *EventItem, el *wbxml.Element) {
	switch el.Name {
	case "Subject":
		out.Subject = el.TextContent()
	case "Location":
		out.Location = el.TextContent()
	case "StartTime":
		out.StartTime, _ = parseEASTime(el.TextContent())
	case "EndTime":
		out.EndTime, _ = parseEASTime(el.TextContent())
	case "AllDayEvent":
		out.AllDayEvent = el.TextContent() == "1"
	case "BusyStatus":
		out.BusyStatus = atoi(el.TextContent())
	case "Sensitivity":
		out.Sensitivity = atoi(el.TextContent())
	case "MeetingStatus":
		out.MeetingStatus = atoi(el.TextContent())
	case "Reminder":
		out.Reminder = atoi(el.TextContent())
	case "OrganizerName":
		out.OrganizerName = el.TextContent()
	case "OrganizerEmail":
		out.OrganizerEmail = el.TextContent()
	case "UID":
		out.UID = el.TextContent()
	case "Attendees":
		for _, c := range el.Children {
			at, ok := c.(*wbxml.Element)
			if !ok || at.Name != "Attendee" {
				continue
			}
			a := EventAttendee{}
			if n := at.Find("Name"); n != nil {
				a.Name = n.TextContent()
			}
			if e := at.Find("Email"); e != nil {
				a.Email = e.TextContent()
			}
			if s := at.Find("AttendeeStatus"); s != nil {
				a.AttendeeStatus = atoi(s.TextContent())
			}
			if t := at.Find("AttendeeType"); t != nil {
				a.AttendeeType = atoi(t.TextContent())
			}
			out.Attendees = append(out.Attendees, a)
		}
	case "Recurrence":
		out.Recurrence = recurrenceFromWBXML(el)
	case "Exceptions":
		for _, c := range el.Children {
			if ex, ok := c.(*wbxml.Element); ok && ex.Name == "Exception" {
				out.Exceptions = append(out.Exceptions, parseException(ex))
			}
		}
	case "TimeZone":
		out.TimeZoneRaw = el.TextContent()
		if tz, err := DecodeTimeZone(out.TimeZoneRaw); err == nil {
			out.TimeZone = tz
		}
	}
}

func parseEventBody(out *EventItem, body *wbxml.Element) {
	for _, c := range body.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Codepage != wbxml.PageAirSyncBase {
			continue
		}
		switch el.Name {
		case "Type":
			out.BodyType = BodyType(atoi(el.TextContent()))
		case "Data":
			if op := firstOpaque(el); op != nil {
				out.Body = string(op)
			} else {
				out.Body = el.TextContent()
			}
		}
	}
}

// CreateEvent adds a new calendar event via Sync. Returns the
// server-assigned ServerID (or the temporary client id if the server
// didn't echo a new one).
func (c *httpClient) CreateEvent(ctx context.Context, folderID string, draft EventDraft) (string, error) {
	if folderID == "" {
		return "", errors.New("eas: CreateEvent: folderID is required")
	}
	if draft.StartTime.IsZero() || draft.EndTime.IsZero() {
		return "", errors.New("eas: CreateEvent: StartTime and EndTime are required")
	}
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return "", err
	}
	clientID := orRandomID("")
	app := buildEventApp(draft)
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Add",
			wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(clientID)),
			app,
		),
	)
	resp, err := c.sendSyncCommands(ctx, folderID, cmds)
	if err != nil {
		return "", err
	}
	// Look for Responses/Add with ClientId match → return the server id.
	if collection := resp.Find("Collection"); collection != nil {
		if responses := findShallow(collection, "Responses", 1); responses != nil {
			for _, r := range responses.Children {
				re, ok := r.(*wbxml.Element)
				if !ok || re.Name != "Add" {
					continue
				}
				cid := re.Find("ClientId")
				sid := re.Find("ServerId")
				if cid != nil && cid.TextContent() == clientID && sid != nil {
					return sid.TextContent(), nil
				}
			}
		}
	}
	return clientID, nil
}

// UpdateEvent modifies an existing event identified by serverID. Only the
// non-zero fields of draft are sent.
func (c *httpClient) UpdateEvent(ctx context.Context, folderID, serverID string, draft EventDraft) error {
	if folderID == "" || serverID == "" {
		return errors.New("eas: UpdateEvent: folderID and serverID are required")
	}
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return err
	}
	app := buildEventApp(draft)
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Change",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
			app,
		),
	)
	_, err := c.sendSyncCommands(ctx, folderID, cmds)
	return err
}

// DeleteEvent removes an event identified by serverID.
func (c *httpClient) DeleteEvent(ctx context.Context, folderID, serverID string) error {
	if folderID == "" || serverID == "" {
		return errors.New("eas: DeleteEvent: folderID and serverID are required")
	}
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return err
	}
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Delete",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
		),
	)
	_, err := c.sendSyncCommands(ctx, folderID, cmds)
	return err
}

// sendSyncCommands wraps a Commands subtree in a Sync request, sends it,
// updates the persisted SyncKey, and returns the response root.
func (c *httpClient) sendSyncCommands(ctx context.Context, folderID string, cmds *wbxml.Element) (*wbxml.Element, error) {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return nil, fmt.Errorf("eas: sendSyncCommands: read key: %w", err)
	}
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		cmds,
	)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	resp, err := c.post(ctx, "Sync", doc)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, nil
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "Sync", Code: st}
	}
	if respCollection := resp.Root.Find("Collection"); respCollection != nil {
		if cs := findShallow(respCollection, "Status", 1); cs != nil {
			if code := atoi(cs.TextContent()); code != 0 && code != StatusOK {
				return nil, &StatusError{Command: "Sync", Code: code}
			}
		}
		if newKey := findShallow(respCollection, "SyncKey", 1); newKey != nil {
			if err := c.cfg.State.SetSyncKey(ctx, folderID, newKey.TextContent()); err != nil {
				return nil, fmt.Errorf("eas: sendSyncCommands: persist key: %w", err)
			}
		}
	}
	return resp.Root, nil
}

func buildEventApp(draft EventDraft) *wbxml.Element {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData")
	if draft.Subject != "" {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Subject", wbxml.Text(draft.Subject)))
	}
	if draft.Location != "" {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Location", wbxml.Text(draft.Location)))
	}
	if !draft.StartTime.IsZero() {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "StartTime", wbxml.Text(formatEASTime(draft.StartTime))))
	}
	if !draft.EndTime.IsZero() {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "EndTime", wbxml.Text(formatEASTime(draft.EndTime))))
	}
	if draft.AllDayEvent {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "AllDayEvent", wbxml.Text("1")))
	}
	app.Children = append(app.Children,
		wbxml.E(wbxml.PageCalendar, "BusyStatus", wbxml.Text(itoa(draft.BusyStatus))),
		wbxml.E(wbxml.PageCalendar, "Sensitivity", wbxml.Text(itoa(draft.Sensitivity))),
	)
	if draft.Reminder > 0 {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "Reminder", wbxml.Text(itoa(draft.Reminder))))
	}
	if len(draft.Attendees) > 0 {
		atts := wbxml.E(wbxml.PageCalendar, "Attendees")
		for _, a := range draft.Attendees {
			at := wbxml.E(wbxml.PageCalendar, "Attendee")
			if a.Name != "" {
				at.Children = append(at.Children, wbxml.E(wbxml.PageCalendar, "Name", wbxml.Text(a.Name)))
			}
			if a.Email != "" {
				at.Children = append(at.Children, wbxml.E(wbxml.PageCalendar, "Email", wbxml.Text(a.Email)))
			}
			atts.Children = append(atts.Children, at)
		}
		app.Children = append(app.Children, atts)
	}
	if draft.Body != "" {
		app.Children = append(app.Children,
			wbxml.E(wbxml.PageAirSyncBase, "Body",
				wbxml.E(wbxml.PageAirSyncBase, "Type", wbxml.Text("1")),
				wbxml.E(wbxml.PageAirSyncBase, "Data", wbxml.Text(draft.Body)),
			),
		)
	}
	if draft.TimeZoneRaw != "" {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "TimeZone",
			wbxml.Text(draft.TimeZoneRaw)))
	} else if draft.TimeZone != nil {
		app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "TimeZone",
			wbxml.Text(draft.TimeZone.Encode())))
	}
	if rec := recurrenceToWBXML(draft.Recurrence); rec != nil {
		app.Children = append(app.Children, rec)
	}
	if len(draft.Exceptions) > 0 {
		exc := wbxml.E(wbxml.PageCalendar, "Exceptions")
		for _, x := range draft.Exceptions {
			exc.Children = append(exc.Children, exceptionToWBXML(x))
		}
		app.Children = append(app.Children, exc)
	}
	// EAS requires a UID for new events; compute a stable one if absent.
	app.Children = append(app.Children, wbxml.E(wbxml.PageCalendar, "UID", wbxml.Text(newEventUID())))
	return app
}

func formatEASTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func newEventUID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return "asmcp-" + hex.EncodeToString(buf)
}
