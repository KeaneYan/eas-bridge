// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// ContactItem is a parsed contact from a Sync or Fetch response.
type ContactItem struct {
	ServerID string

	FirstName  string
	LastName   string
	MiddleName string
	Title      string
	Suffix     string
	FileAs     string

	CompanyName    string
	Department     string
	JobTitle       string
	OfficeLocation string

	Email1Address string
	Email2Address string
	Email3Address string

	HomePhone     string
	BusinessPhone string
	MobilePhone   string

	HomeAddress     ContactAddress
	BusinessAddress ContactAddress

	Birthday    time.Time
	Anniversary time.Time

	WebPage string

	// Picture is base64-encoded JPEG bytes of the contact photo. The
	// server typically caps these at 36 KB. On round-trip the bytes are
	// base64-encoded for transport but stored decoded here.
	Picture []byte

	// Categories is the user's freeform list of tags ("Work", "Family").
	Categories []string

	// Contacts2 (12.x) extras.
	NickName     string
	IMAddress    string
	IMAddress2   string
	IMAddress3   string
	ManagerName  string
	GovernmentID string
	CustomerID   string
}

// ContactAddress is a US-style address.
type ContactAddress struct {
	Street     string
	City       string
	State      string
	PostalCode string
	Country    string
}

// ContactDraft is the input to CreateContact / UpdateContact.
type ContactDraft = ContactItem

// ContactsSyncResult is the parsed output of a contacts Sync.
type ContactsSyncResult struct {
	SyncKey       string
	MoreAvailable bool
	Added         []ContactItem
	Changed       []ContactItem
	Deleted       []string
}

// SyncContacts issues a Sync command for a contacts folder.
func (c *httpClient) SyncContacts(ctx context.Context, folderID string) (*ContactsSyncResult, error) {
	if folderID == "" {
		return nil, errors.New("eas: SyncContacts: folderID is required")
	}
	out := &ContactsSyncResult{}
	key, more, err := c.genericSyncFolder(ctx, folderID,
		func(id string, app *wbxml.Element) {
			out.Added = append(out.Added, parseContactItem(id, app))
		},
		func(id string, app *wbxml.Element) {
			out.Changed = append(out.Changed, parseContactItem(id, app))
		},
		func(id string) {
			out.Deleted = append(out.Deleted, id)
		},
	)
	if err != nil {
		return nil, err
	}
	out.SyncKey = key
	out.MoreAvailable = more
	return out, nil
}

// CreateContact adds a new contact.
func (c *httpClient) CreateContact(ctx context.Context, folderID string, draft ContactDraft) (string, error) {
	return c.addItemViaSync(ctx, folderID, buildContactApp(draft))
}

// UpdateContact modifies an existing contact.
func (c *httpClient) UpdateContact(ctx context.Context, folderID, serverID string, draft ContactDraft) error {
	return c.changeItemViaSync(ctx, folderID, serverID, buildContactApp(draft))
}

// DeleteContact removes a contact.
func (c *httpClient) DeleteContact(ctx context.Context, folderID, serverID string) error {
	return c.deleteItemViaSync(ctx, folderID, serverID)
}

