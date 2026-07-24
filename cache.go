package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	mimeCacheVersion  = "v3"
	mimeCacheMaxBytes = int64(1 << 30)
	mimeCacheMaxAge   = 30 * 24 * time.Hour
)

type flightCall struct {
	done chan struct{}
	val  any
	err  error
}

type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

func (g *flightGroup) Do(key string, fn func() (any, error)) (any, error) {
	return g.DoContext(context.Background(), key, fn)
}

func (g *flightGroup) DoContext(ctx context.Context, key string, fn func() (any, error)) (any, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*flightCall)
	}
	if call := g.calls[key]; call != nil {
		g.mu.Unlock()
		select {
		case <-call.done:
			return call.val, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &flightCall{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	func() {
		defer func() {
			g.mu.Lock()
			delete(g.calls, key)
			close(call.done)
			g.mu.Unlock()
		}()
		call.val, call.err = fn()
	}()
	return call.val, call.err
}

func cachePathComponent(kind, value string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func messageCacheDir(folderID, serverID string) string {
	return filepath.Join(
		mimeCacheDir(),
		mimeCacheVersion,
		cachePathComponent("folder", folderID),
		cachePathComponent("message", serverID),
	)
}

func messageMetadataPath(folderID, serverID string) string {
	return filepath.Join(messageCacheDir(folderID, serverID), "metadata.json")
}

func messageRawMIMEPath(folderID, serverID string) string {
	return filepath.Join(messageCacheDir(folderID, serverID), "raw.eml")
}

func messageFullMIMEPath(folderID, serverID string) string {
	return filepath.Join(messageCacheDir(folderID, serverID), "full.eml")
}

func attachmentCachePath(folderID, serverID, fileReference string) string {
	return filepath.Join(
		messageCacheDir(folderID, serverID),
		"attachments",
		cachePathComponent("attachment", fileReference)+".bin",
	)
}

func readCacheFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return data, nil
}

func atomicWriteFile(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func removeMessageCache(folderID, serverID string) error {
	err := os.RemoveAll(messageCacheDir(folderID, serverID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type cacheDirectory struct {
	path     string
	size     int64
	modified time.Time
}

func pruneMIMECache(now time.Time) error {
	cacheRoot := mimeCacheDir()
	versions, err := os.ReadDir(cacheRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, version := range versions {
		if version.Name() == mimeCacheVersion {
			continue
		}
		// Cache formats are intentionally versioned. Older layouts cannot be
		// trusted to represent the current MIME tree and are fully recoverable.
		_ = os.RemoveAll(filepath.Join(cacheRoot, version.Name()))
	}

	root := filepath.Join(cacheRoot, mimeCacheVersion)
	folders, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var dirs []cacheDirectory
	var total int64
	for _, folder := range folders {
		if !folder.IsDir() {
			continue
		}
		messages, err := os.ReadDir(filepath.Join(root, folder.Name()))
		if err != nil {
			continue
		}
		for _, message := range messages {
			if !message.IsDir() {
				continue
			}
			dir := cacheDirectory{path: filepath.Join(root, folder.Name(), message.Name())}
			_ = filepath.WalkDir(dir.path, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil || entry.IsDir() {
					return nil
				}
				info, infoErr := entry.Info()
				if infoErr != nil {
					return nil
				}
				dir.size += info.Size()
				if info.ModTime().After(dir.modified) {
					dir.modified = info.ModTime()
				}
				return nil
			})
			if dir.modified.IsZero() {
				dir.modified = now
			}
			dirs = append(dirs, dir)
			total += dir.size
		}
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].modified.Before(dirs[j].modified)
	})
	for _, dir := range dirs {
		expired := now.Sub(dir.modified) > mimeCacheMaxAge
		oversized := total > mimeCacheMaxBytes
		if !expired && !oversized {
			continue
		}
		if err := os.RemoveAll(dir.path); err == nil {
			total -= dir.size
		}
	}
	return nil
}
