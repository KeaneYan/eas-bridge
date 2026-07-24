package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
)

// newMutationTestEngine 构造带两封邮件、垃圾箱、归档文件夹的可变异步测试引擎。
func newMutationTestEngine(t *testing.T, c *easmock.Client) *syncEngine {
	t.Helper()
	st, err := loadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Folders = []eas.Folder{
		{ServerID: "inbox", Type: eas.FolderTypeInbox, DisplayName: "Inbox"},
		{ServerID: "trash", Type: eas.FolderTypeDeletedItems, DisplayName: "Trash"},
		{ServerID: "archive", Type: eas.FolderTypeUserMail, DisplayName: "Archive"},
	}
	st.Items["inbox"] = []eas.EmailItem{
		{ServerID: "m1", Subject: "一"},
		{ServerID: "m2", Subject: "二"},
	}
	st.UIDs["inbox"] = []uidEntry{{ServerID: "m1", UID: 1}, {ServerID: "m2", UID: 2}}
	st.FolderMeta["inbox"] = folderMeta{NextUID: 3, UIDValidity: 7}
	return &syncEngine{st: st, c: c}
}

func newMutationSession(engine *syncEngine) *imapSession {
	d := newIMAPD(engine)
	sess := &imapSession{d: d, ctx: context.Background()}
	sess.selected = "inbox"
	st := engine.st
	st.mu.Lock()
	uidForSID := map[string]uint32{}
	sidForUID := map[uint32]string{}
	for _, e := range st.UIDs["inbox"] {
		uidForSID[e.ServerID] = e.UID
		sidForUID[e.UID] = e.ServerID
	}
	items := append([]eas.EmailItem(nil), st.Items["inbox"]...)
	st.mu.Unlock()
	sess.snap = &mboxSnapshot{
		items:       items,
		uidForSID:   uidForSID,
		sidForUID:   sidForUID,
		uidValidity: 7,
		deleted:     map[string]bool{},
	}
	return sess
}

func uidSetOf(uids ...imap.UID) imap.UIDSet {
	var s imap.UIDSet
	s.AddNum(uids...)
	return s
}

func TestTrashFolderID(t *testing.T) {
	engine := newMutationTestEngine(t, &easmock.Client{})
	id, err := engine.trashFolderID()
	if err != nil || id != "trash" {
		t.Fatalf("trashFolderID = %q, %v", id, err)
	}
}

func TestStoreDeletedMarksAndClears(t *testing.T) {
	engine := newMutationTestEngine(t, &easmock.Client{})
	st := engine.st
	if err := st.setDeleted("inbox", "m1", true); err != nil {
		t.Fatal(err)
	}
	if !st.isDeleted("inbox", "m1") {
		t.Fatal("m1 should be marked deleted")
	}
	if err := st.setDeleted("inbox", "m1", false); err != nil {
		t.Fatal(err)
	}
	if st.isDeleted("inbox", "m1") {
		t.Fatal("m1 deleted mark should be cleared")
	}
}

func TestApplyFlagMutationsSeenAndDeleted(t *testing.T) {
	engine := newMutationTestEngine(t, &easmock.Client{})
	sess := newMutationSession(engine)
	readChanges, mutated, err := sess.applyFlagMutations(uidSetOf(1, 2), &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{"\\Seen", "\\Deleted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(readChanges) != 2 || *readChanges[0].Read != true || *readChanges[1].Read != true {
		t.Fatalf("readChanges = %+v", readChanges)
	}
	if len(mutated) != 2 {
		t.Fatalf("mutated = %+v", mutated)
	}
	st := engine.st
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, it := range st.Items["inbox"] {
		if !it.Read {
			t.Fatalf("item %s should be read locally", it.ServerID)
		}
	}
	if !st.Deleted["inbox"]["m1"] || !st.Deleted["inbox"]["m2"] {
		t.Fatal("both items should be marked deleted")
	}
}

func TestPushReadChangesBatchesAndToleratesFailure(t *testing.T) {
	var got []eas.EmailChange
	calls := 0
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			ApplyEmailChangesFunc: func(_ context.Context, folderID string, changes []eas.EmailChange) ([]eas.EmailChangeResult, error) {
				calls++
				if folderID != "inbox" {
					t.Errorf("folderID = %q", folderID)
				}
				got = changes
				if calls == 1 {
					return nil, errors.New("server down")
				}
				out := make([]eas.EmailChangeResult, len(changes))
				for i, ch := range changes {
					out[i] = eas.EmailChangeResult{ServerID: ch.ServerID, Status: 1}
				}
				return out, nil
			},
		},
	}
	engine := newMutationTestEngine(t, mock)
	sess := newMutationSession(engine)
	changes := []eas.EmailChange{{ServerID: "m1", Read: boolPtr(true)}}

	// 失败：只记日志不 panic、不重试
	sess.pushReadChanges(context.Background(), changes)
	// 成功：批量一次调用
	sess.pushReadChanges(context.Background(), changes)
	if calls != 2 || len(got) != 1 || got[0].ServerID != "m1" {
		t.Fatalf("calls=%d got=%+v", calls, got)
	}
	// 空变更不调用服务器
	sess.pushReadChanges(context.Background(), nil)
	if calls != 2 {
		t.Fatalf("empty changes should not call server, calls=%d", calls)
	}
}

