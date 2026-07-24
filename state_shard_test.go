package main

import (
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
