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

func TestSyncCalendarRequestsSixMonthWindow(t *testing.T) {
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			CalendarClient: easmock.CalendarClient{
				SyncCalendarFunc: func(_ context.Context, _ string, opts eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
					if opts.DateFilter != eas.FilterSixMonth {
						t.Fatalf("calendar DateFilter = %d, want FilterSixMonth", opts.DateFilter)
					}
					return &eas.CalendarSyncResult{}, nil
				},
			},
		},
	}
	if err := engine.syncCalendarOnce(context.Background()); err != nil {
		t.Fatal(err)
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
	aliases := map[string]calendarEventAlias{}
	deleted := dedupeEquivalentCalendarEventsLocked(events, aliases)
	if fmt.Sprint(deleted) != "[Event:2]" {
		t.Fatalf("deleted = %v, want [Event:2]", deleted)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d, want 5", len(events))
	}
	if got := aliases["Event:2"].CanonicalID; got != "Event:1" {
		t.Fatalf("Event:2 alias = %q, want Event:1", got)
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
	if _, ok := events["Event:5"]; !ok {
		t.Fatal("identical events with different UIDs must survive startup repair")
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
	aliases := map[string]calendarEventAlias{}
	var changes calendarMutations
	index := newCalendarEventDuplicateIndex(events)
	if !changeCalendarEventLocked(events, aliases, index, changedA, &changes) {
		t.Fatal("Changed 事件的新内容与另一事件相同，也必须覆盖自己的 ServerID")
	}
	if got := events[oldA.ServerID].Subject; got != "new subject" {
		t.Fatalf("Event:A subject = %q, want new subject", got)
	}

	duplicateAdd := changedA
	duplicateAdd.ServerID = "Event:C"
	duplicateAdd.UID = "uid-c"
	if !addCalendarEventLocked(events, aliases, index, duplicateAdd, false, &changes) {
		t.Fatal("普通 Add 的内容相同但 UID 不同时必须保留为独立事件")
	}
	single := &eas.CalendarSyncResult{MoreAvailable: true, Added: []eas.EventItem{duplicateAdd}}
	if calendarPageIsDuplicateReplay(single, events, aliases, index) {
		t.Fatal("单个相同内容 Add 不足以证明服务器发生了整页重复回放")
	}
}

func TestCalendarAliasLifecycle(t *testing.T) {
	start := time.Date(2026, time.July, 27, 14, 0, 0, 0, time.UTC)
	canonical := eas.EventItem{
		ServerID:  "Event:A",
		UID:       "uid-a",
		Subject:   "Standup",
		StartTime: start,
		EndTime:   start.Add(time.Hour),
	}
	duplicate := canonical
	duplicate.ServerID = "Event:B"
	duplicate.UID = "rotated-uid"

	t.Run("delete alias keeps canonical", func(t *testing.T) {
		events := map[string]eas.EventItem{canonical.ServerID: canonical}
		aliases := map[string]calendarEventAlias{}
		index := newCalendarEventDuplicateIndex(events)
		var changes calendarMutations
		if addCalendarEventLocked(events, aliases, index, duplicate, true, &changes) {
			t.Fatal("duplicate replay should be folded into an alias")
		}
		if got := aliases[duplicate.ServerID].CanonicalID; got != canonical.ServerID {
			t.Fatalf("alias target = %q, want %q", got, canonical.ServerID)
		}
		if deleteCalendarEventLocked(events, aliases, index, duplicate.ServerID, &changes) {
			t.Fatal("deleting one alias must not remove the visible event")
		}
		if _, ok := events[canonical.ServerID]; !ok {
			t.Fatal("canonical event was removed with its alias")
		}
		if _, ok := aliases[duplicate.ServerID]; ok {
			t.Fatal("deleted alias is still present")
		}
	})

	t.Run("delete canonical promotes alias", func(t *testing.T) {
		events := map[string]eas.EventItem{canonical.ServerID: canonical}
		aliases := map[string]calendarEventAlias{
			duplicate.ServerID: {CanonicalID: canonical.ServerID, UID: duplicate.UID},
		}
		index := newCalendarEventDuplicateIndex(events)
		var changes calendarMutations
		if !deleteCalendarEventLocked(events, aliases, index, canonical.ServerID, &changes) {
			t.Fatal("canonical deletion should change the visible resource path")
		}
		if _, ok := events[canonical.ServerID]; ok {
			t.Fatal("deleted canonical ID is still visible")
		}
		promoted, ok := events[duplicate.ServerID]
		if !ok || promoted.UID != duplicate.UID {
			t.Fatalf("alias was not promoted with its remote identity: %+v", promoted)
		}
	})

	t.Run("changed alias splits when contents diverge", func(t *testing.T) {
		events := map[string]eas.EventItem{canonical.ServerID: canonical}
		aliases := map[string]calendarEventAlias{
			duplicate.ServerID: {CanonicalID: canonical.ServerID, UID: duplicate.UID},
		}
		index := newCalendarEventDuplicateIndex(events)
		changed := duplicate
		changed.Subject = "Independent meeting"
		var changes calendarMutations
		if !changeCalendarEventLocked(events, aliases, index, changed, &changes) {
			t.Fatal("diverging alias Change must create an independent visible event")
		}
		if _, ok := aliases[duplicate.ServerID]; ok {
			t.Fatal("diverging event is still aliased")
		}
		if got := events[duplicate.ServerID].Subject; got != changed.Subject {
			t.Fatalf("changed alias subject = %q, want %q", got, changed.Subject)
		}
	})
}

func TestCalendarReplayAliasesUseRollingPage(t *testing.T) {
	start := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	canonical := eas.EventItem{
		ServerID:  "Event:canonical",
		UID:       "stable-uid",
		Subject:   "Standup",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}
	events := map[string]eas.EventItem{canonical.ServerID: canonical}
	aliases := map[string]calendarEventAlias{
		"Event:old-replay": {
			CanonicalID: canonical.ServerID,
			UID:         "rotated-old",
			Replay:      true,
		},
		"Event:stable-alias": {
			CanonicalID: canonical.ServerID,
			UID:         canonical.UID,
		},
	}
	newReplay := canonical
	newReplay.ServerID = "Event:new-replay"
	newReplay.UID = "rotated-new"
	index := newCalendarEventDuplicateIndex(events)
	var changes calendarMutations

	pruneCalendarReplayAliasesLocked(
		events,
		aliases,
		index,
		[]eas.EventItem{newReplay},
		&changes,
	)
	if _, ok := aliases["Event:old-replay"]; ok {
		t.Fatal("historical replay alias was not pruned")
	}
	if _, ok := aliases["Event:stable-alias"]; !ok {
		t.Fatal("same-UID stable alias must survive replay pruning")
	}
	if addCalendarEventLocked(events, aliases, index, newReplay, true, &changes) {
		t.Fatal("new replay ID should remain hidden")
	}
	if alias := aliases[newReplay.ServerID]; !alias.Replay || alias.CanonicalID != canonical.ServerID {
		t.Fatalf("new replay alias = %+v", alias)
	}
}

func TestCalendarReplayAliasMigrationIsBounded(t *testing.T) {
	canonical := eas.EventItem{
		ServerID: "Event:canonical",
		UID:      "stable-uid",
		Subject:  "Standup",
	}
	events := map[string]eas.EventItem{canonical.ServerID: canonical}
	aliases := map[string]calendarEventAlias{}
	for i := 0; i < maxCalendarStableAliases+5; i++ {
		id := fmt.Sprintf("Event:legacy:%02d", i)
		aliases[id] = calendarEventAlias{
			CanonicalID: canonical.ServerID,
			UID:         canonical.UID,
		}
	}
	current := canonical
	current.ServerID = "Event:current-replay"
	index := newCalendarEventDuplicateIndex(events)
	var changes calendarMutations

	pruneCalendarReplayAliasesLocked(
		events,
		aliases,
		index,
		[]eas.EventItem{current},
		&changes,
	)
	if len(aliases) != maxCalendarStableAliases {
		t.Fatalf("legacy stable aliases = %d, want cap %d", len(aliases), maxCalendarStableAliases)
	}
	if len(changes.aliasDeletes) != 5 {
		t.Fatalf("pruned legacy aliases = %d, want 5", len(changes.aliasDeletes))
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
	existing2 := eas.EventItem{
		ServerID:  "Event:original:2",
		UID:       "stable-uid-2",
		Subject:   "Planning",
		StartTime: start.Add(time.Hour),
		EndTime:   start.Add(2 * time.Hour),
	}
	if err := st.upsertEvent(existing); err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(existing2); err != nil {
		t.Fatal(err)
	}

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
					duplicate2 := existing2
					duplicate2.ServerID = fmt.Sprintf("Event:duplicate:2:%d", calls)
					duplicate2.UID = fmt.Sprintf("server-rotated-uid:2:%d", calls)
					return &eas.CalendarSyncResult{
						MoreAvailable: true,
						Added:         []eas.EventItem{duplicate, duplicate2},
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
	if len(st.Events) != 2 {
		t.Fatalf("cached events = %d, want 2", len(st.Events))
	}
	if _, ok := st.Events[existing.ServerID]; !ok {
		t.Fatal("stable original event was replaced by a duplicate ServerID")
	}
	wantAliases := 2
	if len(st.EventAliases) != wantAliases {
		t.Fatalf("aliases = %d, want %d", len(st.EventAliases), wantAliases)
	}
	reloaded, err := loadState(st.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.EventAliases) != wantAliases {
		t.Fatalf("persisted aliases = %d, want %d", len(reloaded.EventAliases), wantAliases)
	}
	if d := engine.calendarReplayBackoffRemaining(); d <= 0 || d > time.Minute {
		t.Fatalf("duplicate-only round backoff = %v, want (0, 1m]", d)
	}
	if err := engine.syncCalendar(context.Background()); !errors.Is(err, ErrCalendarReplayBackoffSkip) {
		t.Fatalf("duplicate replay backoff error = %v, want ErrCalendarReplayBackoffSkip", err)
	}
	if calls != maxCalendarNoProgressPages {
		t.Fatalf("backoff should suppress another remote page fetch; calls = %d", calls)
	}
}

func TestSyncCalendarResumesAfterDuplicateOnlyPages(t *testing.T) {
	st := mustLoadTestState(t)
	start := time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC)
	existing := eas.EventItem{
		ServerID:  "Event:original",
		UID:       "stable-uid",
		Subject:   "Standup",
		StartTime: start,
		EndTime:   start.Add(30 * time.Minute),
	}
	existing2 := eas.EventItem{
		ServerID:  "Event:original:2",
		UID:       "stable-uid-2",
		Subject:   "Retrospective",
		StartTime: start.Add(time.Hour),
		EndTime:   start.Add(90 * time.Minute),
	}
	realEvent := eas.EventItem{
		ServerID:  "Event:real",
		UID:       "real-uid",
		Subject:   "Planning",
		StartTime: start.Add(2 * time.Hour),
		EndTime:   start.Add(3 * time.Hour),
	}
	if err := st.mutate(func() {
		st.Folders = []eas.Folder{{ServerID: "calendar", Type: eas.FolderTypeCalendar}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(existing); err != nil {
		t.Fatal(err)
	}
	if err := st.upsertEvent(existing2); err != nil {
		t.Fatal(err)
	}

	calls := 0
	newEngine := func(store *diskState) *syncEngine {
		return &syncEngine{
			st: store,
			c: &easmock.Client{
				CalendarClient: easmock.CalendarClient{
					SyncCalendarFunc: func(ctx context.Context, folderID string, _ eas.CalendarSyncOptions) (*eas.CalendarSyncResult, error) {
						calls++
						key, err := store.SyncKey(ctx, folderID)
						if err != nil {
							return nil, err
						}
						var res *eas.CalendarSyncResult
						switch key {
						case "", "k1", "k2":
							page := map[string]int{"": 1, "k1": 2, "k2": 3}[key]
							duplicate := existing
							duplicate.ServerID = fmt.Sprintf("Event:duplicate:%d", page)
							duplicate.UID = fmt.Sprintf("server-rotated-uid:%d", page)
							duplicate2 := existing2
							duplicate2.ServerID = fmt.Sprintf("Event:duplicate:2:%d", page)
							duplicate2.UID = fmt.Sprintf("server-rotated-uid:2:%d", page)
							nextKey := fmt.Sprintf("k%d", page)
							res = &eas.CalendarSyncResult{
								SyncKey:       nextKey,
								MoreAvailable: true,
								Added:         []eas.EventItem{duplicate, duplicate2},
							}
						case "k3":
							res = &eas.CalendarSyncResult{SyncKey: "k4", Added: []eas.EventItem{realEvent}}
						default:
							return nil, fmt.Errorf("unexpected persisted SyncKey %q", key)
						}
						if err := store.SetSyncKey(ctx, folderID, res.SyncKey); err != nil {
							return nil, err
						}
						return res, nil
					},
				},
			},
		}
	}

	engine := newEngine(st)
	if err := engine.syncCalendarOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Events[realEvent.ServerID]; ok {
		t.Fatal("第一轮应在重复页阈值处结束，尚未读取后续真实事件")
	}
	reloaded, err := loadState(st.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.SyncKeys["calendar"]; got != "k3" {
		t.Fatalf("stalled batch persisted SyncKey = %q, want k3", got)
	}
	wantAliases := 2
	if len(reloaded.EventAliases) != wantAliases {
		t.Fatalf("persisted aliases = %d, want %d", len(reloaded.EventAliases), wantAliases)
	}

	if err := newEngine(reloaded).syncCalendarOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Events[realEvent.ServerID]; !ok {
		t.Fatal("下一轮未从重复页之后恢复并读取真实事件")
	}
	if calls != maxCalendarNoProgressPages+1 {
		t.Fatalf("SyncCalendar calls = %d, want %d", calls, maxCalendarNoProgressPages+1)
	}
}

func TestCalendarReplayBackoffClearsAfterNormalCompletion(t *testing.T) {
	engine := &syncEngine{}
	engine.trackCalendarReplayResult(true)
	if d := engine.calendarReplayBackoffRemaining(); d <= 0 {
		t.Fatal("stalled round did not start replay backoff")
	}
	engine.trackCalendarReplayResult(false)
	if d := engine.calendarReplayBackoffRemaining(); d != 0 {
		t.Fatalf("normal completion did not clear replay backoff: %v", d)
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
