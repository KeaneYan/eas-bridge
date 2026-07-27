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
	asVersion                  = "14.0"
	maxCalendarNoProgressPages = 3
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
	// Some EAS implementations keep returning the same logical events with new
	// ServerIDs while MoreAvailable remains set. Older versions accumulated every
	// copy, which could leave tens of thousands of duplicate CalDAV resources and
	// make desktop calendar clients unusable. Repair exact duplicates eagerly and
	// compact the shard once so subsequent startups do not replay a huge snapshot.
	duplicateEventIDs, cleanupErr := repairDuplicateCalendarEventsOnStartup(st)
	if cleanupErr != nil {
		return nil, fmt.Errorf("清理重复日历事件: %w", cleanupErr)
	}
	if len(duplicateEventIDs) > 0 {
		log.Printf("[state] 清理 %d 条重复日历事件", len(duplicateEventIDs))
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

// ErrSyncBackoffSkip 日历同步因 Status 5 退避被跳过。
// 与 syncMail 退避返回 nil 不同（IMAP SELECT/STATUS 会把错误透传给客户端，
// 退避不该吓客户端），CalDAV 路径在 maybeSyncCalendar 内部消化这个 sentinel——
// 用它区分"跳过"与"成功"，lastCalSync 只在真成功时推进（ZCode backoff M-2）。
var ErrSyncBackoffSkip = errors.New("Status 5 退避期，本次日历同步跳过")

// syncCalendar 增量同步日历事件到 st.Events（synckey 由库自动持久化）。
// 首次全量显式请求 EAS 支持的最大六个月窗口，之后靠 synckey 增量。
func (e *syncEngine) syncCalendar(ctx context.Context) error {
	const key = "calendar"
	if e.skipBackoff(key) {
		return ErrSyncBackoffSkip // 退避期：本地事件缓存照常服务 CalDAV 读
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
	// Directly constructed engines in tests and long-running processes upgraded
	// in place may not have passed through newSyncEngine's startup repair.
	e.st.mu.Lock()
	duplicateEventIDs := dedupeEquivalentCalendarEventsLocked(e.st.Events, e.st.EventAliases)
	var cleanupErr error
	if len(duplicateEventIDs) > 0 {
		var cleanup calendarMutations
		for _, id := range duplicateEventIDs {
			cleanup.recordDelete(id)
			cleanup.recordAlias(id, e.st.EventAliases[id])
		}
		cleanupErr = e.st.saveCalendarLocked(cleanup)
	}
	e.st.mu.Unlock()
	if cleanupErr != nil {
		return cleanupErr
	}
	if len(duplicateEventIDs) > 0 {
		log.Printf("[sync] 清理 %d 条重复日历事件", len(duplicateEventIDs))
	}

	retried := false
	var pending calendarMutations
	noProgressPages := 0
	for page := 0; page < 100; page++ {
		res, err := e.c.SyncCalendar(ctx, calFolderID, eas.CalendarSyncOptions{
			WindowSize: 100,
			DateFilter: eas.FilterSixMonth,
		})
		if err != nil {
			// 与 syncMailOnce 同理：Coremail 对失效 synckey 回 Status 5，清 key 重拉一次
			if eas.IsStatusCode(err, 5) && !retried {
				log.Printf("[sync] 日历 Status 5，按失效 synckey 处理：重置并全量重拉")
				if rerr := e.st.resetSyncKey(calFolderID); rerr != nil {
					log.Printf("[sync] 重置日历 synckey 失败: %v", rerr)
				}
				retried = true
				// 清掉重置前累积的批次，避免与全量重拉重复追加（幂等无害但冗余）
				pending = calendarMutations{}
				noProgressPages = 0
				page = -1
				continue
			}
			return fmt.Errorf("SyncCalendar: %w", err)
		}
		e.st.mu.Lock()
		pageProgress := false
		duplicateIndex := newCalendarEventDuplicateIndex(e.st.Events)
		duplicateReplay := calendarPageIsDuplicateReplay(res, e.st.Events, e.st.EventAliases, duplicateIndex)
		if duplicateReplay {
			pruneCalendarReplayAliasesLocked(
				e.st.Events,
				e.st.EventAliases,
				duplicateIndex,
				res.Added,
				&pending,
			)
		}
		for _, ev := range res.Added {
			if addCalendarEventLocked(e.st.Events, e.st.EventAliases, duplicateIndex, ev, duplicateReplay, &pending) {
				pageProgress = true
			}
		}
		for _, ev := range res.Changed {
			if changeCalendarEventLocked(e.st.Events, e.st.EventAliases, duplicateIndex, ev, &pending) {
				pageProgress = true
			}
		}
		for _, id := range res.Deleted {
			if deleteCalendarEventLocked(e.st.Events, e.st.EventAliases, duplicateIndex, id, &pending) {
				pageProgress = true
			}
		}
		if pageProgress {
			noProgressPages = 0
		} else {
			noProgressPages++
		}
		stalled := res.MoreAvailable && noProgressPages >= maxCalendarNoProgressPages
		shouldSave := (page+1)%10 == 0 || !res.MoreAvailable || stalled || page == 99
		var saveErr error
		if shouldSave {
			// 事件变更按批次追加 events.jsonl（与 synckey 落盘同节奏：
			// 崩溃丢的是未落盘批次的变更，而 key 也未推进，重拉自然补回）
			saveErr = e.st.saveCalendarLocked(pending)
			pending = calendarMutations{}
		}
		e.st.mu.Unlock()
		if saveErr != nil {
			return saveErr
		}
		if !res.MoreAvailable {
			return nil
		}
		if stalled {
			// The EAS client has already accepted this page's SyncKey and
			// saveCalendarLocked persisted it above. If duplicate-only pages
			// precede a real change, the next polling pass resumes after these
			// pages instead of replaying them forever.
			log.Printf("[sync] 日历连续 %d 页没有逻辑变化，忽略异常的 MoreAvailable 并结束本轮同步", noProgressPages)
			return nil
		}
	}
	return fmt.Errorf("日历同步超过 100 页仍未完成")
}

func calendarEventsEqualExceptServerID(a, b eas.EventItem) bool {
	a.ServerID = ""
	b.ServerID = ""
	return reflect.DeepEqual(a, b)
}

// calendarEventsSameContent intentionally ignores both remote identity fields.
// It is only safe after the surrounding page has been identified as a replay;
// using it for arbitrary Add records would hide legitimate identical meetings.
func calendarEventsSameContent(a, b eas.EventItem) bool {
	a.ServerID = ""
	a.UID = ""
	b.ServerID = ""
	b.UID = ""
	return reflect.DeepEqual(a, b)
}

type calendarEventDuplicateKey struct {
	subject        string
	location       string
	body           string
	start          time.Time
	end            time.Time
	allDay         bool
	organizerEmail string
}

func calendarDuplicateKey(ev eas.EventItem) calendarEventDuplicateKey {
	return calendarEventDuplicateKey{
		subject:        ev.Subject,
		location:       ev.Location,
		body:           ev.Body,
		start:          ev.StartTime,
		end:            ev.EndTime,
		allDay:         ev.AllDayEvent,
		organizerEmail: ev.OrganizerEmail,
	}
}

type calendarEventDuplicateIndex map[calendarEventDuplicateKey]map[string]eas.EventItem

func newCalendarEventDuplicateIndex(events map[string]eas.EventItem) calendarEventDuplicateIndex {
	index := make(calendarEventDuplicateIndex)
	for id, ev := range events {
		index.add(id, ev)
	}
	return index
}

func (index calendarEventDuplicateIndex) findEquivalent(ev eas.EventItem, ignoreUID bool) (string, bool) {
	var match string
	for id, existing := range index[calendarDuplicateKey(ev)] {
		equal := calendarEventsEqualExceptServerID(existing, ev)
		if ignoreUID {
			equal = calendarEventsSameContent(existing, ev)
		}
		if equal && (match == "" || id < match) {
			match = id
		}
	}
	return match, match != ""
}

func (index calendarEventDuplicateIndex) add(id string, ev eas.EventItem) {
	key := calendarDuplicateKey(ev)
	if index[key] == nil {
		index[key] = make(map[string]eas.EventItem)
	}
	index[key][id] = ev
}

func (index calendarEventDuplicateIndex) remove(id string, ev eas.EventItem) {
	key := calendarDuplicateKey(ev)
	delete(index[key], id)
	if len(index[key]) == 0 {
		delete(index, key)
	}
}

// dedupeEquivalentCalendarEventsLocked repairs only records with the same
// non-empty iCalendar UID and identical content. Different UIDs are not enough
// evidence for destructive startup cleanup: two real meetings may otherwise be
// byte-identical. Suppressed ServerIDs remain as aliases for later Change/Delete.
func dedupeEquivalentCalendarEventsLocked(events map[string]eas.EventItem, aliases map[string]calendarEventAlias) []string {
	ids := make([]string, 0, len(events))
	for id := range events {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	representatives := make(calendarEventDuplicateIndex)
	var duplicates []string
	for _, id := range ids {
		ev := events[id]
		if ev.UID == "" {
			representatives.add(id, ev)
			continue
		}
		if canonicalID, ok := representatives.findEquivalent(ev, false); ok {
			delete(events, id)
			aliases[id] = calendarEventAlias{CanonicalID: canonicalID, UID: ev.UID}
			duplicates = append(duplicates, id)
			continue
		}
		representatives.add(id, ev)
	}
	return duplicates
}

// repairDuplicateCalendarEventsOnStartup compacts the repaired event map before
// startup proceeds. Returning either snapshot or main-state write errors is
// essential: otherwise duplicates removed only from memory reappear on restart.
func repairDuplicateCalendarEventsOnStartup(st *diskState) ([]string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	duplicateEventIDs := dedupeEquivalentCalendarEventsLocked(st.Events, st.EventAliases)
	if len(duplicateEventIDs) == 0 {
		return nil, nil
	}
	if err := st.writeEventsSnapshotLocked(); err != nil {
		return nil, err
	}
	if err := st.saveMainLocked(); err != nil {
		return nil, err
	}
	return duplicateEventIDs, nil
}

func canonicalCalendarEvent(events map[string]eas.EventItem, aliases map[string]calendarEventAlias, serverID string) (eas.EventItem, string, bool) {
	if ev, ok := events[serverID]; ok {
		return ev, serverID, true
	}
	alias, ok := aliases[serverID]
	if !ok {
		return eas.EventItem{}, "", false
	}
	ev, ok := events[alias.CanonicalID]
	return ev, alias.CanonicalID, ok
}

// calendarPageIsDuplicateReplay uses page-level evidence before ignoring a
// rotated UID. Coremail has been observed returning whole MoreAvailable pages
// whose Add records are byte-identical to cached events except for ServerID/UID.
func calendarPageIsDuplicateReplay(
	res *eas.CalendarSyncResult,
	events map[string]eas.EventItem,
	aliases map[string]calendarEventAlias,
	index calendarEventDuplicateIndex,
) bool {
	// A single identical Add is still plausibly a real independent meeting.
	// The observed Coremail failure repeats batches, so require corroboration
	// from at least two records before ignoring rotated UIDs.
	if !res.MoreAvailable || len(res.Added) < 2 || len(res.Changed) > 0 || len(res.Deleted) > 0 {
		return false
	}
	for _, ev := range res.Added {
		if existing, _, ok := canonicalCalendarEvent(events, aliases, ev.ServerID); ok {
			if !calendarEventsSameContent(existing, ev) {
				return false
			}
			continue
		}
		if _, ok := index.findEquivalent(ev, true); !ok {
			return false
		}
	}
	return true
}

// pruneCalendarReplayAliasesLocked keeps only the replay IDs present on the
// current duplicate page for each affected canonical event. Coremail can mint
// fresh IDs forever; retaining every historical replay ID would merely replace
// full-event cache bloat with unbounded alias bloat.
func pruneCalendarReplayAliasesLocked(
	events map[string]eas.EventItem,
	aliases map[string]calendarEventAlias,
	index calendarEventDuplicateIndex,
	added []eas.EventItem,
	changes *calendarMutations,
) {
	keep := map[string]map[string]struct{}{}
	for _, ev := range added {
		_, canonicalID, ok := canonicalCalendarEvent(events, aliases, ev.ServerID)
		if !ok {
			canonicalID, ok = index.findEquivalent(ev, true)
		}
		if !ok {
			continue
		}
		if keep[canonicalID] == nil {
			keep[canonicalID] = map[string]struct{}{}
		}
		keep[canonicalID][ev.ServerID] = struct{}{}
	}
	for aliasID, alias := range aliases {
		currentIDs, touched := keep[alias.CanonicalID]
		if !alias.Replay || !touched {
			continue
		}
		if _, current := currentIDs[aliasID]; current {
			continue
		}
		delete(aliases, aliasID)
		changes.recordAliasDelete(aliasID)
	}
}

// addCalendarEventLocked applies EAS Add semantics. A new ServerID is folded
// only when it shares a stable UID or belongs to a page-level replay. The alias
// preserves the remote identity for future incremental Change/Delete commands.
func addCalendarEventLocked(
	events map[string]eas.EventItem,
	aliases map[string]calendarEventAlias,
	index calendarEventDuplicateIndex,
	ev eas.EventItem,
	duplicateReplay bool,
	changes *calendarMutations,
) bool {
	if ev.ServerID == "" {
		return false
	}
	if existing, ok := events[ev.ServerID]; ok {
		if reflect.DeepEqual(existing, ev) {
			return false
		}
		index.remove(ev.ServerID, existing)
		events[ev.ServerID] = ev
		index.add(ev.ServerID, ev)
		changes.recordUpsert(ev)
		return true
	}
	if alias, ok := aliases[ev.ServerID]; ok {
		if existing, ok := events[alias.CanonicalID]; ok && calendarEventsSameContent(existing, ev) {
			if alias.UID != ev.UID {
				alias.UID = ev.UID
				aliases[ev.ServerID] = alias
				changes.recordAlias(ev.ServerID, alias)
			}
			return false
		}
		delete(aliases, ev.ServerID)
		changes.recordAliasDelete(ev.ServerID)
	}

	var canonicalID string
	var equivalent bool
	replayAlias := false
	if ev.UID != "" {
		canonicalID, equivalent = index.findEquivalent(ev, false)
	}
	if !equivalent && duplicateReplay {
		canonicalID, equivalent = index.findEquivalent(ev, true)
		replayAlias = equivalent
	}
	if equivalent {
		alias := calendarEventAlias{CanonicalID: canonicalID, UID: ev.UID, Replay: replayAlias}
		aliases[ev.ServerID] = alias
		changes.recordAlias(ev.ServerID, alias)
		return false
	}
	events[ev.ServerID] = ev
	index.add(ev.ServerID, ev)
	changes.recordUpsert(ev)
	return true
}

// changeCalendarEventLocked updates a canonical record directly. A changed alias
// that diverges is split back into an independent event instead of overwriting or
// deleting the canonical meeting.
func changeCalendarEventLocked(
	events map[string]eas.EventItem,
	aliases map[string]calendarEventAlias,
	index calendarEventDuplicateIndex,
	ev eas.EventItem,
	changes *calendarMutations,
) bool {
	if ev.ServerID == "" {
		return false
	}
	if existing, ok := events[ev.ServerID]; ok {
		if reflect.DeepEqual(existing, ev) {
			return false
		}
		index.remove(ev.ServerID, existing)
		events[ev.ServerID] = ev
		index.add(ev.ServerID, ev)
		changes.recordUpsert(ev)
		return true
	}
	if alias, ok := aliases[ev.ServerID]; ok {
		if existing, ok := events[alias.CanonicalID]; ok && calendarEventsSameContent(existing, ev) {
			if alias.UID != ev.UID {
				alias.UID = ev.UID
				aliases[ev.ServerID] = alias
				changes.recordAlias(ev.ServerID, alias)
			}
			return false
		}
		delete(aliases, ev.ServerID)
		changes.recordAliasDelete(ev.ServerID)
	}
	events[ev.ServerID] = ev
	index.add(ev.ServerID, ev)
	changes.recordUpsert(ev)
	return true
}

// deleteCalendarEventLocked removes one remote identity. Deleting an alias does
// not delete the visible event; deleting the canonical ID promotes a remaining
// alias so the logical meeting survives until the server deletes its last ID.
func deleteCalendarEventLocked(
	events map[string]eas.EventItem,
	aliases map[string]calendarEventAlias,
	index calendarEventDuplicateIndex,
	serverID string,
	changes *calendarMutations,
) bool {
	if _, ok := aliases[serverID]; ok {
		delete(aliases, serverID)
		changes.recordAliasDelete(serverID)
		return false
	}
	existing, ok := events[serverID]
	if !ok {
		return false
	}

	var members []string
	for aliasID, alias := range aliases {
		if alias.CanonicalID == serverID {
			members = append(members, aliasID)
		}
	}
	if len(members) == 0 {
		index.remove(serverID, existing)
		delete(events, serverID)
		changes.recordDelete(serverID)
		return true
	}

	sort.Strings(members)
	promotedID := members[0]
	promotedAlias := aliases[promotedID]
	delete(aliases, promotedID)
	changes.recordAliasDelete(promotedID)

	index.remove(serverID, existing)
	delete(events, serverID)
	changes.recordDelete(serverID)

	promoted := existing
	promoted.ServerID = promotedID
	promoted.UID = promotedAlias.UID
	events[promotedID] = promoted
	index.add(promotedID, promoted)
	changes.recordUpsert(promoted)

	for _, aliasID := range members[1:] {
		alias := aliases[aliasID]
		alias.CanonicalID = promotedID
		aliases[aliasID] = alias
		changes.recordAlias(aliasID, alias)
	}
	return true
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
