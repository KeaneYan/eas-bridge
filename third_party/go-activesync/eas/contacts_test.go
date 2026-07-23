// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

func TestSyncContacts_emptyFolderRejected(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit")
	})
	if _, err := c.SyncContacts(context.Background(), ""); err == nil {
		t.Error("want error for empty folderID")
	}
}

func TestSyncContacts_parsesItem(t *testing.T) {
	add := wbxml.E(wbxml.PageAirSync, "Add",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("c-1")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageContacts, "FirstName", wbxml.Text("Alice")),
			wbxml.E(wbxml.PageContacts, "LastName", wbxml.Text("Engineer")),
			wbxml.E(wbxml.PageContacts, "Email1Address", wbxml.Text("alice@example.com")),
			wbxml.E(wbxml.PageContacts, "MobilePhoneNumber", wbxml.Text("+1-555-0100")),
		),
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse("contacts", "C1", add))
	})
	res, err := c.SyncContacts(context.Background(), "contacts")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 {
		t.Fatalf("len = %d", len(res.Added))
	}
	if res.Added[0].FirstName != "Alice" || res.Added[0].Email1Address != "alice@example.com" {
		t.Errorf("got %+v", res.Added[0])
	}
}

func TestCreateContact_returnsServerID(t *testing.T) {
	c, bodyP := twoCallSyncServer(t, "contacts", "BOOT", "contacts:NEW")
	id, err := c.CreateContact(context.Background(), "contacts", ContactDraft{
		FirstName: "Alice", LastName: "Engineer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "contacts:NEW" {
		t.Errorf("id = %q", id)
	}
	if !strings.Contains(string(**bodyP), "Engineer") {
		t.Error("body missing last name")
	}
}

func TestUpdateContact_emitsChange(t *testing.T) {
	c, lastBody := singleCallSyncServer(t, "contacts")
	if err := c.UpdateContact(context.Background(), "contacts", "c-1", ContactDraft{
		FirstName: "Alice", LastName: "Updated",
	}); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(*lastBody, wbxml.DefaultRegistry())
	if req.Root.Find("Change") == nil {
		t.Error("request missing <Change>")
	}
	if !strings.Contains(string(*lastBody), "Updated") {
		t.Error("body missing new last name")
	}
}

func TestDeleteContact_emitsDelete(t *testing.T) {
	c, lastBody := singleCallSyncServer(t, "contacts")
	if err := c.DeleteContact(context.Background(), "contacts", "c-1"); err != nil {
		t.Fatal(err)
	}
	req, _ := wbxml.Unmarshal(*lastBody, wbxml.DefaultRegistry())
	if req.Root.Find("Delete") == nil {
		t.Error("request missing <Delete>")
	}
}

func TestParseContactItem_allFields(t *testing.T) {
	// Picture: build a base64 payload the parser will decode.
	pic := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46} // JPEG-ish prefix
	picB64 := "/9j/4AAQSkY="                                      // base64 of pic
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData",
		wbxml.E(wbxml.PageContacts, "FirstName", wbxml.Text("Alice")),
		wbxml.E(wbxml.PageContacts, "LastName", wbxml.Text("Engineer")),
		wbxml.E(wbxml.PageContacts, "MiddleName", wbxml.Text("Q")),
		wbxml.E(wbxml.PageContacts, "Title", wbxml.Text("Dr.")),
		wbxml.E(wbxml.PageContacts, "Suffix", wbxml.Text("PhD")),
		wbxml.E(wbxml.PageContacts, "FileAs", wbxml.Text("Engineer, Alice")),
		wbxml.E(wbxml.PageContacts, "CompanyName", wbxml.Text("Acme")),
		wbxml.E(wbxml.PageContacts, "Department", wbxml.Text("R&D")),
		wbxml.E(wbxml.PageContacts, "JobTitle", wbxml.Text("Staff SWE")),
		wbxml.E(wbxml.PageContacts, "OfficeLocation", wbxml.Text("HQ-3")),
		wbxml.E(wbxml.PageContacts, "Email1Address", wbxml.Text("alice@x")),
		wbxml.E(wbxml.PageContacts, "Email2Address", wbxml.Text("a.engineer@personal")),
		wbxml.E(wbxml.PageContacts, "Email3Address", wbxml.Text("a@old")),
		wbxml.E(wbxml.PageContacts, "HomePhoneNumber", wbxml.Text("+1-555-0100")),
		wbxml.E(wbxml.PageContacts, "BusinessPhoneNumber", wbxml.Text("+1-555-0200")),
		wbxml.E(wbxml.PageContacts, "MobilePhoneNumber", wbxml.Text("+1-555-0300")),
		wbxml.E(wbxml.PageContacts, "HomeAddressStreet", wbxml.Text("1 Home St")),
		wbxml.E(wbxml.PageContacts, "HomeAddressCity", wbxml.Text("Hometown")),
		wbxml.E(wbxml.PageContacts, "HomeAddressState", wbxml.Text("HS")),
		wbxml.E(wbxml.PageContacts, "HomeAddressPostalCode", wbxml.Text("H0H 0H0")),
		wbxml.E(wbxml.PageContacts, "HomeAddressCountry", wbxml.Text("Canada")),
		wbxml.E(wbxml.PageContacts, "BusinessAddressStreet", wbxml.Text("9 Office Blvd")),
		wbxml.E(wbxml.PageContacts, "BusinessAddressCity", wbxml.Text("Worktown")),
		wbxml.E(wbxml.PageContacts, "BusinessAddressState", wbxml.Text("WS")),
		wbxml.E(wbxml.PageContacts, "BusinessAddressPostalCode", wbxml.Text("90210")),
		wbxml.E(wbxml.PageContacts, "BusinessAddressCountry", wbxml.Text("USA")),
		wbxml.E(wbxml.PageContacts, "Birthday", wbxml.Text("1990-04-15T00:00:00.000Z")),
		wbxml.E(wbxml.PageContacts, "Anniversary", wbxml.Text("2018-06-12T00:00:00.000Z")),
		wbxml.E(wbxml.PageContacts, "WebPage", wbxml.Text("https://alice.example")),
		wbxml.E(wbxml.PageContacts, "Picture", wbxml.Text(picB64)),
		wbxml.E(wbxml.PageContacts, "Categories",
			wbxml.E(wbxml.PageContacts, "Category", wbxml.Text("personal")),
			wbxml.E(wbxml.PageContacts, "Category", wbxml.Text("vip")),
		),
		// Wrong codepage should be silently skipped.
		wbxml.E(wbxml.PageEmail, "Subject", wbxml.Text("ignored")),
	)
	got := parseContactItem("c-99", app)
	wantNames := map[string]string{
		"ServerID":       got.ServerID,
		"FirstName":      got.FirstName,
		"LastName":       got.LastName,
		"MiddleName":     got.MiddleName,
		"Title":          got.Title,
		"Suffix":         got.Suffix,
		"FileAs":         got.FileAs,
		"CompanyName":    got.CompanyName,
		"Department":     got.Department,
		"JobTitle":       got.JobTitle,
		"OfficeLocation": got.OfficeLocation,
		"Email1Address":  got.Email1Address,
		"Email2Address":  got.Email2Address,
		"Email3Address":  got.Email3Address,
		"HomePhone":      got.HomePhone,
		"BusinessPhone":  got.BusinessPhone,
		"MobilePhone":    got.MobilePhone,
		"WebPage":        got.WebPage,
	}
	expect := map[string]string{
		"ServerID":       "c-99",
		"FirstName":      "Alice",
		"LastName":       "Engineer",
		"MiddleName":     "Q",
		"Title":          "Dr.",
		"Suffix":         "PhD",
		"FileAs":         "Engineer, Alice",
		"CompanyName":    "Acme",
		"Department":     "R&D",
		"JobTitle":       "Staff SWE",
		"OfficeLocation": "HQ-3",
		"Email1Address":  "alice@x",
		"Email2Address":  "a.engineer@personal",
		"Email3Address":  "a@old",
		"HomePhone":      "+1-555-0100",
		"BusinessPhone":  "+1-555-0200",
		"MobilePhone":    "+1-555-0300",
		"WebPage":        "https://alice.example",
	}
	for k, v := range expect {
		if wantNames[k] != v {
			t.Errorf("%s = %q, want %q", k, wantNames[k], v)
		}
	}
	if got.HomeAddress.Street != "1 Home St" || got.HomeAddress.Country != "Canada" {
		t.Errorf("HomeAddress = %+v", got.HomeAddress)
	}
	if got.BusinessAddress.City != "Worktown" || got.BusinessAddress.PostalCode != "90210" {
		t.Errorf("BusinessAddress = %+v", got.BusinessAddress)
	}
	if got.Birthday.IsZero() || got.Anniversary.IsZero() {
		t.Errorf("dates = %+v", got)
	}
	if !bytes.Equal(got.Picture, pic) {
		t.Errorf("Picture decoded = %v want %v", got.Picture, pic)
	}
	if len(got.Categories) != 2 || got.Categories[0] != "personal" || got.Categories[1] != "vip" {
		t.Errorf("Categories = %v", got.Categories)
	}
}

