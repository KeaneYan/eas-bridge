package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hstern/go-activesync/eas"
)

// writeLegacyState 写一份旧版单文件格式 state.json。
func writeLegacyState(t *testing.T, path string) {
	t.Helper()
	legacy := map[string]any{
		"device_id": "dev1",
		"sync_keys": map[string]string{"1": "k1", "2": "k2"},
		"folders":   []eas.Folder{{ServerID: "1", DisplayName: "Inbox", Type: 2}},
		"folder_meta": map[string]folderMeta{
			"1": {NextUID: 3, UIDValidity: 111},
			"2": {NextUID: 2, UIDValidity: 222},
		},
		"items": map[string][]eas.EmailItem{
			"1": {{ServerID: "m1", Subject: "一"}, {ServerID: "m2", Subject: "二", Read: true}},
			"2": {{ServerID: "n1", Subject: "三"}},
		},
		"uids": map[string][]uidEntry{
			"1": {{"m1", 1}, {"m2", 2}},
			"2": {{"n1", 1}},
		},
		"events":  map[string]eas.EventItem{"ev1": {ServerID: "ev1", Subject: "会"}},
		"deleted": map[string]map[string]bool{"1": {"m2": true}},
	}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	writeLegacyState(t, path)

	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	// 内存数据完整
	if st.DeviceID != "dev1" || st.SyncKeys["1"] != "k1" {
		t.Fatalf("main 数据丢失: %+v", st)
	}
	if len(st.Items["1"]) != 2 || len(st.Items["2"]) != 1 || !st.Items["1"][1].Read {
		t.Fatalf("items 迁移失败: %+v", st.Items)
	}
	if !st.Deleted["1"]["m2"] || st.Events["ev1"].Subject != "会" {
		t.Fatalf("deleted/events 迁移失败")
	}
	// 分片文件已生成
	for _, fid := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(dir, "folders", shardFileName(fid))); err != nil {
			t.Fatalf("分片未生成 %s: %v", fid, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "events.json")); err != nil {
		t.Fatal("events.json 未生成")
	}
	// 主文件不再含 items（已瘦身）
	b, _ := os.ReadFile(path)
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["items"]; ok {
		t.Fatal("迁移后主文件仍含 items 字段")
	}
	// 二次加载（分片路径）数据一致
	st2, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Items["1"]) != 2 || st2.Events["ev1"].Subject != "会" || !st2.Deleted["1"]["m2"] {
		t.Fatal("二次加载数据不一致")
	}
}

func TestShardGranularity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	writeLegacyState(t, path)
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}

	// 记录文件夹 2 分片与主文件 mtime
	stat := func(p string) int64 {
		fi, err := os.Stat(p)
		if err != nil {
			return -1
		}
		return fi.ModTime().UnixNano()
	}
	shard2 := filepath.Join(dir, "folders", shardFileName("2"))
	mainBefore := stat(path)
	shard2Before := stat(shard2)

	// 文件夹 1 的单封操作不应触碰文件夹 2 分片和主文件
	if err := st.markRead("1", "m1"); err != nil {
		t.Fatal(err)
	}
	if stat(shard2) != shard2Before {
		t.Fatal("markRead(文件夹1) 不应重写文件夹2分片")
	}
	if stat(path) != mainBefore {
		t.Fatal("markRead 不应重写主文件")
	}
	// 文件夹 1 分片确实更新了
	shard1 := filepath.Join(dir, "folders", shardFileName("1"))
	if stat(shard1) <= stat(shard2) && shard2Before > 0 {
		// shard1 mtime 应 >= 本次操作时间（宽松断言：内容校验为准）
	}
	// 内容校验：重新加载确认已读落盘
	st2, _ := loadState(path)
	for _, it := range st2.Items["1"] {
		if it.ServerID == "m1" && !it.Read {
			t.Fatal("m1 已读未落盘")
		}
	}

	// synckey 变更（saveNow）只写主文件
	shard1Before := stat(shard1)
	if err := st.mutate(func() { st.SyncKeys["1"] = "k9" }); err != nil {
		t.Fatal(err)
	}
	if stat(shard1) != shard1Before {
		t.Fatal("主文件变更不应重写文件夹分片")
	}
	st3, _ := loadState(path)
	if st3.SyncKeys["1"] != "k9" {
		t.Fatal("synckey 未落盘")
	}
}

