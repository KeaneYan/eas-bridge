package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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

// diskState 是进程内唯一的 EAS 同步状态 + IMAP UID 映射，JSON 持久化。
type diskState struct {
	path string
	mu   sync.Mutex

	DeviceID     string                     `json:"device_id"`
	PolicyKeyVal string                     `json:"policy_key"`
	SyncKeys     map[string]string          `json:"sync_keys"`
	Folders      []eas.Folder               `json:"folders"`
	Items        map[string][]eas.EmailItem `json:"items"`       // folderID → 邮件缓存
	UIDs         map[string][]uidEntry      `json:"uids"`        // folderID → UID 映射（按 UID 升序）
	FolderMeta   map[string]folderMeta      `json:"folder_meta"` // folderID → UID 管理元数据
	Events       map[string]eas.EventItem   `json:"events"`      // 日历事件缓存 serverID→event
	Deleted      map[string]map[string]bool `json:"deleted,omitempty"` // folderID → serverID → 已标记 \Deleted
}

func loadState(path string) (*diskState, error) {
	s := &diskState{
		path:       path,
		SyncKeys:   map[string]string{},
		Items:      map[string][]eas.EmailItem{},
		UIDs:       map[string][]uidEntry{},
		FolderMeta: map[string]folderMeta{},
		Events:     map[string]eas.EventItem{},
		Deleted:    map[string]map[string]bool{},
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("state.json 损坏: %w", err)
	}
	s.path = path
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
	return s, nil
}

// save 原子写（tmp+rename），持锁状态全量快照。
func (s *diskState) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *diskState) saveLocked() error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicWriteFile(s.path, b, 0600)
}

// mutate 持锁执行 fn 并原子落盘。
func (s *diskState) mutate(fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
	return s.saveLocked()
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

// markRead 标记某封邮件为已读（内存 + 磁盘），并返回需要推 EAS 的 serverID。
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
	return s.saveLocked()
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
	return s.saveLocked()
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
	return s.saveLocked()
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
	return s.saveLocked()
}

// addMovedItems 把 MoveItems 移入的邮件追加到目标文件夹并分配 UID。
// EAS 不回显本设备引起的变更（fork AGENTS.md 与实测均确认），目标文件夹的
// 增量同步永远看不到这些邮件，必须由客户端自己落进本地 state。
func (s *diskState) addMovedItems(dstFolder string, items []eas.EmailItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Items[dstFolder] = append(s.Items[dstFolder], items...)
	s.assignUIDs(dstFolder, items)
	return s.saveLocked()
}
