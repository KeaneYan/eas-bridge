package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hstern/go-activesync/eas"
)

const (
	asVersion = "14.0"
)

// syncEngine 封装 EAS 客户端与同步状态。
type syncEngine struct {
	cfg     *config
	st      *diskState
	c       eas.Client
	flights flightGroup
}

func newSyncEngine(cfg *config) (*syncEngine, error) {
	st, err := loadState(statePath())
	if err != nil {
		// H2 修复：state.json 损坏不锁死——备份原文件后从空状态重建（UIDVALIDITY 时间戳机制保证客户端识别重置）
		backup := statePath() + ".corrupt-" + time.Now().Format("20060102150405")
		if rerr := os.Rename(statePath(), backup); rerr == nil {
			log.Printf("[state] state.json 损坏（%v），已备份到 %s，从空状态重新开始", err, backup)
		} else {
			log.Printf("[state] state.json 损坏（%v）且备份失败（%v），从空状态重新开始", err, rerr)
		}
		st, err = loadState(statePath())
		if err != nil {
			return nil, err
		}
	}
	st.mu.Lock()
	needID := st.DeviceID == ""
	st.mu.Unlock()
	if needID {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, fmt.Errorf("生成 DeviceID: %w", err)
		}
		if err := st.mutate(func() { st.DeviceID = fmt.Sprintf("%x", b) }); err != nil {
			return nil, err
		}
	}
	st.mu.Lock()
	deviceID := st.DeviceID
	st.mu.Unlock()

	c, err := eas.NewClient(eas.Config{
		ServerURL:  cfg.Server,
		Username:   cfg.User,
		Password:   cfg.Password,
		DeviceID:   deviceID,
		DeviceType: "eas-bridge",
		ASVersion:  asVersion,
		State:      st,
	})
	if err != nil {
		return nil, err
	}
	if err := c.Provision(context.Background()); err != nil {
		return nil, fmt.Errorf("Provision: %w", err)
	}
	engine := &syncEngine{cfg: cfg, st: st, c: c}
	engine.scheduleCachePrune()
	return engine, nil
}

func (e *syncEngine) syncFolders(ctx context.Context) error {
	res, err := e.c.FolderSync(ctx)
	if err != nil {
		return err
	}
	return e.st.mutate(func() {
		byID := map[string]eas.Folder{}
		for _, f := range e.st.Folders {
			byID[f.ServerID] = f
		}
		for _, f := range res.Added {
			byID[f.ServerID] = f
		}
		for _, f := range res.Updated {
			byID[f.ServerID] = f
		}
		for _, id := range res.Deleted {
			delete(byID, id)
		}
		e.st.Folders = e.st.Folders[:0]
		for _, f := range byID {
			e.st.Folders = append(e.st.Folders, f)
		}
		sort.Slice(e.st.Folders, func(i, j int) bool {
			return e.st.Folders[i].ServerID < e.st.Folders[j].ServerID
		})
	})
}

func (e *syncEngine) syncMail(ctx context.Context, folderID string) error {
	_, err := e.flights.DoContext(ctx, "sync-mail:"+folderID, func() (any, error) {
		return nil, e.syncMailOnce(ctx, folderID)
	})
	return err
}

func (e *syncEngine) syncMailOnce(ctx context.Context, folderID string) error {
	res, err := e.c.SyncEmail(ctx, folderID, eas.EmailSyncOptions{
		WindowSize: 200,
		BodyType:   eas.BodyTypePlain,
	})
	if err != nil {
		return err
	}
	invalidated := make([]string, 0, len(res.Added)+len(res.Changed)+len(res.Deleted))
	err = e.st.mutate(func() {
		existing := map[string]eas.EmailItem{}
		order := make([]string, 0, len(e.st.Items[folderID])+len(res.Added))
		for _, it := range e.st.Items[folderID] {
			existing[it.ServerID] = it
			order = append(order, it.ServerID)
		}
		for _, it := range res.Added {
			if _, exists := existing[it.ServerID]; !exists {
				order = append(order, it.ServerID)
			}
			existing[it.ServerID] = it
			invalidated = append(invalidated, it.ServerID)
		}
		for _, it := range res.Changed {
			before := existing[it.ServerID]
			if _, exists := existing[it.ServerID]; !exists {
				order = append(order, it.ServerID)
			}
			after := mergeEmailChange(before, it)
			existing[it.ServerID] = after
			if !emailMIMEContentEqual(before, after) {
				invalidated = append(invalidated, it.ServerID)
			}
		}
		deletedIDs := map[string]bool{}
		for _, id := range res.Deleted {
			deletedIDs[id] = true
			invalidated = append(invalidated, id)
		}
		var merged []eas.EmailItem
		for _, id := range order {
			if !deletedIDs[id] {
				merged = append(merged, existing[id])
			}
		}
		merged = e.st.assignUIDs(folderID, merged)
		e.st.Items[folderID] = merged
	})
	if err != nil {
		return err
	}
	e.invalidateMessageCache(folderID, invalidated...)
	return nil
}