func TestParseContactItem_nilSafe(t *testing.T) {
	got := parseContactItem("only-id", nil)
	if got.ServerID != "only-id" {
		t.Errorf("ServerID = %q", got.ServerID)
	}
}

// TestParseContactItem_Contacts2Page covers the second codepage (NickName,
// IMAddress*, ManagerName, GovernmentID, CustomerID) which the
// allFields test only stocked from the primary Contacts page.
func TestParseContactItem_Contacts2Page(t *testing.T) {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData",
		wbxml.E(wbxml.PageContacts2, "NickName", wbxml.Text("Ali")),
		wbxml.E(wbxml.PageContacts2, "IMAddress", wbxml.Text("aim:ali")),
		wbxml.E(wbxml.PageContacts2, "IMAddress2", wbxml.Text("xmpp:ali@x")),
		wbxml.E(wbxml.PageContacts2, "IMAddress3", wbxml.Text("matrix:@ali")),
		wbxml.E(wbxml.PageContacts2, "ManagerName", wbxml.Text("Bob")),
		wbxml.E(wbxml.PageContacts2, "GovernmentId", wbxml.Text("SIN-1")),
		wbxml.E(wbxml.PageContacts2, "CustomerId", wbxml.Text("CUST-1")),
	)
	got := parseContactItem("c-3", app)
	if got.NickName != "Ali" || got.IMAddress != "aim:ali" || got.IMAddress2 != "xmpp:ali@x" ||
		got.IMAddress3 != "matrix:@ali" || got.ManagerName != "Bob" ||
		got.GovernmentID != "SIN-1" || got.CustomerID != "CUST-1" {
		t.Errorf("got %+v", got)
	}
}

