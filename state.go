package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hstern/go-activesync/eas"
)

// uidEntry 将一个 EAS serverID 映射到一个稳定的 IMAP UID。
type uidEntry struct {
	ServerID string `json:"sid"`
	UID      uint32 `json:"uid"`
}

// folderMeta 每个文件夹的 UID 管理元数据。
type folderMeta struct {
	NextUID     uint32 `json:"next_uid"`
	UIDValidity uint32 `json:"uid_validity"`
}

// ---------- 持久化格式（分片） ----------
//
// 2026-07-25 重构：单文件 11.5MB 全量重写（每封已读/星标都 Marshal+fsync 整份）
// → 按作用域分片：
//   state.json          主文件：DeviceID/PolicyKey/SyncKeys/Folders/FolderMeta（小）
//   folders/<fid>.json  每文件夹：Items/UIDs/Deleted（markRead 等单封操作只写此片）
//   events.json         日历事件
// 旧版单文件格式（含 items/uids/events/deleted 字段）首次加载时自动迁移。

// stateMain 主文件结构。Legacy* 字段仅为读取旧格式存在，永不写回。
type stateMain struct {
	DeviceID     string                `json:"device_id"`
	PolicyKeyVal string                `json:"policy_key"`
	SyncKeys     map[string]string     `json:"sync_keys"`
	Folders      []eas.Folder          `json:"folders"`
	FolderMeta   map[string]folderMeta `json:"folder_meta"`

	LegacyItems   map[string][]eas.EmailItem `json:"items,omitempty"`
	LegacyUIDs    map[string][]uidEntry      `json:"uids,omitempty"`
	LegacyEvents  map[string]eas.EventItem   `json:"events,omitempty"`
	LegacyDeleted map[string]map[string]bool `json:"deleted,omitempty"`
}

// folderShard 单文件夹分片。FolderID 冗余存储便于加载时还原键。
type folderShard struct {
	FolderID string          `json:"folder_id"`
	Items    []eas.EmailItem `json:"items"`
	UIDs     []uidEntry      `json:"uids"`
	Deleted  map[string]bool `json:"deleted,omitempty"`
}

// eventsShard 日历事件分片。
type eventsShard struct {
	Events map[string]eas.EventItem `json:"events"`
}

// diskState 是进程内唯一的 EAS 同步状态 + IMAP UID 映射。
// 内存结构与分片前一致；仅落盘粒度变化。
type diskState struct {
	path string // 主文件 state.json
	dir  string // state 目录（folders/ 与 events.json 所在）
	mu   sync.Mutex

	DeviceID     string
	PolicyKeyVal string
	SyncKeys     map[string]string
	Folders      []eas.Folder
	Items        map[string][]eas.EmailItem // folderID → 邮件缓存
	UIDs         map[string][]uidEntry      // folderID → UID 映射（按 UID 升序）
	FolderMeta   map[string]folderMeta      // folderID → UID 管理元数据
	Events       map[string]eas.EventItem   // 日历事件缓存 serverID→event
	Deleted      map[string]map[string]bool // folderID → serverID → 已标记 \Deleted
}

