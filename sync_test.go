package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func TestSyncBackoffEscalatesAndClears(t *testing.T) {
	status5 := &eas.StatusError{Command: "Sync", Code: 5}
	e := &syncEngine{}
	// 连续 Status 5 按档位升退避（1m→5m→15m→30m，之后封顶）
	want := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		e.trackSyncResult("mail:inbox", status5)
		d := e.backoffRemaining("mail:inbox")
		if d <= 0 || d > w {
			t.Fatalf("第 %d 次失败后退避 = %v，应在 (0, %v]", i+1, d, w)
		}
	}
	// 成功清零
	e.trackSyncResult("mail:inbox", nil)
	if d := e.backoffRemaining("mail:inbox"); d != 0 {
		t.Fatalf("成功后退避应清零，实际 %v", d)
	}
	// 非 Status 5 错误不触发退避（网络抖动不放大）
	e.trackSyncResult("mail:inbox", errors.New("connection reset"))
	if d := e.backoffRemaining("mail:inbox"); d != 0 {
		t.Fatalf("非 Status 5 错误不应退避，实际 %v", d)
	}
}

// 退避期间 syncMail 必须短路：不打服务器、不视为错误（本地 state 照常服务）。
func TestSyncMailSkipsDuringBackoff(t *testing.T) {
	calls := 0
	engine := &syncEngine{
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
					calls++
					return &eas.EmailSyncResult{}, nil
				},
			},
		},
	}
	engine.trackSyncResult("mail:inbox", &eas.StatusError{Command: "Sync", Code: 5})
	if err := engine.syncMail(context.Background(), "inbox"); err != nil {
		t.Fatalf("退避期跳过不应报错: %v", err)
	}
	if calls != 0 {
		t.Fatalf("退避期不应发起 SyncEmail，实际 %d 次", calls)
	}
}

// 过期退避不再拦截，且失败会接着升档。
func TestSyncBackoffExpiryAllowsRetry(t *testing.T) {
	engine := &syncEngine{
		backoff: map[string]folderBackoff{
			"mail:inbox": {failures: 2, nextRetry: time.Now().Add(-time.Second)},
		},
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
					return nil, &eas.StatusError{Command: "Sync", Code: 5}
				},
			},
		},
		st: mustLoadTestState(t),
	}
	err := engine.syncMail(context.Background(), "inbox")
	if !eas.IsStatusCode(err, 5) {
		t.Fatalf("应返回 Status 5，实际 %v", err)
	}
	// 第 3 次失败 → 15m 档
	if d := engine.backoffRemaining("mail:inbox"); d <= 14*time.Minute || d > 15*time.Minute {
		t.Fatalf("第 3 次失败后退避应为 15m 档，实际 %v", d)
	}
}

func mustLoadTestState(t *testing.T) *diskState {
	t.Helper()
	st, err := loadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// 并发调用同一文件夹：flight 合并为一次真实同步，Status 5 只升一档（ZCode H-1）。
func TestSyncBackoffConcurrentFailureCountsOnce(t *testing.T) {
	st := mustLoadTestState(t)
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				SyncEmailFunc: func(context.Context, string, eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
					time.Sleep(50 * time.Millisecond) // 拉大并发窗口
					return nil, &eas.StatusError{Command: "Sync", Code: 5}
				},
			},
		},
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = engine.syncMail(context.Background(), "inbox")
		}()
	}
	wg.Wait()
	engine.backoffMu.Lock()
	got := engine.backoff["mail:inbox"].failures
	engine.backoffMu.Unlock()
	if got != 1 {
		t.Fatalf("8 个并发调用后 failures = %d，应为 1（flight 内计数）", got)
	}
}

// 日历通道退避：短路、不打服务器、不报错。
func TestSyncCalendarSkipsDuringBackoff(t *testing.T) {
	calls := 0
	engine := &syncEngine{
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(context.Context, string, eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					calls++
					return &eas.CalendarSyncResult{}, nil
				},
			},
		},
	}
	engine.trackSyncResult("calendar", &eas.StatusError{Command: "Sync", Code: 5})
	// 语义（P3 起）：退避跳过返回 ErrSyncBackoffSkip sentinel，
	// 供 CalDAV 层区分"跳过"与"成功"（lastCalSync 不推进）
	if err := engine.syncCalendar(context.Background()); !errors.Is(err, ErrSyncBackoffSkip) {
		t.Fatalf("退避期应返回 ErrSyncBackoffSkip，实际 %v", err)
	}
	if calls != 0 {
		t.Fatalf("退避期不应发起 SyncCalendar，实际 %d 次", calls)
	}
}

