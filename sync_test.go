package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
)

func TestSyncMailPreservesServerArrivalOrder(t *testing.T) {
	st, err := loadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Items["inbox"] = []eas.EmailItem{{ServerID: "existing"}}
	st.UIDs["inbox"] = []uidEntry{{ServerID: "existing", UID: 1}}
	st.FolderMeta["inbox"] = folderMeta{NextUID: 2, UIDValidity: 123}

	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
					return &eas.EmailSyncResult{
						Added: []eas.EmailItem{
							{ServerID: "first-new"},
							{ServerID: "second-new"},
						},
					}, nil
				},
			},
		},
	}
	if err := engine.syncMail(context.Background(), "inbox"); err != nil {
		t.Fatal(err)
	}

	gotItems := st.Items["inbox"]
	if len(gotItems) != 3 ||
		gotItems[0].ServerID != "existing" ||
		gotItems[1].ServerID != "first-new" ||
		gotItems[2].ServerID != "second-new" {
		t.Fatalf("mail order = %+v", gotItems)
	}
	gotUIDs := st.UIDs["inbox"]
	if gotUIDs[1].UID != 2 || gotUIDs[1].ServerID != "first-new" ||
		gotUIDs[2].UID != 3 || gotUIDs[2].ServerID != "second-new" {
		t.Fatalf("UID order = %+v", gotUIDs)
	}
}

func TestSyncKeyIsCommittedWithStateSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSyncKey(context.Background(), "inbox", "next"); err != nil {
		t.Fatal(err)
	}
	beforeCommit, err := loadState(path)
	if err != nil {
		t.Fatalf("loading absent pre-commit state: %v", err)
	}
	if beforeCommit.SyncKeys["inbox"] != "" {
		t.Fatalf("SyncKey was persisted before state snapshot: %q", beforeCommit.SyncKeys["inbox"])
	}
	if err := st.save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SyncKeys["inbox"] != "next" {
		t.Fatalf("persisted SyncKey = %q", reloaded.SyncKeys["inbox"])
	}
}