func TestExpungeMovesDeletedToTrash(t *testing.T) {
	var movedSrc, movedDst string
	var movedIDs []string
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				return &eas.EmailSyncResult{}, nil
			},
		},
		FolderClient: easmock.FolderClient{
			MoveItemsFunc: func(_ context.Context, src, dst string, ids []string) ([]eas.MoveItemResult, error) {
				movedSrc, movedDst, movedIDs = src, dst, ids
				out := make([]eas.MoveItemResult, len(ids))
				for i, id := range ids {
					out[i] = eas.MoveItemResult{SrcServerID: id, DstServerID: "trash:" + id, Status: 3}
				}
				return out, nil
			},
		},
	}
	engine := newMutationTestEngine(t, mock)
	if err := engine.st.setDeleted("inbox", "m2", true); err != nil {
		t.Fatal(err)
	}
	sess := newMutationSession(engine)
	sess.snap.deleted["m2"] = true

	seqs, err := sess.expungeMarked(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 1 || seqs[0] != 2 {
		t.Fatalf("seqs = %v", seqs)
	}
	if movedSrc != "inbox" || movedDst != "trash" || len(movedIDs) != 1 || movedIDs[0] != "m2" {
		t.Fatalf("MoveItems(%q→%q, %v)", movedSrc, movedDst, movedIDs)
	}
	engine.st.mu.Lock()
	defer engine.st.mu.Unlock()
	if len(engine.st.Items["inbox"]) != 1 || engine.st.Items["inbox"][0].ServerID != "m1" {
		t.Fatalf("inbox items = %+v", engine.st.Items["inbox"])
	}
	if engine.st.Deleted["inbox"]["m2"] {
		t.Fatal("deleted mark should be cleared after expunge")
	}
	// EAS 不回显本设备变更：垃圾箱必须本地落库移入的邮件（新 serverID）
	trash := engine.st.Items["trash"]
	if len(trash) != 1 || trash[0].ServerID != "trash:m2" || trash[0].Subject != "二" {
		t.Fatalf("trash items = %+v", trash)
	}
	if len(engine.st.UIDs["trash"]) != 1 {
		t.Fatal("trash UID should be assigned")
	}
}

func TestExpungeSkipsUnflagged(t *testing.T) {
	called := false
	mock := &easmock.Client{
		FolderClient: easmock.FolderClient{
			MoveItemsFunc: func(context.Context, string, string, []string) ([]eas.MoveItemResult, error) {
				called = true
				return nil, nil
			},
		},
	}
	engine := newMutationTestEngine(t, mock)
	sess := newMutationSession(engine)
	seqs, err := sess.expungeMarked(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 0 || called {
		t.Fatalf("seqs=%v called=%v", seqs, called)
	}
}

func TestExpungePartialFailureKeepsUnmoved(t *testing.T) {
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				return &eas.EmailSyncResult{}, nil
			},
		},
		FolderClient: easmock.FolderClient{
			MoveItemsFunc: func(_ context.Context, _, _ string, ids []string) ([]eas.MoveItemResult, error) {
				// 只第一封成功，第二封服务器拒绝
				return []eas.MoveItemResult{
					{SrcServerID: ids[0], Status: 3},
					{SrcServerID: ids[1], Status: 5},
				}, nil
			},
		},
	}
	engine := newMutationTestEngine(t, mock)
	engine.st.setDeleted("inbox", "m1", true)
	engine.st.setDeleted("inbox", "m2", true)
	sess := newMutationSession(engine)
	sess.snap.deleted["m1"] = true
	sess.snap.deleted["m2"] = true

	seqs, err := sess.expungeMarked(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 1 || seqs[0] != 1 {
		t.Fatalf("seqs = %v", seqs)
	}
	engine.st.mu.Lock()
	defer engine.st.mu.Unlock()
	if len(engine.st.Items["inbox"]) != 1 || engine.st.Items["inbox"][0].ServerID != "m2" {
		t.Fatalf("failed item must stay: %+v", engine.st.Items["inbox"])
	}
}