func parseContactItem(serverID string, app *wbxml.Element) ContactItem {
	out := ContactItem{ServerID: serverID}
	if app == nil {
		return out
	}
	for _, c := range app.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Codepage != wbxml.PageContacts {
			continue
		}
		switch el.Name {
		case "FirstName":
			out.FirstName = el.TextContent()
		case "LastName":
			out.LastName = el.TextContent()
		case "MiddleName":
			out.MiddleName = el.TextContent()
		case "Title":
			out.Title = el.TextContent()
		case "Suffix":
			out.Suffix = el.TextContent()
		case "FileAs":
			out.FileAs = el.TextContent()
		case "CompanyName":
			out.CompanyName = el.TextContent()
		case "Department":
			out.Department = el.TextContent()
		case "JobTitle":
			out.JobTitle = el.TextContent()
		case "OfficeLocation":
			out.OfficeLocation = el.TextContent()
		case "Email1Address":
			out.Email1Address = el.TextContent()
		case "Email2Address":
			out.Email2Address = el.TextContent()
		case "Email3Address":
			out.Email3Address = el.TextContent()
		case "HomePhoneNumber":
			out.HomePhone = el.TextContent()
		case "BusinessPhoneNumber":
			out.BusinessPhone = el.TextContent()
		case "MobilePhoneNumber":
			out.MobilePhone = el.TextContent()
		case "HomeAddressStreet":
			out.HomeAddress.Street = el.TextContent()
		case "HomeAddressCity":
			out.HomeAddress.City = el.TextContent()
		case "HomeAddressState":
			out.HomeAddress.State = el.TextContent()
		case "HomeAddressPostalCode":
			out.HomeAddress.PostalCode = el.TextContent()
		case "HomeAddressCountry":
			out.HomeAddress.Country = el.TextContent()
		case "BusinessAddressStreet":
			out.BusinessAddress.Street = el.TextContent()
		case "BusinessAddressCity":
			out.BusinessAddress.City = el.TextContent()
		case "BusinessAddressState":
			out.BusinessAddress.State = el.TextContent()
		case "BusinessAddressPostalCode":
			out.BusinessAddress.PostalCode = el.TextContent()
		case "BusinessAddressCountry":
			out.BusinessAddress.Country = el.TextContent()
		case "Birthday":
			out.Birthday, _ = parseEASTime(el.TextContent())
		case "Anniversary":
			out.Anniversary, _ = parseEASTime(el.TextContent())
		case "WebPage":
			out.WebPage = el.TextContent()
		case "Picture":
			// Server returns base64-encoded JPEG as text content.
			if t := el.TextContent(); t != "" {
				if dec, err := base64.StdEncoding.DecodeString(t); err == nil {
					out.Picture = dec
				}
			}
		case "Categories":
			for _, cc := range el.Children {
				if ce, ok := cc.(*wbxml.Element); ok && ce.Name == "Category" {
					out.Categories = append(out.Categories, ce.TextContent())
				}
			}
		}
	}
	// Contacts2 extras come on a different codepage; walk again.
	for _, c := range app.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Codepage != wbxml.PageContacts2 {
			continue
		}
		switch el.Name {
		case "NickName":
			out.NickName = el.TextContent()
		case "IMAddress":
			out.IMAddress = el.TextContent()
		case "IMAddress2":
			out.IMAddress2 = el.TextContent()
		case "IMAddress3":
			out.IMAddress3 = el.TextContent()
		case "ManagerName":
			out.ManagerName = el.TextContent()
		case "GovernmentId":
			out.GovernmentID = el.TextContent()
		case "CustomerId":
			out.CustomerID = el.TextContent()
		}
	}
	return out
}