func TestMutateFolderSavesBoth(t *testing.T) {
	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = st.mutateFolder("1", func() {
		st.Items["1"] = []eas.EmailItem{{ServerID: "x1"}}
		st.SyncKeys["1"] = "abc"
	})
	if err != nil {
		t.Fatal(err)
	}
	st2, _ := loadState(filepath.Join(dir, "state.json"))
	if len(st2.Items["1"]) != 1 || st2.SyncKeys["1"] != "abc" {
		t.Fatalf("mutateFolder 未同时落分片与主文件: %+v / %+v", st2.Items, st2.SyncKeys)
	}
}

func TestEventShardPersistence(t *testing.T) {
	dir := t.TempDir()
	st, _ := loadState(filepath.Join(dir, "state.json"))
	if err := st.upsertEvent(eas.EventItem{ServerID: "e1", Subject: "例会"}); err != nil {
		t.Fatal(err)
	}
	st2, _ := loadState(filepath.Join(dir, "state.json"))
	if st2.Events["e1"].Subject != "例会" {
		t.Fatal("事件未落盘")
	}
	if err := st2.deleteEvent("e1"); err != nil {
		t.Fatal(err)
	}
	st3, _ := loadState(filepath.Join(dir, "state.json"))
	if _, ok := st3.Events["e1"]; ok {
		t.Fatal("事件删除未落盘")
	}
}

func TestCalendarAliasPersistsThroughLogAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical := eas.EventItem{ServerID: "ev1", UID: "uid1", Subject: "会议"}
	if err := st.upsertEvent(canonical); err != nil {
		t.Fatal(err)
	}

	st.mu.Lock()
	alias := calendarEventAlias{CanonicalID: canonical.ServerID, UID: canonical.UID}
	st.EventAliases["ev2"] = alias
	var changes calendarMutations
	changes.recordAlias("ev2", alias)
	err = st.saveCalendarLocked(changes)
	st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	fromLog, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fromLog.EventAliases["ev2"]; got != alias {
		t.Fatalf("replayed alias = %+v, want %+v", got, alias)
	}

	fromLog.mu.Lock()
	err = fromLog.writeEventsSnapshotLocked()
	fromLog.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	fromSnapshot, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fromSnapshot.EventAliases["ev2"]; got != alias {
		t.Fatalf("snapshotted alias = %+v, want %+v", got, alias)
	}
}

func TestCalendarAliasPromotionPersistsAsAtomicBatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical := eas.EventItem{ServerID: "ev-a", UID: "uid-a", Subject: "会议"}
	st.Events[canonical.ServerID] = canonical
	st.EventAliases["ev-b"] = calendarEventAlias{CanonicalID: canonical.ServerID, UID: "uid-b"}
	st.EventAliases["ev-c"] = calendarEventAlias{CanonicalID: canonical.ServerID, UID: "uid-c"}
	st.mu.Lock()
	if err := st.writeEventsSnapshotLocked(); err != nil {
		st.mu.Unlock()
		t.Fatal(err)
	}
	st.mu.Unlock()

	st.mu.Lock()
	index := newCalendarEventDuplicateIndex(st.Events)
	var changes calendarMutations
	if !deleteCalendarEventLocked(st.Events, st.EventAliases, index, canonical.ServerID, &changes) {
		st.mu.Unlock()
		t.Fatal("canonical deletion did not promote an alias")
	}
	err = st.saveCalendarLocked(changes)
	st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(st.eventLogPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.FieldsFunc(data, func(r rune) bool { return r == '\n' })
	if len(lines) != 1 {
		t.Fatalf("promotion log lines = %d, want one atomic batch", len(lines))
	}

	reloaded, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Events["ev-a"]; ok {
		t.Fatal("deleted canonical ID reappeared")
	}
	if got := reloaded.Events["ev-b"].UID; got != "uid-b" {
		t.Fatalf("promoted UID = %q, want uid-b", got)
	}
	if got := reloaded.EventAliases["ev-c"].CanonicalID; got != "ev-b" {
		t.Fatalf("retargeted alias = %q, want ev-b", got)
	}
}