func loadState(path string) (*diskState, error) {
	s := &diskState{
		path:       path,
		dir:        filepath.Dir(path),
		SyncKeys:   map[string]string{},
		Items:      map[string][]eas.EmailItem{},
		UIDs:       map[string][]uidEntry{},
		FolderMeta: map[string]folderMeta{},
		Events:     map[string]eas.EventItem{},
		Deleted:    map[string]map[string]bool{},
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "folders"), 0700); err != nil {
		return nil, err
	}

	// 1. 主文件
	var legacy bool
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// 全新启动
	case err != nil:
		return nil, err
	default:
		var m stateMain
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("state.json 损坏: %w", err)
		}
		s.DeviceID = m.DeviceID
		s.PolicyKeyVal = m.PolicyKeyVal
		s.SyncKeys = m.SyncKeys
		s.Folders = m.Folders
		s.FolderMeta = m.FolderMeta
		// 旧格式迁移：主文件里还嵌着 items/uids/events/deleted
		if len(m.LegacyItems) > 0 || len(m.LegacyUIDs) > 0 || len(m.LegacyEvents) > 0 || len(m.LegacyDeleted) > 0 {
			legacy = true
			for fid, items := range m.LegacyItems {
				s.Items[fid] = items
			}
			for fid, uids := range m.LegacyUIDs {
				s.UIDs[fid] = uids
			}
			for sid, ev := range m.LegacyEvents {
				s.Events[sid] = ev
			}
			for fid, del := range m.LegacyDeleted {
				s.Deleted[fid] = del
			}
		}
	}

	// 2. 文件夹分片（分片数据优先于 legacy——同名 folder 已被分片覆盖的不回退）
	shardDir := filepath.Join(s.dir, "folders")
	entries, err := os.ReadDir(shardDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(shardDir, ent.Name()))
		if err != nil {
			continue // 坏片跳过，全量同步会重建
		}
		var sh folderShard
		if json.Unmarshal(data, &sh) != nil || sh.FolderID == "" {
			continue
		}
		s.Items[sh.FolderID] = sh.Items
		s.UIDs[sh.FolderID] = sh.UIDs
		if sh.Deleted != nil {
			s.Deleted[sh.FolderID] = sh.Deleted
		}
	}

	// 3. 日历分片：快照 + events.jsonl 增量日志重放（幂等）
	if data, err := os.ReadFile(filepath.Join(s.dir, "events.json")); err == nil {
		var es eventsShard
		if json.Unmarshal(data, &es) == nil && es.Events != nil {
			s.Events = es.Events
		}
	}
	if err := s.replayEventLogLocked(); err != nil {
		// IO 级错误（盘坏/权限）不应让 daemon 起不来：备份坏日志后继续——
		// 快照仍在，未落快照的变更由下次同步补回（与 state.json 损坏同理，H2 哲学）
		backup := s.eventLogPath() + ".corrupt-" + time.Now().Format("20060102150405")
		if rerr := os.Rename(s.eventLogPath(), backup); rerr == nil {
			log.Printf("[state] events.jsonl 读取失败（%v），已备份到 %s，从快照继续", err, backup)
		} else {
			log.Printf("[state] events.jsonl 读取失败（%v）且备份失败（%v），从快照继续", err, rerr)
		}
	}
	// 启动压实：日志超阈值时写快照+截断（daemon 长运行，日志只增不减会无限膨胀）
	if fi, err := os.Stat(s.eventLogPath()); err == nil && fi.Size() >= eventLogCompactThreshold {
		if err := s.writeEventsSnapshotLocked(); err != nil {
			return nil, fmt.Errorf("压实 events.jsonl 失败: %w", err)
		}
	}

	s.normalize()

	// 4. 迁移落地：把旧格式内容写入分片，主文件重写为无 legacy 的小文件。
	// 任一分片写失败必须中止——否则主文件瘦身后 legacy 数据永久丢失（ZCode M2）。
	if legacy {
		for fid := range s.Items {
			if err := s.saveFolderLocked(fid); err != nil {
				return nil, fmt.Errorf("迁移分片 %s 失败: %w", fid, err)
			}
		}
		if err := s.writeEventsSnapshotLocked(); err != nil {
			return nil, fmt.Errorf("迁移 events.json 失败: %w", err)
		}
		if err := s.saveMainLocked(); err != nil {
			return nil, fmt.Errorf("迁移 state.json 分片失败: %w", err)
		}
	}
	return s, nil
}

func (s *diskState) normalize() {
	if s.SyncKeys == nil {
		s.SyncKeys = map[string]string{}
	}
	if s.Items == nil {
		s.Items = map[string][]eas.EmailItem{}
	}
	if s.UIDs == nil {
		s.UIDs = map[string][]uidEntry{}
	}
	if s.FolderMeta == nil {
		s.FolderMeta = map[string]folderMeta{}
	}
	if s.Events == nil {
		s.Events = map[string]eas.EventItem{}
	}
	if s.Deleted == nil {
		s.Deleted = map[string]map[string]bool{}
	}
}