func buildContactApp(d ContactDraft) *wbxml.Element {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData")
	add := func(name, val string) {
		if val == "" {
			return
		}
		app.Children = append(app.Children, wbxml.E(wbxml.PageContacts, name, wbxml.Text(val)))
	}
	add("FirstName", d.FirstName)
	add("LastName", d.LastName)
	add("MiddleName", d.MiddleName)
	add("Title", d.Title)
	add("Suffix", d.Suffix)
	add("FileAs", d.FileAs)
	add("CompanyName", d.CompanyName)
	add("Department", d.Department)
	add("JobTitle", d.JobTitle)
	add("OfficeLocation", d.OfficeLocation)
	add("Email1Address", d.Email1Address)
	add("Email2Address", d.Email2Address)
	add("Email3Address", d.Email3Address)
	add("HomePhoneNumber", d.HomePhone)
	add("BusinessPhoneNumber", d.BusinessPhone)
	add("MobilePhoneNumber", d.MobilePhone)
	add("HomeAddressStreet", d.HomeAddress.Street)
	add("HomeAddressCity", d.HomeAddress.City)
	add("HomeAddressState", d.HomeAddress.State)
	add("HomeAddressPostalCode", d.HomeAddress.PostalCode)
	add("HomeAddressCountry", d.HomeAddress.Country)
	add("BusinessAddressStreet", d.BusinessAddress.Street)
	add("BusinessAddressCity", d.BusinessAddress.City)
	add("BusinessAddressState", d.BusinessAddress.State)
	add("BusinessAddressPostalCode", d.BusinessAddress.PostalCode)
	add("BusinessAddressCountry", d.BusinessAddress.Country)
	add("WebPage", d.WebPage)
	if !d.Birthday.IsZero() {
		add("Birthday", formatEASTime(d.Birthday))
	}
	if !d.Anniversary.IsZero() {
		add("Anniversary", formatEASTime(d.Anniversary))
	}
	if len(d.Picture) > 0 {
		app.Children = append(app.Children, wbxml.E(wbxml.PageContacts, "Picture",
			wbxml.Text(base64.StdEncoding.EncodeToString(d.Picture))))
	}
	if len(d.Categories) > 0 {
		cats := wbxml.E(wbxml.PageContacts, "Categories")
		for _, c := range d.Categories {
			cats.Children = append(cats.Children, wbxml.E(wbxml.PageContacts, "Category", wbxml.Text(c)))
		}
		app.Children = append(app.Children, cats)
	}
	addC2 := func(name, val string) {
		if val == "" {
			return
		}
		app.Children = append(app.Children, wbxml.E(wbxml.PageContacts2, name, wbxml.Text(val)))
	}
	addC2("NickName", d.NickName)
	addC2("IMAddress", d.IMAddress)
	addC2("IMAddress2", d.IMAddress2)
	addC2("IMAddress3", d.IMAddress3)
	addC2("ManagerName", d.ManagerName)
	addC2("GovernmentId", d.GovernmentID)
	addC2("CustomerId", d.CustomerID)
	return app
}

// --- shared item-mutation helpers (Contacts, Tasks, Notes) -----------------

// addItemViaSync sends an Add command via Sync and returns the
// server-assigned ServerID.
func (c *httpClient) addItemViaSync(ctx context.Context, folderID string, app *wbxml.Element) (string, error) {
	if folderID == "" {
		return "", errors.New("eas: addItemViaSync: folderID is required")
	}
	clientID := orRandomID("")
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Add",
			wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(clientID)),
			app,
		),
	)
	resp, err := c.sendSyncCommandsWithReset(ctx, folderID, cmds)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return clientID, nil
	}
	if collection := resp.Find("Collection"); collection != nil {
		if responses := findShallow(collection, "Responses", 1); responses != nil {
			for _, r := range responses.Children {
				re, ok := r.(*wbxml.Element)
				if !ok || re.Name != "Add" {
					continue
				}
				if cid := re.Find("ClientId"); cid != nil && cid.TextContent() == clientID {
					if sid := re.Find("ServerId"); sid != nil {
						return sid.TextContent(), nil
					}
				}
			}
		}
	}
	return clientID, nil
}

func (c *httpClient) changeItemViaSync(ctx context.Context, folderID, serverID string, app *wbxml.Element) error {
	if folderID == "" || serverID == "" {
		return errors.New("eas: changeItemViaSync: folderID and serverID are required")
	}
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Change",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
			app,
		),
	)
	_, err := c.sendSyncCommandsWithReset(ctx, folderID, cmds)
	return err
}

func (c *httpClient) deleteItemViaSync(ctx context.Context, folderID, serverID string) error {
	if folderID == "" || serverID == "" {
		return errors.New("eas: deleteItemViaSync: folderID and serverID are required")
	}
	cmds := wbxml.E(wbxml.PageAirSync, "Commands",
		wbxml.E(wbxml.PageAirSync, "Delete",
			wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
		),
	)
	_, err := c.sendSyncCommandsWithReset(ctx, folderID, cmds)
	return err
}