// TestAssignUIDsClampsNextUID：主文件比分片旧时 NextUID 回退，assignUIDs
// 必须校正到 maxUID+1，杜绝 UID 重用（ZCode 全量审查 HIGH-2 回归）。
func TestAssignUIDsClampsNextUID(t *testing.T) {
	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.UIDs["1"] = []uidEntry{{ServerID: "old", UID: 5}}
	st.Items["1"] = []eas.EmailItem{{ServerID: "old"}}
	st.FolderMeta["1"] = folderMeta{NextUID: 3, UIDValidity: 7} // 回退的 NextUID
	got := st.assignUIDs("1", append(st.Items["1"], eas.EmailItem{ServerID: "new"}))
	meta := st.FolderMeta["1"]
	st.mu.Unlock()
	if meta.NextUID != 7 {
		t.Fatalf("NextUID = %d, want 7（max 5 + 新分配 6 + 1）", meta.NextUID)
	}
	// 新邮件必须拿到 6，不是回退后的 3
	for _, it := range got {
		if it.ServerID == "new" {
			if st.uidForServerID("1", "new") != 6 {
				t.Fatalf("新邮件 UID = %d, want 6", st.uidForServerID("1", "new"))
			}
		}
	}
}

// events.jsonl：upsert/delete 追加 → 不写快照 → 重放还原
func TestEventLogAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(eas.EventItem{ServerID: "ev1", Subject: "会议1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(eas.EventItem{ServerID: "ev2", Subject: "会议2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.deleteEvent("ev1"); err != nil {
		t.Fatal(err)
	}
	// 追加路径不写 events.json 快照
	if _, err := os.Stat(filepath.Join(dir, "events.json")); !os.IsNotExist(err) {
		t.Fatalf("追加路径不应写 events.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatalf("events.jsonl 不存在: %v", err)
	}

	st2, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st2.Events["ev1"]; ok {
		t.Fatal("ev1 应已删除")
	}
	if ev, ok := st2.Events["ev2"]; !ok || ev.Subject != "会议2" {
		t.Fatalf("ev2 重放失败: %v %v", ok, ev.Subject)
	}
}

// 崩溃截断的尾行被跳过，不污染已有数据
func TestEventLogToleratesTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(eas.EventItem{ServerID: "ev1", Subject: "完好"}); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃写一半的尾行
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"upsert":{"ServerID":"ev-broken","Subject":"残缺`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	st2, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("坏尾行不应导致加载失败: %v", err)
	}
	if _, ok := st2.Events["ev-broken"]; ok {
		t.Fatal("截断行不应生效")
	}
	if st2.Events["ev1"].Subject != "完好" {
		t.Fatal("已有事件被污染")
	}
}

// 超阈值启动压实：快照重写 + 日志截断 + 数据不丢
func TestEventLogCompaction(t *testing.T) {
	old := eventLogCompactThreshold
	eventLogCompactThreshold = 1 // 1 字节，任何日志都触发压实
	defer func() { eventLogCompactThreshold = old }()

	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(eas.EventItem{ServerID: "ev1", Subject: "压实前"}); err != nil {
		t.Fatal(err)
	}

	st2, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st2.Events["ev1"].Subject != "压实前" {
		t.Fatal("压实后事件丢失")
	}
	fi, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("压实后 events.jsonl 应存在（空文件）: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("压实后日志应截断为 0，实际 %d", fi.Size())
	}
	if _, err := os.Stat(filepath.Join(dir, "events.json")); err != nil {
		t.Fatalf("压实后快照应存在: %v", err)
	}
}

// 快照+旧日志并存时重放幂等（压实中崩溃窗口：日志内容已在快照里）
func TestEventLogReplayIdempotentWithSnapshot(t *testing.T) {
	dir := t.TempDir()
	st, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(eas.EventItem{ServerID: "ev1", Subject: "v1"}); err != nil {
		t.Fatal(err)
	}
	// 手动写快照（不截断日志，模拟压实中途崩溃）
	st.mu.Lock()
	st.Events["ev1"] = eas.EventItem{ServerID: "ev1", Subject: "v1"}
	b, _ := json.Marshal(eventsShard{Events: st.Events})
	if err := atomicWriteFile(filepath.Join(dir, "events.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	st.mu.Unlock()

	st2, err := loadState(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Events) != 1 || st2.Events["ev1"].Subject != "v1" {
		t.Fatalf("快照+日志重放结果错误: %v", st2.Events)
	}
}