// shardFileName 把 folderID 转为安全的文件名（EAS folderID 形如
// "1"/"9722593"/"Event:DEFAULT"，非安全字符替换为 '_'）。
func shardFileName(folderID string) string {
	var b strings.Builder
	for _, r := range folderID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + ".json"
}

// ---------- 落盘（调用方须已持 s.mu） ----------

// saveMainLocked 写主文件（DeviceID/PolicyKey/SyncKeys/Folders/FolderMeta）。
func (s *diskState) saveMainLocked() error {
	m := stateMain{
		DeviceID:     s.DeviceID,
		PolicyKeyVal: s.PolicyKeyVal,
		SyncKeys:     s.SyncKeys,
		Folders:      s.Folders,
		FolderMeta:   s.FolderMeta,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return atomicWriteFile(s.path, b, 0600)
}

// saveFolderLocked 写单文件夹分片（Items/UIDs/Deleted）。
func (s *diskState) saveFolderLocked(folderID string) error {
	sh := folderShard{
		FolderID: folderID,
		Items:    s.Items[folderID],
		UIDs:     s.UIDs[folderID],
		Deleted:  s.Deleted[folderID],
	}
	b, err := json.Marshal(sh)
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(s.dir, "folders", shardFileName(folderID)), b, 0600)
}

// writeEventsSnapshotLocked 全量写 events.json 快照；成功后截断 events.jsonl
// （快照已含日志全部内容，重放幂等所以"先快照后截断"的崩溃窗口安全：
// 崩溃在两者之间 = 旧日志被多放一遍，结果相同）。截断失败只记日志。
// 调用方须已持 s.mu（或 loadState 单线程阶段）。
func (s *diskState) writeEventsSnapshotLocked() error {
	b, err := json.Marshal(eventsShard{Events: s.Events})
	if err != nil {
		return err
	}
	if err := atomicWriteFile(filepath.Join(s.dir, "events.json"), b, 0600); err != nil {
		return err
	}
	if _, err := os.Stat(s.eventLogPath()); err == nil {
		if err := atomicWriteFile(s.eventLogPath(), nil, 0600); err != nil {
			log.Printf("[state] 截断 events.jsonl 失败（不影响正确性，下轮压实重试）: %v", err)
		}
	}
	return nil
}

// saveLocked 全量落盘（主+全部分片+事件快照）。仅用于启动迁移等批量场景；
// 常规路径用定向落盘（saveMainLocked/saveFolderLocked/appendEventLogLocked）。
func (s *diskState) saveLocked() error {
	for fid := range s.Items {
		if err := s.saveFolderLocked(fid); err != nil {
			return err
		}
	}
	if err := s.writeEventsSnapshotLocked(); err != nil {
		return err
	}
	return s.saveMainLocked()
}

// save 原子写（tmp+rename），持锁全量快照。
func (s *diskState) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// mutate 持锁执行 fn 并落盘主文件（Folders/PolicyKey 等主文件作用域的变更）。
func (s *diskState) mutate(fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
	return s.saveMainLocked()
}

// mutateFolder 持锁执行 fn 并落盘主文件 + 该文件夹分片
// （syncMailOnce 同时推进 Items/UIDs（分片）与 FolderMeta/SyncKeys（主文件））。
// 顺序必须是先主后分（ZCode 全量审查 HIGH-2）：主文件先落，若分片写失败，
// 后果只是 NextUID 跳号（无害空洞）；反过来分片先落而主文件失败，
// NextUID 回退而分片里已有高位 UID，重启后 assignUIDs 会重用 UID。
func (s *diskState) mutateFolder(folderID string, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
	if err := s.saveMainLocked(); err != nil {
		return err
	}
	return s.saveFolderLocked(folderID)
}

// ---------- EAS StateStore 接口实现 ----------

func (s *diskState) PolicyKey(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PolicyKeyVal, nil
}

func (s *diskState) SetPolicyKey(_ context.Context, key string) error {
	return s.mutate(func() { s.PolicyKeyVal = key })
}

func (s *diskState) SyncKey(_ context.Context, folderID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.SyncKeys[folderID], nil
}

func (s *diskState) SetSyncKey(_ context.Context, folderID, key string) error {
	s.mu.Lock()
	s.SyncKeys[folderID] = key
	s.mu.Unlock()
	return nil
}

