package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachePrunerStopsWithLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	engine := &syncEngine{st: mustLoadTestState(t)}
	done := engine.scheduleCachePrune(ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cache pruner did not stop after lifecycle cancellation")
	}
}

func TestCachePathsAreVersionedAndPathSafe(t *testing.T) {
	dir := messageCacheDir("../../folder", "../../message")
	if !strings.Contains(dir, string(filepath.Separator)+mimeCacheVersion+string(filepath.Separator)) {
		t.Fatalf("cache path %q does not contain version %q", dir, mimeCacheVersion)
	}
	relative, err := filepath.Rel(filepath.Join(mimeCacheDir(), mimeCacheVersion), dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(relative, "..") {
		t.Fatalf("cache path escaped root: %q", dir)
	}
	if strings.Contains(filepath.Base(dir), "..") {
		t.Fatalf("unsafe cache component %q", filepath.Base(dir))
	}
}

func TestAtomicWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "message.eml")
	want := bytes.Repeat([]byte("message"), 128)
	if err := atomicWriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("atomicWriteFile content mismatch")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestFlightGroupDeduplicatesConcurrentCalls(t *testing.T) {
	var group flightGroup
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := group.Do("same-key", func() (any, error) {
			calls.Add(1)
			close(started)
			<-release
			return "ok", nil
		})
		if err != nil {
			t.Errorf("leader Do: %v", err)
		}
	}()
	<-started

	observed := make([]chan struct{}, 7)
	for i := range observed {
		observed[i] = make(chan struct{})
		ctx := &doneObservedContext{
			Context:  context.Background(),
			observed: observed[i],
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := group.DoContext(ctx, "same-key", func() (any, error) {
				calls.Add(1)
				return "ok", nil
			})
			if err != nil {
				t.Errorf("waiter Do: %v", err)
			}
		}()
	}
	for i, ch := range observed {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d did not join the active flight", i)
		}
	}
	releaseAll()
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want 1", got)
	}
}

// doneObservedContext reports when DoContext reaches the waiter select. The
// callback cannot close this signal because the flight leader path never reads
// ctx.Done(), so the test can release the leader without scheduler-dependent
// sleeps.
type doneObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (ctx *doneObservedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.Context.Done()
}

func TestPruneMIMECacheRemovesLegacyVersions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	legacy := filepath.Join(mimeCacheDir(), "legacy-folder", "message.eml")
	current := messageMetadataPath("folder", "message")
	if err := atomicWriteFile(legacy, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(current, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := pruneMIMECache(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy cache still exists: %v", err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current cache was removed: %v", err)
	}
}

func TestFlightGroupWaiterRespectsCancellation(t *testing.T) {
	var group flightGroup
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = group.Do("shared", func() (any, error) {
			close(started)
			<-release
			return nil, nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := group.DoContext(ctx, "shared", func() (any, error) {
		t.Fatal("canceled waiter unexpectedly ran its function")
		return nil, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DoContext error = %v, want context.Canceled", err)
	}
	close(release)
	<-finished
}

// 不同 key 各自独立执行（互不去重）。
func TestFlightGroupDistinctKeysIndependent(t *testing.T) {
	var g flightGroup
	var calls int32
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			if _, err := g.DoContext(context.Background(), k, func() (any, error) {
				atomic.AddInt32(&calls, 1)
				return nil, nil
			}); err != nil {
				t.Error(err)
			}
		}(key)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("3 个不同 key 应各执行一次，实际 %d", got)
	}
}