func TestDedupeEquivalentCalendarEventsKeepsVariants(t *testing.T) {
	start := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	base := eas.EventItem{
		ServerID:  "Event:1",
		UID:       "stable-uid",
		Subject:   "Standup",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}
	duplicate := base
	duplicate.ServerID = "Event:2"
	variant := base
	variant.ServerID = "Event:3"
	variant.Subject = "Standup (updated)"
	noUID := base
	noUID.ServerID = "Event:4"
	noUID.UID = ""
	noUID.Subject = "Independent event"
	differentUIDDuplicate := base
	differentUIDDuplicate.ServerID = "Event:5"
	differentUIDDuplicate.UID = "server-rotated-uid"
	organizerVariant := base
	organizerVariant.ServerID = "Event:6"
	organizerVariant.UID = "independent-uid"
	organizerVariant.OrganizerEmail = "other@example.com"

	events := map[string]eas.EventItem{
		base.ServerID:                  base,
		duplicate.ServerID:             duplicate,
		variant.ServerID:               variant,
		noUID.ServerID:                 noUID,
		differentUIDDuplicate.ServerID: differentUIDDuplicate,
		organizerVariant.ServerID:      organizerVariant,
	}
	deleted := dedupeEquivalentCalendarEventsLocked(events)
	if fmt.Sprint(deleted) != "[Event:2 Event:5]" {
		t.Fatalf("deleted = %v, want [Event:2 Event:5]", deleted)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	if _, ok := events["Event:1"]; !ok {
		t.Fatal("deterministic first ServerID should survive")
	}
	if _, ok := events["Event:3"]; !ok {
		t.Fatal("same-UID variant must survive")
	}
	if _, ok := events["Event:4"]; !ok {
		t.Fatal("different event without UID must survive")
	}
	if _, ok := events["Event:6"]; !ok {
		t.Fatal("same-time event from another organizer must survive")
	}
}

func TestRepairDuplicateCalendarEventsOnStartupPropagatesWriteErrors(t *testing.T) {
	newStateWithDuplicate := func(t *testing.T) *diskState {
		t.Helper()
		st := mustLoadTestState(t)
		event := eas.EventItem{ServerID: "Event:1", UID: "uid-1", Subject: "duplicate"}
		duplicate := event
		duplicate.ServerID = "Event:2"
		duplicate.UID = "uid-2"
		st.Events[event.ServerID] = event
		st.Events[duplicate.ServerID] = duplicate
		return st
	}

	t.Run("snapshot", func(t *testing.T) {
		st := newStateWithDuplicate(t)
		notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(notDirectory, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		st.dir = notDirectory
		if _, err := repairDuplicateCalendarEventsOnStartup(st); err == nil {
			t.Fatal("events.json 快照写失败必须返回错误")
		}
	})

	t.Run("main state", func(t *testing.T) {
		st := newStateWithDuplicate(t)
		notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(notDirectory, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		st.path = filepath.Join(notDirectory, "state.json")
		if _, err := repairDuplicateCalendarEventsOnStartup(st); err == nil {
			t.Fatal("state.json 写失败必须返回错误")
		}
	})
}

func TestChangedCalendarEventIsNotDroppedWhenItMatchesAnotherEvent(t *testing.T) {
	start := time.Date(2026, time.July, 27, 14, 0, 0, 0, time.UTC)
	oldA := eas.EventItem{
		ServerID:  "Event:A",
		UID:       "uid-a",
		Subject:   "old subject",
		StartTime: start,
		EndTime:   start.Add(time.Hour),
	}
	eventB := oldA
	eventB.ServerID = "Event:B"
	eventB.UID = "uid-b"
	eventB.Subject = "new subject"
	changedA := eventB
	changedA.ServerID = oldA.ServerID
	changedA.UID = oldA.UID

	events := map[string]eas.EventItem{
		oldA.ServerID:   oldA,
		eventB.ServerID: eventB,
	}
	index := newCalendarEventDuplicateIndex(events)
	if !changeCalendarEventLocked(events, index, changedA) {
		t.Fatal("Changed 事件的新内容与另一事件相同，也必须覆盖自己的 ServerID")
	}
	if got := events[oldA.ServerID].Subject; got != "new subject" {
		t.Fatalf("Event:A subject = %q, want new subject", got)
	}

	duplicateAdd := changedA
	duplicateAdd.ServerID = "Event:C"
	duplicateAdd.UID = "uid-c"
	if addCalendarEventLocked(events, index, duplicateAdd) {
		t.Fatal("Add 路径仍应抑制跨 ServerID 的等价重复事件")
	}
}

func TestSyncCalendarStopsAfterDuplicateOnlyPages(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	start := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	existing := eas.EventItem{
		ServerID:  "Event:original",
		UID:       "stable-uid",
		Subject:   "Standup",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}
	st.Events[existing.ServerID] = existing

	calls := 0
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(context.Context, string, eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					calls++
					duplicate := existing
					duplicate.ServerID = fmt.Sprintf("Event:duplicate:%d", calls)
					duplicate.UID = fmt.Sprintf("server-rotated-uid:%d", calls)
					return &eas.CalendarSyncResult{
						MoreAvailable: true,
						Added:         []eas.EventItem{duplicate},
					}, nil
				},
			},
		},
	}
	if err := engine.syncCalendarOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != maxCalendarNoProgressPages {
		t.Fatalf("SyncCalendar calls = %d, want %d", calls, maxCalendarNoProgressPages)
	}
	if len(st.Events) != 1 {
		t.Fatalf("cached events = %d, want 1", len(st.Events))
	}
	if _, ok := st.Events[existing.ServerID]; !ok {
		t.Fatal("stable original event was replaced by a duplicate ServerID")
	}
}