// ---------- IMAP UID 管理 ----------

// assignUIDs 为一批 EAS 邮件分配（或查询）IMAP UID，返回按 UID 升序排列的列表。
// 新来邮件按 arrivalOrder 分配递增 UID；已存在邮件保留原 UID。
func (s *diskState) assignUIDs(folderID string, items []eas.EmailItem) []eas.EmailItem {
	meta := s.FolderMeta[folderID]
	if meta.UIDValidity == 0 {
		// 时间戳 UIDVALIDITY：state 重置后自动升位，客户端能识别需全量重同步
		meta.UIDValidity = uint32(time.Now().Unix())
	}
	if meta.NextUID == 0 {
		meta.NextUID = 1 // IMAP UID 从 1 开始
	}
	// 防御：NextUID 不得低于已有最大 UID+1（ZCode 全量审查 HIGH-2——
	// 主文件比分片旧时 NextUID 回退会重用 UID，UIDVALIDITY 不变下 UID 重复
	// 是最静默的损坏形态）
	for _, e := range s.UIDs[folderID] {
		if e.UID >= meta.NextUID {
			meta.NextUID = e.UID + 1
		}
	}
	s.FolderMeta[folderID] = meta

	// 构建 serverID→UID 反查表
	sidMap := map[string]uint32{}
	for _, e := range s.UIDs[folderID] {
		sidMap[e.ServerID] = e.UID
	}

	// 为新邮件分配 UID（按到达顺序）
	changed := false
	for _, it := range items {
		if _, ok := sidMap[it.ServerID]; !ok {
			uid := meta.NextUID
			meta.NextUID++
			sidMap[it.ServerID] = uid
			s.UIDs[folderID] = append(s.UIDs[folderID], uidEntry{it.ServerID, uid})
			changed = true
		}
	}
	if changed {
		s.FolderMeta[folderID] = meta
	}

	// 给 items 填 UID 并按 UID 排序（供 IMAP seqnum 使用）
	type withUID struct {
		item eas.EmailItem
		uid  uint32
	}
	var tagged []withUID
	for _, it := range items {
		tagged = append(tagged, withUID{it, sidMap[it.ServerID]})
	}
	sort.Slice(tagged, func(i, j int) bool { return tagged[i].uid < tagged[j].uid })
	result := make([]eas.EmailItem, len(tagged))
	for i, t := range tagged {
		result[i] = t.item
	}
	// H1 修复：始终按当前 items 重建 UID 映射——被删除邮件的映射随之清掉，不留悬空项
	s.UIDs[folderID] = make([]uidEntry, len(tagged))
	for i, t := range tagged {
		s.UIDs[folderID][i] = uidEntry{t.item.ServerID, t.uid}
	}
	return result
}

// uidForServerID 返回 IMAP UID（0 表示未找到）。
func (s *diskState) uidForServerID(folderID, serverID string) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.UIDs[folderID] {
		if e.ServerID == serverID {
			return e.UID
		}
	}
	return 0
}

// uidValidity 返回文件夹的 UIDVALIDITY。
func (s *diskState) uidValidity(folderID string) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.FolderMeta[folderID].UIDValidity
}

// findItemByUID 按 IMAP UID 在 items 里查找（O(n)，邮件量<5000 可接受）。
func (s *diskState) findItemByUID(folderID string, uid uint32) (eas.EmailItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 反查 serverID
	for _, e := range s.UIDs[folderID] {
		if e.UID == uid {
			for _, it := range s.Items[folderID] {
				if it.ServerID == e.ServerID {
					return it, true
				}
			}
			break
		}
	}
	return eas.EmailItem{}, false
}

// serverIDForUID 返回 serverID（空串表示未找到）。
func (s *diskState) serverIDForUID(folderID string, uid uint32) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.UIDs[folderID] {
		if e.UID == uid {
			return e.ServerID
		}
	}
	return ""
}

// markRead 标记某封邮件为已读（内存 + 该文件夹分片），并返回需要推 EAS 的 serverID。
func (s *diskState) markRead(folderID, serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.Items[folderID]
	for i := range items {
		if items[i].ServerID == serverID {
			items[i].Read = true
			s.Items[folderID] = items
			break
		}
	}
	return s.saveFolderLocked(folderID)
}