func TestBuildContactApp_emitsAllFields(t *testing.T) {
	d := ContactDraft{
		FirstName:       "Alice",
		LastName:        "Engineer",
		MiddleName:      "Q",
		Title:           "Dr.",
		Suffix:          "PhD",
		FileAs:          "Engineer, Alice",
		CompanyName:     "Acme",
		Department:      "R&D",
		JobTitle:        "Staff",
		OfficeLocation:  "HQ",
		Email1Address:   "a@x",
		Email2Address:   "a@y",
		Email3Address:   "a@z",
		HomePhone:       "1",
		BusinessPhone:   "2",
		MobilePhone:     "3",
		WebPage:         "https://a",
		HomeAddress:     ContactAddress{Street: "1 Home", City: "HC", State: "HS", PostalCode: "H0H", Country: "Canada"},
		BusinessAddress: ContactAddress{Street: "9 Biz", City: "BC", State: "BS", PostalCode: "B0B", Country: "USA"},
		Birthday:        time.Date(1990, 4, 15, 0, 0, 0, 0, time.UTC),
		Anniversary:     time.Date(2018, 6, 12, 0, 0, 0, 0, time.UTC),
		Picture:         []byte{0x01, 0x02, 0x03},
		Categories:      []string{"vip", "work"},
		NickName:        "Ali",
		IMAddress:       "aim:ali",
		ManagerName:     "Bob",
		GovernmentID:    "SIN-1",
	}
	app := buildContactApp(d)
	wantText := map[string]string{
		"FirstName":             "Alice",
		"LastName":              "Engineer",
		"MiddleName":            "Q",
		"Title":                 "Dr.",
		"Suffix":                "PhD",
		"FileAs":                "Engineer, Alice",
		"CompanyName":           "Acme",
		"Department":            "R&D",
		"JobTitle":              "Staff",
		"OfficeLocation":        "HQ",
		"Email1Address":         "a@x",
		"Email2Address":         "a@y",
		"Email3Address":         "a@z",
		"HomePhoneNumber":       "1",
		"BusinessPhoneNumber":   "2",
		"MobilePhoneNumber":     "3",
		"WebPage":               "https://a",
		"HomeAddressStreet":     "1 Home",
		"HomeAddressCity":       "HC",
		"HomeAddressState":      "HS",
		"HomeAddressPostalCode": "H0H",
		"HomeAddressCountry":    "Canada",
		"BusinessAddressStreet": "9 Biz",
		"NickName":              "Ali",
		"IMAddress":             "aim:ali",
		"ManagerName":           "Bob",
		"GovernmentId":          "SIN-1",
	}
	for name, want := range wantText {
		if el := app.Find(name); el == nil || el.TextContent() != want {
			t.Errorf("buildContactApp <%s> = %v, want %q", name, el, want)
		}
	}
	// Birthday + Anniversary go through formatEASTime.
	if app.Find("Birthday") == nil || app.Find("Anniversary") == nil {
		t.Error("date fields missing")
	}
	if pic := app.Find("Picture"); pic == nil || pic.TextContent() == "" {
		t.Error("Picture missing or not base64-encoded")
	}
	cats := app.Find("Categories")
	if cats == nil || len(cats.Children) != 2 {
		t.Errorf("Categories = %v", cats)
	}
}

