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
	"sync"
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

	backoffMu sync.Mutex
	backoff   map[string]folderBackoff
}

// folderBackoff 记录单个同步通道（邮件文件夹/日历）的连续 Status 5 退避状态。
type folderBackoff struct {
	failures    int
	nextRetry   time.Time
	lastSkipLog time.Time
}

// syncBackoffSteps 连续第 N 次 Status 5 后的退避时长（超出最后一档按封顶值）。
// 背景：2026-07-25 凌晨 mm.tenbank.com 故障 6 小时，全文件夹 Status 5，
// 桥每分钟对每个文件夹"清 key + 全量重拉 6 个月邮件"，既打服务器又反复
// 摧毁本地同步状态。退避把故障期的重拉频率压到最多 2 次/小时/文件夹。
var syncBackoffSteps = []time.Duration{
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

// backoffRemaining 返回 key 当前剩余的退避时间（0 表示可以同步）。
func (e *syncEngine) backoffRemaining(key string) time.Duration {
	e.backoffMu.Lock()
	defer e.backoffMu.Unlock()
	b, ok := e.backoff[key]
	if !ok {
		return 0
	}
	if d := time.Until(b.nextRetry); d > 0 {
		return d
	}
	return 0
}

// skipBackoff 报告 key 是否处于退避期；处于时按 10 分钟/通道的频率上限打一条
// 跳过日志（退避期不能全静默——30m 封顶档下故障几小时也该在日志里看得见）。
func (e *syncEngine) skipBackoff(key string) bool {
	d := e.backoffRemaining(key)
	if d <= 0 {
		return false
	}
	e.backoffMu.Lock()
	if b, ok := e.backoff[key]; ok && time.Since(b.lastSkipLog) >= 10*time.Minute {
		b.lastSkipLog = time.Now()
		e.backoff[key] = b
		log.Printf("[sync] %s 处于 Status 5 退避期（剩余 ~%s），本轮跳过，本地数据照常服务", key, d.Round(time.Second))
	}
	e.backoffMu.Unlock()
	return true
}

// trackSyncResult 按同步结果更新退避状态：成功清零；Status 5 升档；
// 其他错误（网络抖动等）不影响退避（它们不触发清 key 全量重拉，危害不同）。
func (e *syncEngine) trackSyncResult(key string, err error) {
	e.backoffMu.Lock()
	defer e.backoffMu.Unlock()
	if err == nil {
		delete(e.backoff, key)
		return
	}
	if !eas.IsStatusCode(err, 5) {
		return
	}
	b := e.backoff[key]
	b.failures++
	step := syncBackoffSteps[min(b.failures, len(syncBackoffSteps))-1]
	b.nextRetry = time.Now().Add(step)
	if e.backoff == nil {
		e.backoff = map[string]folderBackoff{}
	}
	e.backoff[key] = b
	log.Printf("[sync] %s 连续第 %d 次 Status 5，退避 %s 后重试", key, b.failures, step)
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
	engine := &syncEngine{cfg: cfg, st: st, c: c, backoff: map[string]folderBackoff{}}
	engine.scheduleCachePrune()
	return engine, nil
}

func (e *syncEngine) syncFolders(ctx context.Context) error {
	res, err := e.c.FolderSync(ctx)
	if err != nil {
		return err
	}
	if len(res.Added) == 0 && len(res.Updated) == 0 && len(res.Deleted) == 0 {
		return nil // 无变更不落盘，避免每轮启动/同步都重写 state.json
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
	key := "mail:" + folderID
	if e.skipBackoff(key) {
		return nil // 退避期：本轮跳过，本地 state 照常服务 IMAP 读
	}
	_, err := e.flights.DoContext(ctx, "sync-mail:"+folderID, func() (any, error) {
		// trackSyncResult 放 flight 内：并发调用合并为一次执行，失败只计一次
		err := e.syncMailOnce(ctx, folderID)
		e.trackSyncResult(key, err)
		return nil, err
	})
	return err
}

func (e *syncEngine) syncMailOnce(ctx context.Context, folderID string) error {
	// EAS Sync 按 WindowSize 分页，单页最多 200 封；MoreAvailable 时必须翻页拉完，
	// 否则超过 200 封的文件夹只能看到最新的一页（老邮件不可见）。
	// 中途出错时先落盘已拉到的页（synckey 按页持久化，下次从断点续拉），再返回错误。
	keyBefore, _ := e.st.SyncKey(ctx, folderID)
	var added, changed []eas.EmailItem
	var deleted []string
	var syncErr error
	retried := false
	for page := 0; ; page++ {
		if page >= 500 {
			syncErr = fmt.Errorf("邮件同步超过 500 页仍未完成")
			break
		}
		res, err := e.c.SyncEmail(ctx, folderID, eas.EmailSyncOptions{
			WindowSize: 200,
			BodyType:   eas.BodyTypePlain,
			// Coremail 把 FilterType=0（协议未定义值）当作默认短窗口（实测只回最近 ~2 周），
			// 显式指定 6 个月窗口——这是 EAS 协议支持的最大范围。
			DateFilter: eas.FilterSixMonth,
		})
		if err != nil {
			// Coremail 对失效 synckey 回 Status 5 而非规范的 3（fork 不会自动重置）。
			// 自愈：清 key 全量重拉一次；再失败才视为真实故障。
			if eas.IsStatusCode(err, 5) && !retried {
				log.Printf("[sync] %s Status 5，按失效 synckey 处理：重置并全量重拉", folderID)
				if rerr := e.st.resetSyncKey(folderID); rerr != nil {
					log.Printf("[sync] 重置 synckey 失败: %v", rerr)
				}
				retried = true
				added, changed, deleted = nil, nil, nil
				page = -1
				continue
			}
			syncErr = err
			break
		}
		added = append(added, res.Added...)
		changed = append(changed, res.Changed...)
		deleted = append(deleted, res.Deleted...)
		if !res.MoreAvailable {
			break
		}
	}
	// 无变更：不失效缓存。但 synckey 前进必须落盘——Coremail 对旧 key 判失效
	// 回 Status 5（2026-07-24 实测：重启从两天前的落盘 key 恢复，全文件夹被拒）。
	if len(added) == 0 && len(changed) == 0 && len(deleted) == 0 {
		if keyAfter, _ := e.st.SyncKey(ctx, folderID); keyAfter != keyBefore {
			if err := e.st.saveNow(); err != nil {
				log.Printf("[sync] %s synckey 落盘失败: %v", folderID, err)
			}
		}
		return syncErr
	}
	invalidated := make([]string, 0, len(added)+len(changed)+len(deleted))
	err := e.st.mutateFolder(folderID, func() {
		existing := map[string]eas.EmailItem{}
		order := make([]string, 0, len(e.st.Items[folderID])+len(added))
		for _, it := range e.st.Items[folderID] {
			existing[it.ServerID] = it
			order = append(order, it.ServerID)
		}
		for _, it := range added {
			if _, exists := existing[it.ServerID]; !exists {
				order = append(order, it.ServerID)
			}
			existing[it.ServerID] = it
			invalidated = append(invalidated, it.ServerID)
		}
		for _, it := range changed {
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
		for _, id := range deleted {
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
	// 已拉取的页已安全落盘；把翻页中途的错误（如有）返回给调用方记日志
	return syncErr
}

// trashFolderID 返回服务器"已删除"文件夹（EAS DeletesAsMoves 目标）。
func (e *syncEngine) trashFolderID() (string, error) {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, f := range e.st.Folders {
		if f.Type == eas.FolderTypeDeletedItems {
			return f.ServerID, nil
		}
	}
	return "", fmt.Errorf("服务器没有已删除文件夹")
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
	const key = "calendar"
	if e.skipBackoff(key) {
		return nil // 退避期：本轮跳过，本地事件缓存照常服务 CalDAV 读
	}
	_, err := e.flights.DoContext(ctx, "sync-calendar", func() (any, error) {
		err := e.syncCalendarOnce(ctx)
		e.trackSyncResult(key, err)
		return nil, err
	})
	return err
}

// calendarFolderID 返回服务器日历文件夹 ID。
func (e *syncEngine) calendarFolderID() (string, error) {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, f := range e.st.Folders {
		if f.Type == eas.FolderTypeCalendar {
			return f.ServerID, nil
		}
	}
	return "", fmt.Errorf("服务器没有日历文件夹")
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
	retried := false
	var pendingUpserts []eas.EventItem
	var pendingDeletes []string
	for page := 0; page < 100; page++ {
		res, err := e.c.SyncCalendar(ctx, calFolderID, eas.CalendarSyncOptions{WindowSize: 100})
		if err != nil {
			// 与 syncMailOnce 同理：Coremail 对失效 synckey 回 Status 5，清 key 重拉一次
			if eas.IsStatusCode(err, 5) && !retried {
				log.Printf("[sync] 日历 Status 5，按失效 synckey 处理：重置并全量重拉")
				if rerr := e.st.resetSyncKey(calFolderID); rerr != nil {
					log.Printf("[sync] 重置日历 synckey 失败: %v", rerr)
				}
				retried = true
				// 清掉重置前累积的批次，避免与全量重拉重复追加（幂等无害但冗余）
				pendingUpserts, pendingDeletes = nil, nil
				page = -1
				continue
			}
			return fmt.Errorf("SyncCalendar: %w", err)
		}
		e.st.mu.Lock()
		for _, ev := range res.Added {
			e.st.Events[ev.ServerID] = ev
			pendingUpserts = append(pendingUpserts, ev)
		}
		for _, ev := range res.Changed {
			e.st.Events[ev.ServerID] = ev
			pendingUpserts = append(pendingUpserts, ev)
		}
		for _, id := range res.Deleted {
			delete(e.st.Events, id)
			pendingDeletes = append(pendingDeletes, id)
		}
		shouldSave := (page+1)%10 == 0 || !res.MoreAvailable || page == 99
		var saveErr error
		if shouldSave {
			// 事件变更按批次追加 events.jsonl（与 synckey 落盘同节奏：
			// 崩溃丢的是未落盘批次的变更，而 key 也未推进，重拉自然补回）
			saveErr = e.st.saveCalendarLocked(pendingUpserts, pendingDeletes)
			pendingUpserts, pendingDeletes = nil, nil
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

// mailFolderIDs 返回当前所有可同步邮件文件夹的 ServerID（每轮重新读，新文件夹自动纳入）。
// 基于 isMailFolderType（folders.go）。
func (e *syncEngine) mailFolderIDs() []string {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	var ids []string
	for _, f := range e.st.Folders {
		if isMailFolderType(f.Type) {
			ids = append(ids, f.ServerID)
		}
	}
	return ids
}

func (e *syncEngine) poller(ctx context.Context, interval time.Duration, onChange func(folderID string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 轮询所有邮件文件夹（每轮重读列表），跳过日历/联系人等非邮件文件夹避免 SyncEmail 报错
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 各文件夹并发同步（full-review ROI-5）：串行时大文件夹拖慢其他
			// 文件夹时效。syncMail 内部按文件夹 singleflight，并发安全；
			// SELECT/STATUS 路径本就会与轮询并发打同一服务器。
			var wg sync.WaitGroup
			for _, fid := range e.mailFolderIDs() {
				if e.skipBackoff("mail:" + fid) {
					continue // 退避期跳过：不同步也不广播（本地无新数据可通知）
				}
				wg.Add(1)
				go func(fid string) {
					defer wg.Done()
					if err := e.syncMail(ctx, fid); err != nil {
						// 单文件夹失败不影响其他文件夹；临时网络错误静默，持续错误记日志但不崩溃
						if !errors.Is(err, context.Canceled) {
							log.Printf("[poll] syncMail %s: %v", fid, err)
						}
						return
					}
					onChange(fid)
				}(fid)
			}
			wg.Wait()
		}
	}
}