func TestCopyMovesToDestFolder(t *testing.T) {
	var movedDst string
	mock := &easmock.Client{
		EmailClient: easmock.EmailClient{
			SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
				return &eas.EmailSyncResult{}, nil
			},
		},
		FolderClient: easmock.FolderClient{
			MoveItemsFunc: func(_ context.Context, src, dst string, ids []string) ([]eas.MoveItemResult, error) {
				movedDst = dst
				out := make([]eas.MoveItemResult, len(ids))
				for i, id := range ids {
					out[i] = eas.MoveItemResult{SrcServerID: id, DstServerID: "archive:" + id, Status: 3}
				}
				return out, nil
			},
		},
	}
	engine := newMutationTestEngine(t, mock)
	// 目标文件夹预置一封邮件，验证 addMovedItems 不会清空其 UID 映射（MEDIUM-3 回归）
	engine.st.Items["archive"] = []eas.EmailItem{{ServerID: "pre-existing", Subject: "旧"}}
	engine.st.UIDs["archive"] = []uidEntry{{ServerID: "pre-existing", UID: 41}}
	engine.st.FolderMeta["archive"] = folderMeta{NextUID: 42, UIDValidity: 9}
	sess := newMutationSession(engine)
	if _, err := sess.Copy(uidSetOf(1), "Archive"); err != nil {
		t.Fatal(err)
	}
	if movedDst != "archive" {
		t.Fatalf("MoveItems dst = %q", movedDst)
	}
	engine.st.mu.Lock()
	defer engine.st.mu.Unlock()
	if len(engine.st.Items["inbox"]) != 1 || engine.st.Items["inbox"][0].ServerID != "m2" {
		t.Fatalf("inbox items = %+v", engine.st.Items["inbox"])
	}
	// EAS 不回显本设备变更：目标文件夹必须本地落库移入的邮件
	arch := engine.st.Items["archive"]
	if len(arch) != 2 || arch[1].ServerID != "archive:m1" || arch[1].Subject != "一" {
		t.Fatalf("archive items = %+v", arch)
	}
	// MEDIUM-3 回归：目标文件夹原有邮件的 UID 映射不得被清空
	if arch[0].ServerID != "pre-existing" {
		t.Fatalf("pre-existing item lost: %+v", arch)
	}
	var preUID uint32
	for _, e := range engine.st.UIDs["archive"] {
		if e.ServerID == "pre-existing" {
			preUID = e.UID
		}
	}
	if preUID != 41 {
		t.Fatalf("pre-existing UID = %d, want 41 (映射被重建清空)", preUID)
	}
}

func TestCopyRejectsUnknownDest(t *testing.T) {
	engine := newMutationTestEngine(t, &easmock.Client{})
	sess := newMutationSession(engine)
	if _, err := sess.Copy(uidSetOf(1), "NoSuchFolder"); err == nil {
		t.Fatal("expected error for unknown destination")
	}
}

// TestStoreFlaggedLocalAndPush：STORE +FLAGS \Flagged 必须本地落 FlagStatus=2
// 且上行 EmailChange.SetFlagStatus=2；去除星标对称。
func TestStoreFlaggedLocalAndPush(t *testing.T) {
	engine := newMutationTestEngine(t, &easmock.Client{})
	sess := newMutationSession(engine)

	readChanges, mutated, err := sess.applyFlagMutations(uidSetOf(1), &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{"\\Flagged"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(readChanges) != 1 || readChanges[0].SetFlagStatus == nil || *readChanges[0].SetFlagStatus != 2 {
		t.Fatalf("SetFlagStatus 上行缺失: %+v", readChanges)
	}
	// 本地 state 已落
	st := engine.st
	st.mu.Lock()
	got := st.Items["inbox"][0].FlagStatus
	st.mu.Unlock()
	if got != 2 {
		t.Fatalf("本地 FlagStatus = %d, want 2", got)
	}
	// 响应 flags 含 \Flagged
	found := false
	for _, f := range mutated[0].flags {
		if f == "\\Flagged" {
			found = true
		}
	}
	if !found {
		t.Fatalf("响应 flags 缺 \\Flagged: %+v", mutated[0].flags)
	}

	// 去星
	readChanges, mutated, err = sess.applyFlagMutations(uidSetOf(1), &imap.StoreFlags{
		Op:    imap.StoreFlagsDel,
		Flags: []imap.Flag{"\\Flagged"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(readChanges) != 1 || *readChanges[0].SetFlagStatus != 0 {
		t.Fatalf("去星上行缺失: %+v", readChanges)
	}
	st.mu.Lock()
	got = st.Items["inbox"][0].FlagStatus
	st.mu.Unlock()
	if got != 0 {
		t.Fatalf("去星后 FlagStatus = %d, want 0", got)
	}
	// itemFlags 反映星标
	st.Items["inbox"][0] = eas.EmailItem{ServerID: "m1", FlagStatus: 2}
	flags := itemFlags(st.Items["inbox"][0], false)
	hasFlagged := false
	for _, f := range flags {
		if f == "\\Flagged" {
			hasFlagged = true
		}
	}
	if !hasFlagged {
		t.Fatalf("itemFlags 缺 \\Flagged: %+v", flags)
	}
}
