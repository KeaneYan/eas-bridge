// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"net/http"
	"testing"

	"github.com/hstern/go-activesync/wbxml"
)

// pimSyncResponse builds a Sync response for any class with the given
// adds (each Add already in WBXML form). Used by per-class _test.go
// files to fabricate server responses without per-class boilerplate.
func pimSyncResponse(folderID, syncKey string, adds ...*wbxml.Element) []byte {
	collection := wbxml.E(wbxml.PageAirSync, "Collection",
		wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(syncKey)),
		wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
		wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
	)
	if len(adds) > 0 {
		commands := wbxml.E(wbxml.PageAirSync, "Commands")
		for _, a := range adds {
			commands.Children = append(commands.Children, a)
		}
		collection.Children = append(collection.Children, commands)
	}
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageAirSync, "Sync",
			wbxml.E(wbxml.PageAirSync, "Collections", collection),
		),
	}
	out, err := wbxml.Marshal(doc, wbxml.DefaultRegistry())
	if err != nil {
		panic(err)
	}
	return out
}

// twoCallSyncServer wires a test handler that responds to the bootstrap
// Sync (call 1) with an empty response carrying syncKey, then on call 2
// echoes any ClientId in an <Add> response with the supplied serverID.
//
// The returned **[]byte holds the second call's request body so tests
// can assert on what the client emitted.
func twoCallSyncServer(t *testing.T, folderID, syncKey, serverID string) (*httpClient, **[]byte) {
	t.Helper()
	var calls int
	bodyHolder := new([]byte)
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		calls++
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		switch calls {
		case 1:
			w.Write(pimSyncResponse(folderID, syncKey))
		case 2:
			*bodyHolder = body
			req, _ := wbxml.Unmarshal(body, wbxml.DefaultRegistry())
			cid := ""
			if e := req.Root.Find("ClientId"); e != nil {
				cid = e.TextContent()
			}
			doc := &wbxml.Document{
				Root: wbxml.E(wbxml.PageAirSync, "Sync",
					wbxml.E(wbxml.PageAirSync, "Collections",
						wbxml.E(wbxml.PageAirSync, "Collection",
							wbxml.E(wbxml.PageAirSync, "SyncKey", wbxml.Text(syncKey+"+1")),
							wbxml.E(wbxml.PageAirSync, "CollectionId", wbxml.Text(folderID)),
							wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
							wbxml.E(wbxml.PageAirSync, "Responses",
								wbxml.E(wbxml.PageAirSync, "Add",
									wbxml.E(wbxml.PageAirSync, "ClientId", wbxml.Text(cid)),
									wbxml.E(wbxml.PageAirSync, "ServerId", wbxml.Text(serverID)),
									wbxml.E(wbxml.PageAirSync, "Status", wbxml.Text("1")),
								),
							),
						),
					),
				),
			}
			b, _ := wbxml.Marshal(doc, wbxml.DefaultRegistry())
			w.Write(b)
		}
	})
	holder := &bodyHolder
	return c, holder
}

// errSentinel is a one-line error literal usable from tests.
type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// errStateStore wraps another StateStore and returns a fixed error from
// a chosen method. Tests use it to exercise the "state lookup failed"
// branches without needing a stub implementation per call site.
type errStateStore struct {
	inner         StateStore
	policyKeyErr  error
	syncKeyErr    error
	setPolicyErr  error
	setSyncKeyErr error
}

func (e *errStateStore) PolicyKey(ctx context.Context) (string, error) {
	if e.policyKeyErr != nil {
		return "", e.policyKeyErr
	}
	return e.inner.PolicyKey(ctx)
}

func (e *errStateStore) SetPolicyKey(ctx context.Context, key string) error {
	if e.setPolicyErr != nil {
		return e.setPolicyErr
	}
	return e.inner.SetPolicyKey(ctx, key)
}

func (e *errStateStore) SyncKey(ctx context.Context, folderID string) (string, error) {
	if e.syncKeyErr != nil {
		return "", e.syncKeyErr
	}
	return e.inner.SyncKey(ctx, folderID)
}

func (e *errStateStore) SetSyncKey(ctx context.Context, folderID, key string) error {
	if e.setSyncKeyErr != nil {
		return e.setSyncKeyErr
	}
	return e.inner.SetSyncKey(ctx, folderID, key)
}

// singleCallSyncServer responds to any Sync POST with an empty success
// response and stashes the request body. The client's per-folder sync
// key is pre-populated so the helper bootstrap path is skipped — tests
// see exactly the change/delete the caller emits.
func singleCallSyncServer(t *testing.T, folderID string) (*httpClient, *[]byte) {
	t.Helper()
	var lastBody []byte
	c, _, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readCapped(r.Body, 1<<20)
		lastBody = body
		w.Header().Set("Content-Type", "application/vnd.ms-sync.wbxml")
		w.Write(pimSyncResponse(folderID, "DONE"))
	})
	_ = c.cfg.State.SetSyncKey(context.Background(), folderID, "PRIOR")
	return c, &lastBody
}