func TestParseContactItem_invalidPictureB64IsIgnored(t *testing.T) {
	app := wbxml.E(wbxml.PageAirSync, "ApplicationData",
		wbxml.E(wbxml.PageContacts, "Picture", wbxml.Text("!!!not-base64!!!")),
	)
	got := parseContactItem("c-1", app)
	if got.Picture != nil {
		t.Errorf("Picture = %v, want nil for invalid base64", got.Picture)
	}
}

// TestAddItemViaSync_invalidSyncKeyRetries: the per-item Sync helpers
// must reset and replay on Status=3, matching the recovery semantics of
// SyncCalendar / ApplyEmailChanges. Without this, a stale per-folder
// SyncKey (e.g. another device on the same account rotated state)
// makes every CreateContact / UpdateTask / DeleteEvent hard-fail.
func TestAddItemViaSync_invalidSyncKeyRetries(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		this := calls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch this {
		case 1: // first sendSyncCommands → Status=3
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(body)
		case 2: // re-bootstrap (ensureSynced)
			w.Write(pimSyncResponse("contacts", "RESET-1"))
		default: // replay succeeds with the new key
			w.Write(pimSyncResponse("contacts", "RESET-2"))
		}
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "STALE")
	if _, err := c.CreateContact(context.Background(), "contacts", ContactDraft{
		FirstName: "Alice",
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 3 {
		t.Errorf("calls = %d, want 3 (status3 → bootstrap → replay)", got)
	}
}

func TestSyncContacts_parsesChangeAndDelete(t *testing.T) {
	change := wbxml.E(wbxml.PageAirSync, "Change",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("c-2")),
		wbxml.E(wbxml.PageAirSync, "ApplicationData",
			wbxml.E(wbxml.PageContacts, "FirstName", wbxml.Text("Updated")),
		),
	)
	del := wbxml.E(wbxml.PageAirSync, "Delete",
		wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("c-3")),
	)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse("contacts", "C1", change, del))
	})
	res, err := c.SyncContacts(context.Background(), "contacts")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changed) != 1 || res.Changed[0].FirstName != "Updated" {
		t.Errorf("changed = %+v", res.Changed)
	}
	if len(res.Deleted) != 1 || res.Deleted[0] != "c-3" {
		t.Errorf("deleted = %v", res.Deleted)
	}
}

