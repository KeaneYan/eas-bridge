package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hstern/go-activesync/eas"
)

const (
	asVersion = "14.0"
)

// syncEngine 封装 EAS 客户端与同步状态。
type syncEngine struct {
	cfg *config
	st  *diskState
	c   eas.Client
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
	return &syncEngine{cfg: cfg, st: st, c: c}, nil
}

func (e *syncEngine) syncFolders(ctx context.Context) error {
	res, err := e.c.FolderSync(ctx)
	if err != nil {
		return err
	}
	if len(res.Added) == 0 && len(res.Updated) == 0 && len(res.Deleted) == 0 {
		return nil
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
	})
}

func (e *syncEngine) syncMail(ctx context.Context, folderID string) error {
	res, err := e.c.SyncEmail(ctx, folderID, eas.EmailSyncOptions{
		WindowSize: 200,
		BodyType:   eas.BodyTypePlain,
	})
	if err != nil {
		return err
	}
	if len(res.Added) == 0 && len(res.Changed) == 0 && len(res.Deleted) == 0 {
		return nil
	}
	return e.st.mutate(func() {
		existing := map[string]eas.EmailItem{}
		for _, it := range e.st.Items[folderID] {
			existing[it.ServerID] = it
		}
		for _, it := range res.Added {
			existing[it.ServerID] = it
		}
		for _, it := range res.Changed {
			existing[it.ServerID] = it
		}
		deletedIDs := map[string]bool{}
		for _, id := range res.Deleted {
			deletedIDs[id] = true
		}
		var merged []eas.EmailItem
		for _, it := range existing {
			if !deletedIDs[it.ServerID] {
				merged = append(merged, it)
			}
		}
		merged = e.st.assignUIDs(folderID, merged)
		e.st.Items[folderID] = merged
	})
}

func (e *syncEngine) findFolder(folderID string) (eas.Folder, bool) {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, f := range e.st.Folders {
		if f.ServerID == folderID {
			return f, true
		}
	}
	aliases := map[string]eas.FolderType{
		"INBOX": eas.FolderTypeInbox, "收件箱": eas.FolderTypeInbox,
		"Sent": eas.FolderTypeSentItems, "已发送": eas.FolderTypeSentItems,
		"Drafts": eas.FolderTypeDrafts, "草稿": eas.FolderTypeDrafts,
		"Trash": eas.FolderTypeDeletedItems, "已删除": eas.FolderTypeDeletedItems,
	}
	if t, ok := aliases[folderID]; ok {
		for _, f := range e.st.Folders {
			if f.Type == t {
				return f, true
			}
		}
	}
	return eas.Folder{}, false
}

// fetchMIME 从缓存或服务器拉取完整 RFC822 消息。
// Coremail EAS 对纯文本邮件不返回 BodyMIME（已知怪癖），此时用 EAS 元数据构造。
func (e *syncEngine) fetchMIME(ctx context.Context, folderID, serverID string) ([]byte, error) {
	dir, err := ensureMIMECacheDir(folderID)
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(dir, serverID+".eml")
	if b, err := os.ReadFile(cachePath); err == nil && len(b) > 10 {
		return b, nil
	}

	// 尝试从服务器拉 MIME
	f, ok := e.findFolder(folderID)
	if ok {
		if it, ferr := e.c.FetchEmail(ctx, f.ServerID, serverID, eas.FetchEmailOptions{BodyType: eas.BodyTypeMIME}); ferr == nil && len(it.BodyMIME) > 10 {
			_ = os.WriteFile(cachePath, it.BodyMIME, 0600)
			return it.BodyMIME, nil
		}
	}

	// 降级：从 EAS 元数据构造 RFC822 消息
	e.st.mu.Lock()
	var item eas.EmailItem
	for _, it := range e.st.Items[folderID] {
		if it.ServerID == serverID {
			item = it
			break
		}
	}
	e.st.mu.Unlock()
	b := constructRFC822(item)
	if len(b) > 0 {
		_ = os.WriteFile(cachePath, b, 0600)
	}
	return b, nil
}

// constructRFC822 从 EAS 元数据构造最小合法 RFC822 消息。
// H3 修复：所有 header 值先剥 CR/LF，防 header 注入。
func constructRFC822(item eas.EmailItem) []byte {
	sanitize := func(v string) string {
		return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
	}
	var buf bytes.Buffer
	writeHeader := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&buf, "%s: %s\r\n", k, sanitize(v))
		}
	}
	writeHeader("From", item.From)
	writeHeader("To", item.To)
	writeHeader("Cc", item.Cc)
	writeHeader("Subject", item.Subject)
	if !item.DateReceived.IsZero() {
		fmt.Fprintf(&buf, "Date: %s\r\n", item.DateReceived.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	}
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: base64\r\n")
	buf.WriteString("\r\n")
	body := item.Body
	if body == "" {
		body = "(no body)"
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		buf.WriteString(encoded[i:end])
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

// syncCalendar 增量同步日历事件到 st.Events（synckey 由库自动持久化）。
// Coremail 忽略 FilterType，首次全量拉取后靠 synckey 增量。
func (e *syncEngine) syncCalendar(ctx context.Context) error {
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
		if err := e.st.mutate(func() {
			for _, ev := range res.Added {
				e.st.Events[ev.ServerID] = ev
			}
			for _, ev := range res.Changed {
				e.st.Events[ev.ServerID] = ev
			}
			for _, id := range res.Deleted {
				delete(e.st.Events, id)
			}
		}); err != nil {
			return err
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