// markUnread 标记某封邮件为未读。
func (s *diskState) markUnread(folderID, serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.Items[folderID]
	for i := range items {
		if items[i].ServerID == serverID {
			items[i].Read = false
			s.Items[folderID] = items
			break
		}
	}
	return s.saveFolderLocked(folderID)
}

// setFlagged 标记/清除某封邮件的星标（\Flagged，EAS FlagStatus 2/0）。
func (s *diskState) setFlagged(folderID, serverID string, flagged bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.Items[folderID]
	for i := range items {
		if items[i].ServerID == serverID {
			if flagged {
				items[i].FlagStatus = 2
			} else {
				items[i].FlagStatus = 0
			}
			s.Items[folderID] = items
			break
		}
	}
	return s.saveFolderLocked(folderID)
}

// setDeleted 标记/清除某封邮件的 \Deleted 旗标（本地标记，EXPUNGE 时才真正删除）。
func (s *diskState) setDeleted(folderID, serverID string, deleted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.Deleted[folderID]
	if m == nil {
		m = map[string]bool{}
		s.Deleted[folderID] = m
	}
	if deleted {
		m[serverID] = true
	} else {
		delete(m, serverID)
	}
	return s.saveFolderLocked(folderID)
}

// isDeleted 查询邮件是否被标记 \Deleted。调用方需已持有锁或使用本方法的锁。
func (s *diskState) isDeleted(folderID, serverID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Deleted[folderID][serverID]
}

// removeItems 从文件夹移除邮件并清理 \Deleted 标记（EXPUNGE/COPY 移出后调用）。
func (s *diskState) removeItems(folderID string, serverIDs ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	drop := map[string]bool{}
	for _, id := range serverIDs {
		drop[id] = true
	}
	var kept []eas.EmailItem
	for _, it := range s.Items[folderID] {
		if !drop[it.ServerID] {
			kept = append(kept, it)
		}
	}
	s.Items[folderID] = kept
	if m := s.Deleted[folderID]; m != nil {
		for _, id := range serverIDs {
			delete(m, id)
		}
	}
	return s.saveFolderLocked(folderID)
}

// addMovedItems 把 MoveItems 移入的邮件追加到目标文件夹并分配 UID。
// EAS 不回显本设备引起的变更（fork AGENTS.md 与实测均确认），目标文件夹的
// 增量同步永远看不到这些邮件，必须由客户端自己落进本地 state。
// 注意：assignUIDs 末尾会按传入列表全量重建 UID 映射，必须传文件夹全量
// items（而非仅增量），否则该文件夹原有 UID 映射被清空（ZCode MEDIUM-3）。
func (s *diskState) addMovedItems(dstFolder string, items []eas.EmailItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Items[dstFolder] = append(s.Items[dstFolder], items...)
	s.Items[dstFolder] = s.assignUIDs(dstFolder, s.Items[dstFolder])
	// assignUIDs 推进了 FolderMeta（主文件），两处都要落；先主后分（同 HIGH-2）
	if err := s.saveMainLocked(); err != nil {
		return err
	}
	return s.saveFolderLocked(dstFolder)
}

// resetSyncKey 清除文件夹的 synckey 并立即落盘（下次同步从头引导）。
// 用于 Coremail 对失效 synckey 回 Status 5 的自愈路径。
func (s *diskState) resetSyncKey(folderID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.SyncKeys, folderID)
	return s.saveMainLocked()
}

// saveNow 立即落盘主文件（synckey 前进但邮件无变更时调用——key 不落盘的话，
// 重启后从旧 key 恢复会被 Coremail 判失效返回 Status 5）。
func (s *diskState) saveNow() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveMainLocked()
}

// ---------- events.jsonl 增量日志（full-review ROI-4）----------
//
// 背景：events.json ~10.5MB（765 事件含 HTML 描述），改造前每次日历写/每 10
// 页同步都全量 marshal+原子重写。改为 append-only 日志 + 周期压实：
// 写路径只追加变更行（KB 级），读路径仍全走内存 map，重启时快照+日志重放。

