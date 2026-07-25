package main

import (
	"context"
	"errors"
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
	if err := engine.syncCalendar(context.Background()); err != nil {
		t.Fatalf("退避期跳过不应报错: %v", err)
	}
	if calls != 0 {
		t.Fatalf("退避期不应发起 SyncCalendar，实际 %d 次", calls)
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