func TestSyncContacts_postErrorPropagates(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	if _, err := c.SyncContacts(context.Background(), "contacts"); err == nil {
		t.Error("want HTTP error")
	}
}

func TestGenericSyncFolder_emptyResponseUsesOldKey(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "PRIOR")
	res, err := c.SyncContacts(context.Background(), "contacts")
	if err != nil {
		t.Fatal(err)
	}
	if res.SyncKey != "PRIOR" {
		t.Errorf("expected old key carried through; got %q", res.SyncKey)
	}
}

func TestGenericSyncFolder_topLevelStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("8")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "K")
	if _, err := c.SyncContacts(context.Background(), "contacts"); !IsStatusCode(err, 8) {
		t.Errorf("err = %v", err)
	}
}

func TestGenericSyncFolder_missingCollection(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "K")
	if _, err := c.SyncContacts(context.Background(), "contacts"); err == nil {
		t.Error("want missing-Collection error")
	}
}

func TestGenericSyncFolder_collectionStatusError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("K")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("contacts")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("8")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "K")
	if _, err := c.SyncContacts(context.Background(), "contacts"); !IsStatusCode(err, 8) {
		t.Errorf("err = %v", err)
	}
}

func TestGenericSyncFolder_missingSyncKey(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("contacts")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	// Pre-set key to "" so newKey starts empty and stays empty.
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "")
	if _, err := c.SyncContacts(context.Background(), "contacts"); err == nil {
		t.Error("want missing-SyncKey error")
	}
}

func TestGenericSyncFolder_skipsNonElementCommands(t *testing.T) {
	// Stray text inside <Commands> must be skipped without affecting parse.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("K2")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("contacts")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageAirSync, "Commands",
						wbxml.Text("stray"),
						wbxml.E(wbxml.PageAirSync, "Add",
							wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("1")),
							wbxml.E(wbxml.PageAirSync, "ApplicationData",
								wbxml.E(wbxml.PageContacts, "FirstName", wbxml.Text("F")),
							),
						),
					),
				),
			),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	res, err := c.SyncContacts(context.Background(), "contacts")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0].FirstName != "F" {
		t.Errorf("added = %+v", res.Added)
	}
}

func TestGenericSyncFolder_persistKeyError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse("contacts", "NEWKEY"))
	})
	es := &errStateStore{inner: NewMemoryState(), setSyncKeyErr: errSentinel("disk full")}
	c.cfg.State = es
	if _, err := c.SyncContacts(context.Background(), "contacts"); err == nil {
		t.Error("want persist error")
	}
}

func TestGenericSyncFolder_syncKeyReadError(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("read fail")}
	if _, err := c.SyncContacts(context.Background(), "contacts"); err == nil {
		t.Error("want SyncKey read error")
	}
}

func TestAddItemViaSync_emptyResponseReturnsClientID(t *testing.T) {
	// Server replies with empty 200 OK; addItemViaSync returns the
	// client-generated id since there's no server id available.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // empty body
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "K1")
	id, err := c.CreateContact(context.Background(), "contacts", ContactDraft{
		FirstName: "Bob",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Error("expected client-generated id, got empty")
	}
}

func TestAddItemViaSync_responseWithoutCollection(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// Top-level Status only, no <Collection>.
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "K1")
	id, err := c.CreateContact(context.Background(), "contacts", ContactDraft{FirstName: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Error("expected client-generated id fallback")
	}
}