func TestSyncCalendarResumesAfterDuplicateOnlyPages(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	start := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	existing := eas.EventItem{
		ServerID:  "Event:original",
		UID:       "stable-uid",
		Subject:   "Standup",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}
	realEvent := eas.EventItem{
		ServerID:  "Event:real",
		UID:       "real-uid",
		Subject:   "Planning",
		StartTime: start.Add(2 * time.Hour),
		EndTime:   start.Add(3 * time.Hour),
	}
	st.Events[existing.ServerID] = existing

	calls := 0
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(context.Context, string, eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					calls++
					if calls <= maxCalendarNoProgressPages {
						duplicate := existing
						duplicate.ServerID = fmt.Sprintf("Event:duplicate:%d", calls)
						duplicate.UID = fmt.Sprintf("server-rotated-uid:%d", calls)
						return &eas.CalendarSyncResult{
							MoreAvailable: true,
							Added:         []eas.EventItem{duplicate},
						}, nil
					}
					return &eas.CalendarSyncResult{Added: []eas.EventItem{realEvent}}, nil
				},
			},
		},
	}

	if err := engine.syncCalendarOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Events[realEvent.ServerID]; ok {
		t.Fatal("第一轮应在重复页阈值处结束，尚未读取后续真实事件")
	}
	if err := engine.syncCalendarOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Events[realEvent.ServerID]; !ok {
		t.Fatal("下一轮未从重复页之后恢复并读取真实事件")
	}
	if calls != maxCalendarNoProgressPages+1 {
		t.Fatalf("SyncCalendar calls = %d, want %d", calls, maxCalendarNoProgressPages+1)
	}
}

// 升档封顶：连续 50 次失败不越界、停在 30m 档。
func TestSyncBackoffCapsAtMaxStep(t *testing.T) {
	e := &syncEngine{}
	status5 := &eas.StatusError{Command: "Sync", Code: 5}
	for i := 0; i < 50; i++ {
		e.trackSyncResult("mail:inbox", status5)
	}
	d := e.backoffRemaining("mail:inbox")
	if d <= 29*time.Minute || d > 30*time.Minute {
		t.Fatalf("第 50 次失败后退避应为 30m 封顶档，实际 %v", d)
	}
}

// poller 并发：文件夹 A 的同步等 B 启动后才返回——串行实现必然超时失败。
func TestPollerSyncsFoldersConcurrently(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{
		{ServerID: "a", Type: eas.FolderTypeInbox},
		{ServerID: "b", Type: eas.FolderTypeUserMail},
	}
	startedB := make(chan struct{})
	var once sync.Once
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				SyncEmailFunc: func(_ context.Context, fid string, _ eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
					switch fid {
					case "a":
						select {
						case <-startedB:
						case <-time.After(2 * time.Second):
							return nil, errors.New("B 未在 A 期间启动——poller 是串行的")
						}
					case "b":
						once.Do(func() { close(startedB) })
					}
					return &eas.EmailSyncResult{}, nil
				},
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		engine.poller(ctx, 20*time.Millisecond, func(string) {})
		close(done)
	}()
	select {
	case <-startedB:
	case <-time.After(3 * time.Second):
		t.Fatal("3s 内 B 未启动")
	}
	time.Sleep(150 * time.Millisecond) // 让这一轮完整跑完
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("poller 未在 cancel 后退出")
	}
}