// eventLogCompactThreshold events.jsonl 超过该体积时压实（启动时 + 每日修剪）。
// 设为 var 以便测试调小。
var eventLogCompactThreshold = int64(16 << 20) // 16MB

// eventLogEntry 是 events.jsonl 的一行：一次事件 upsert 或 delete。
type eventLogEntry struct {
	Upsert *eas.EventItem `json:"upsert,omitempty"`
	Delete string         `json:"delete,omitempty"`
}

func (s *diskState) eventLogPath() string { return filepath.Join(s.dir, "events.jsonl") }

// appendEventLogLocked 把事件变更追加到 events.jsonl（每行一条 JSON，flush+fsync）。
// 崩溃安全：只有最后一行可能写一半，replayEventLogLocked 跳过坏尾行——该行的
// 变更未被确认，而主文件 synckey 在同批次靠后落盘，崩溃窗口内 key 也未推进，
// 下次同步会重新拿到这些事件（与改造前"全量写 events.json → 写主文件"的窗口一致）。
// 调用方须已持 s.mu。
func (s *diskState) appendEventLogLocked(upserts []eas.EventItem, deletes []string) error {
	f, err := os.OpenFile(s.eventLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	writeLine := func(ent eventLogEntry) error {
		line, err := json.Marshal(ent)
		if err != nil {
			return err
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		return w.WriteByte('\n')
	}
	for i := range upserts {
		if err := writeLine(eventLogEntry{Upsert: &upserts[i]}); err != nil {
			return err
		}
	}
	for _, id := range deletes {
		if err := writeLine(eventLogEntry{Delete: id}); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Sync()
}

// replayEventLogLocked 重放 events.jsonl 到内存 map（幂等：按 serverID 覆盖/删除）。
// JSON 不合法的行（崩溃截断的尾行）跳过。
func (s *diskState) replayEventLogLocked() error {
	data, err := os.ReadFile(s.eventLogPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ent eventLogEntry
		if json.Unmarshal(line, &ent) != nil {
			continue // 崩溃截断的尾行
		}
		if ent.Upsert != nil {
			s.Events[ent.Upsert.ServerID] = *ent.Upsert
		} else if ent.Delete != "" {
			delete(s.Events, ent.Delete)
		}
	}
	return nil
}

// compactEventLogIfNeeded 日志超阈值时压实（每日修剪调用；启动压实在 loadState）。
func (s *diskState) compactEventLogIfNeeded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fi, err := os.Stat(s.eventLogPath()); err != nil || fi.Size() < eventLogCompactThreshold {
		return
	}
	if err := s.writeEventsSnapshotLocked(); err != nil {
		log.Printf("[state] events.jsonl 压实失败: %v", err)
	}
}

// saveCalendarLocked 日历同步批次落盘：事件变更追加 events.jsonl + 主文件（synckey）。
// 调用方须已持 s.mu（供 syncCalendarOnce 在锁内调用）。
func (s *diskState) saveCalendarLocked(upserts []eas.EventItem, deletes []string) error {
	if err := s.appendEventLogLocked(upserts, deletes); err != nil {
		return err
	}
	return s.saveMainLocked()
}

// upsertEvent 写入/更新日历事件（CalDAV 写操作后本地落库）。
// EAS 不回显本设备变更，与 addMovedItems 同理必须自行落库。
// 主文件也要落：CreateEvent/UpdateEvent 上行会推进日历 synckey（内存），
// 不落盘则重启从旧 key 恢复被判失效（ZCode 全量审查 HIGH-1）。
func (s *diskState) upsertEvent(ev eas.EventItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Events[ev.ServerID] = ev
	if err := s.appendEventLogLocked([]eas.EventItem{ev}, nil); err != nil {
		return err
	}
	return s.saveMainLocked()
}

// deleteEvent 删除日历事件（CalDAV DELETE 后本地落库）。主文件同 upsertEvent。
func (s *diskState) deleteEvent(serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Events, serverID)
	if err := s.appendEventLogLocked(nil, []string{serverID}); err != nil {
		return err
	}
	return s.saveMainLocked()
}