func TestAddItemViaSync_skipsNonAddInResponses(t *testing.T) {
	// Responses contains a Change entry alongside the Add — non-Add must
	// be skipped without confusing the ServerId lookup.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		req, _ := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
		clientID := ""
		if cid := req.Root.Find("ClientId"); cid != nil {
			clientID = cid.TextContent()
		}
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections",
				wbxml.E(wbxml.PageAirSync, "Collection",
					wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text("K2")),
					wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text("contacts")),
					wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
					wbxml.E(wbxml.PageAirSync, "Responses",
						wbxml.E(wbxml.PageAirSync, "Change",
							wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("ignored")),
						),
						wbxml.E(wbxml.PageAirSync, "Add",
							wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(clientID)),
							wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text("real:1")),
							wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
						),
					),
				),
			),
		)}
		respBody, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(respBody)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "K1")
	id, err := c.CreateContact(context.Background(), "contacts", ContactDraft{FirstName: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "real:1" {
		t.Errorf("id = %q, want real:1", id)
	}
}

func TestSendSyncCommandsWithReset_ensureSyncedFails(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be reached when state read fails")
	})
	c.cfg.State = &errStateStore{inner: NewMemoryState(), syncKeyErr: errSentinel("boom")}
	if _, err := c.CreateContact(context.Background(), "contacts", ContactDraft{FirstName: "X"}); err == nil {
		t.Error("want error from ensureSynced")
	}
}

func TestSendSyncCommandsWithReset_resetKeyFails(t *testing.T) {
	// Status=3 from the change reply → SetSyncKey reset fails.
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// The single reply path: Status=3 InvalidSyncKey.
		doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
		)}
		body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(body)
	})
	es := &errStateStore{inner: NewMemoryState()}
	c.cfg.State = es
	_ = es.inner.SetSyncKey(context.Background(), "contacts", "STALE")
	es.setSyncKeyErr = errSentinel("ro state")
	_, err := c.CreateContact(context.Background(), "contacts", ContactDraft{FirstName: "X"})
	if err == nil || !strings.Contains(err.Error(), "reset sync key") {
		t.Errorf("err = %v", err)
	}
}

func TestSendSyncCommandsWithReset_reBootstrapFails(t *testing.T) {
	// Status=3 → reset OK → re-bootstrap (ensureSynced) fails on the
	// follow-up Sync HTTP call.
	calls := 0
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			doc := &wbxml.Document{Root: wbxml.E(wbxml.PageAirSync, "Sync",
				wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("3")),
			)}
			body, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
			w.Write(body)
			return
		}
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), "contacts", "STALE")
	if _, err := c.CreateContact(context.Background(), "contacts", ContactDraft{FirstName: "X"}); err == nil {
		t.Error("want re-bootstrap error")
	}
}

// The shared sync helpers (addItemViaSync, changeItemViaSync,
// deleteItemViaSync) live in contacts.go because that's the original
// home of the per-item Sync wrapper logic. Their argument-validation
// branches deserve a co-located test.
func TestSyncHelpers_rejectMissingArgs(t *testing.T) {
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be hit on validation failure")
	})
	cases := []struct {
		name string
		fn   func() error
	}{
		{"add empty folder", func() error {
			_, err := c.addItemViaSync(context.Background(), "", wbxml.E(wbxml.PageAirSync, "ApplicationData"))
			return err
		}},
		{"change empty folder", func() error {
			return c.changeItemViaSync(context.Background(), "", "id", wbxml.E(wbxml.PageAirSync, "ApplicationData"))
		}},
		{"change empty server id", func() error {
			return c.changeItemViaSync(context.Background(), "f", "", wbxml.E(wbxml.PageAirSync, "ApplicationData"))
		}},
		{"delete empty folder", func() error {
			return c.deleteItemViaSync(context.Background(), "", "id")
		}},
		{"delete empty server id", func() error {
			return c.deleteItemViaSync(context.Background(), "f", "")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Error("want error")
			}
		})
	}
}