// sendSyncCommandsWithReset wraps sendSyncCommands with the same
// bootstrap + InvalidSyncKey reset-and-retry semantics SyncCalendar
// and ApplyEmailChanges already use. Without this, a stale SyncKey
// (e.g. another device on the same account rotated state) made every
// CreateEvent / UpdateContact / DeleteTask hard-fail until the caller
// manually reset and re-bootstrapped.
func (c *httpClient) sendSyncCommandsWithReset(ctx context.Context, folderID string, cmds *wbxml.Element) (*wbxml.Element, error) {
	if err := c.ensureSynced(ctx, folderID); err != nil {
		return nil, err
	}
	resp, err := c.sendSyncCommands(ctx, folderID, cmds)
	if err == nil || !IsStatusCode(err, StatusInvalidSyncKey) {
		return resp, err
	}
	if rerr := c.cfg.State.SetSyncKey(ctx, folderID, "0"); rerr != nil {
		return nil, fmt.Errorf("eas: reset sync key: %w", rerr)
	}
	if rerr := c.ensureSynced(ctx, folderID); rerr != nil {
		return nil, rerr
	}
	return c.sendSyncCommands(ctx, folderID, cmds)
}

// genericSyncFolder issues a Sync request for a non-email folder class
// and dispatches each Add/Change to the per-class parser. Returns the
// SyncKey, more-available flag, and parsed items.
func (c *httpClient) genericSyncFolder(
	ctx context.Context,
	folderID string,
	parseAdd func(serverID string, app *wbxml.Element),
	parseChange func(serverID string, app *wbxml.Element),
	parseDelete func(serverID string),
) (string, bool, error) {
	key, err := c.cfg.State.SyncKey(ctx, folderID)
	if err != nil {
		return "", false, fmt.Errorf("eas: genericSyncFolder: read key: %w", err)
	}
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(key)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		wbxml.E(wbxml.PageAirSync, "DeletesAsMoves", wbxml.Text("1")),
		wbxml.E(wbxml.PageAirSync, "GetChanges", wbxml.Text("1")),
		wbxml.E(wbxml.PageAirSync, "WindowSize", wbxml.Text("100")),
	)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	resp, err := c.post(ctx, "Sync", doc)
	if err != nil {
		return "", false, err
	}
	if resp == nil || resp.Root == nil {
		return key, false, nil
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return "", false, &StatusError{Command: "Sync", Code: st}
	}
	respCollection := resp.Root.Find("Collection")
	if respCollection == nil {
		return "", false, errors.New("eas: genericSyncFolder: missing <Collection>")
	}
	if cs := findShallow(respCollection, "Status", 1); cs != nil {
		if code := atoi(cs.TextContent()); code != 0 && code != StatusOK {
			return "", false, &StatusError{Command: "Sync", Code: code}
		}
	}
	newKey := key
	if k := findShallow(respCollection, "SyncKey", 1); k != nil {
		newKey = k.TextContent()
	}
	more := findShallow(respCollection, "MoreAvailable", 1) != nil

	if cmds := findShallow(respCollection, "Commands", 1); cmds != nil {
		for _, c := range cmds.Children {
			el, ok := c.(*wbxml.Element)
			if !ok {
				continue
			}
			id := ""
			if sid := el.Find("ServerId"); sid != nil {
				id = sid.TextContent()
			}
			switch el.Name {
			case "Add":
				if app := el.Find("ApplicationData"); app != nil {
					parseAdd(id, app)
				}
			case "Change":
				if app := el.Find("ApplicationData"); app != nil {
					parseChange(id, app)
				}
			case "Delete", "SoftDelete":
				parseDelete(id)
			}
		}
	}
	if newKey == "" {
		return "", false, errors.New("eas: genericSyncFolder: missing SyncKey")
	}
	if err := c.cfg.State.SetSyncKey(ctx, folderID, newKey); err != nil {
		return "", false, fmt.Errorf("eas: genericSyncFolder: persist: %w", err)
	}
	return newKey, more, nil
}