// mergeEmailChange 保留 EAS Change 未携带的邮件字段。
// 大多数服务器的 Change 是稀疏增量（常见只有 Read/Flag），直接替换会丢失主题、
// 发件人、正文摘要和附件元数据。
func mergeEmailChange(base, change eas.EmailItem) eas.EmailItem {
	if base.ServerID == "" {
		return change
	}
	merged := mergeEmailItem(base, change)
	if change.ReadPresent {
		merged.Read = change.Read
	}
	if change.FlagStatusPresent {
		merged.FlagStatus = change.FlagStatus
	}
	return merged
}

func emailMIMEContentEqual(a, b eas.EmailItem) bool {
	return a.ServerID == b.ServerID &&
		a.Subject == b.Subject &&
		a.From == b.From &&
		a.To == b.To &&
		a.Cc == b.Cc &&
		a.ReplyTo == b.ReplyTo &&
		a.DateReceived.Equal(b.DateReceived) &&
		a.BodyType == b.BodyType &&
		a.Body == b.Body &&
		a.BodyTruncated == b.BodyTruncated &&
		a.HasAttachments == b.HasAttachments &&
		reflect.DeepEqual(a.Attachments, b.Attachments)
}

func (e *syncEngine) findFolder(folderID string) (eas.Folder, bool) {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, f := range e.st.Folders {
		if f.ServerID == folderID {
			return f, true
		}
	}
	names := imapFolderNames(e.st.Folders)
	for _, f := range e.st.Folders {
		if strings.EqualFold(names[f.ServerID], folderID) {
			return f, true
		}
	}
	return eas.Folder{}, false
}

// syncCalendar 增量同步日历事件到 st.Events（synckey 由库自动持久化）。
// Coremail 忽略 FilterType，首次全量拉取后靠 synckey 增量。
func (e *syncEngine) syncCalendar(ctx context.Context) error {
	_, err := e.flights.DoContext(ctx, "sync-calendar", func() (any, error) {
		return nil, e.syncCalendarOnce(ctx)
	})
	return err
}

func (e *syncEngine) syncCalendarOnce(ctx context.Context) error {
	e.st.mu.Lock()
	var calFolderID string
	for _, f := range e.st.Folders {
		if f.Type == eas.FolderTypeCalendar {
			calFolderID = f.ServerID
			break
		}
	}
	e.st.mu.Unlock()
	if calFolderID == "" {
		return fmt.Errorf("服务器没有日历文件夹")
	}
	for page := 0; page < 100; page++ {
		res, err := e.c.SyncCalendar(ctx, calFolderID, eas.CalendarSyncOptions{WindowSize: 100})
		if err != nil {
			return fmt.Errorf("SyncCalendar: %w", err)
		}
		e.st.mu.Lock()
		for _, ev := range res.Added {
			e.st.Events[ev.ServerID] = ev
		}
		for _, ev := range res.Changed {
			e.st.Events[ev.ServerID] = ev
		}
		for _, id := range res.Deleted {
			delete(e.st.Events, id)
		}
		shouldSave := (page+1)%10 == 0 || !res.MoreAvailable || page == 99
		var saveErr error
		if shouldSave {
			saveErr = e.st.saveLocked()
		}
		e.st.mu.Unlock()
		if saveErr != nil {
			return saveErr
		}
		if !res.MoreAvailable {
			return nil
		}
	}
	return fmt.Errorf("日历同步超过 100 页仍未完成")
}

func (e *syncEngine) poller(ctx context.Context, interval time.Duration, onChange func(folderID string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 只轮询收件箱（邮件文件夹），跳过日历/联系人等非邮件文件夹避免 SyncEmail 报错
	inboxID := ""
	e.st.mu.Lock()
	for _, f := range e.st.Folders {
		if f.Type == eas.FolderTypeInbox {
			inboxID = f.ServerID
			break
		}
	}
	e.st.mu.Unlock()
	if inboxID == "" {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.syncMail(ctx, inboxID); err != nil {
				// 临时网络错误静默，持续错误会记日志但不崩溃
				if !errors.Is(err, context.Canceled) {
					log.Printf("[poll] syncMail inbox: %v", err)
				}
				continue
			}
			onChange(inboxID)
		}
	}
}
