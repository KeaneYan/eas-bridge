package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
)

func TestMessagePlanMetadataDoesNotDownloadKnownAttachments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var attachmentCalls int
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
				attachmentCalls++
				return &eas.FetchAttachmentResult{Data: []byte("unexpected")}, nil
			},
		},
	}}
	plan := newMessagePlan("folder", "message", eas.EmailItem{
		ServerID: "message",
		BodyType: eas.BodyTypeHTML,
		Body:     "<p>message</p>",
		Attachments: []eas.Attachment{{
			FileReference:     "large",
			DisplayName:       "large.bin",
			EstimatedDataSize: 128 << 20,
		}},
	}, "message")

	size, err := plan.estimatedRFC822Size(context.Background(), engine)
	if err != nil {
		t.Fatal(err)
	}
	if size <= 128<<20 {
		t.Fatalf("estimated RFC822 size = %d, want MIME overhead included", size)
	}
	structure, err := plan.bodyStructure(context.Background(), engine)
	if err != nil {
		t.Fatal(err)
	}
	if structure == nil {
		t.Fatal("nil BODYSTRUCTURE")
	}
	if attachmentCalls != 0 {
		t.Fatalf("metadata fetch downloaded %d attachments", attachmentCalls)
	}
}

func TestMessagePlanFetchesOnlyRequestedAttachmentPart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var fetched []string
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(_ context.Context, ref string, _, _ int64) (*eas.FetchAttachmentResult, error) {
				mu.Lock()
				fetched = append(fetched, ref)
				mu.Unlock()
				return &eas.FetchAttachmentResult{Data: []byte("data:" + ref)}, nil
			},
		},
	}}
	plan := newMessagePlan("folder", "message", eas.EmailItem{
		ServerID: "message",
		BodyType: eas.BodyTypeHTML,
		Body:     "<p>message</p>",
		Attachments: []eas.Attachment{
			{FileReference: "inline", DisplayName: "inline.png", ContentID: "inline", IsInline: true, EstimatedDataSize: 11},
			{FileReference: "regular", DisplayName: "regular.pdf", EstimatedDataSize: 12},
		},
	}, "message")

	// mixed/related structure: part 2 is the regular attachment.
	body, err := plan.bodySection(context.Background(), engine, &imap.FetchItemBodySection{Part: []int{2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("empty attachment body")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := fmt.Sprint(fetched); got != "[regular]" {
		t.Fatalf("fetched attachments = %s, want [regular]", got)
	}
}

func TestDownloadAttachmentUsesRangesForLargeFiles(t *testing.T) {
	size := attachmentChunkSize + 1
	// 服务器对每个分块独立 base64 编码：Range 作用于原始字节，Data 是该
	// 字节区间的 base64 文本（中间分块也各自带 padding——4MB 不被 3 整除）。
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i * 31)
	}
	var ranges [][2]int64
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(_ context.Context, _ string, start, end int64) (*eas.FetchAttachmentResult, error) {
				ranges = append(ranges, [2]int64{start, end})
				return &eas.FetchAttachmentResult{
					Data:  []byte(base64.StdEncoding.EncodeToString(content[start : end+1])),
					Range: fmt.Sprintf("%d-%d", start, end),
				}, nil
			},
		},
	}}

	got, err := engine.downloadAttachment(context.Background(), eas.Attachment{
		FileReference:     "large",
		EstimatedDataSize: size,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded content mismatch: got %d bytes, want %d", len(got), size)
	}
	if len(ranges) != 2 {
		t.Fatalf("range requests = %v, want 2 chunks", ranges)
	}
	// 偏移必须按解码后的原始字节推进（按 base64 文本推进会跳内容）
	if ranges[1][0] != attachmentChunkSize {
		t.Fatalf("second chunk start = %d, want %d", ranges[1][0], attachmentChunkSize)
	}
}

// 服务器忽略 Range 直接返回完整附件（Range 为空）时也要解码。
func TestDownloadAttachmentRangeIgnoredDecodesWhole(t *testing.T) {
	payload := bytes.Repeat([]byte{1, 2, 3, 4, 5}, 100)
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
				return &eas.FetchAttachmentResult{
					Data: []byte(base64.StdEncoding.EncodeToString(payload)),
				}, nil
			},
		},
	}}
	got, err := engine.downloadAttachment(context.Background(), eas.Attachment{
		FileReference:     "large",
		EstimatedDataSize: attachmentChunkSize + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d bytes, want %d", len(got), len(payload))
	}
}

func TestFailedBodyFetchIsNotCachedAndRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st, err := loadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Folders = []eas.Folder{{ServerID: "inbox", Type: eas.FolderTypeInbox}}
	st.Items["inbox"] = []eas.EmailItem{{
		ServerID: "message",
		BodyType: eas.BodyTypePlain,
		Body:     "synchronized summary",
	}}

	htmlCalls := 0
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				FetchEmailFunc: func(_ context.Context, _, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
					switch opts.BodyType {
					case eas.BodyTypeMIME:
						return &eas.EmailItem{ServerID: serverID}, nil
					case eas.BodyTypeHTML:
						htmlCalls++
						if htmlCalls == 1 {
							return nil, errors.New("temporary HTML failure")
						}
						return &eas.EmailItem{
							ServerID: serverID,
							BodyType: eas.BodyTypeHTML,
							Body:     "<p>complete HTML</p>",
						}, nil
					case eas.BodyTypePlain:
						return &eas.EmailItem{
							ServerID: serverID,
							BodyType: eas.BodyTypePlain,
							Body:     "complete plain text",
						}, nil
					default:
						return nil, nil
					}
				},
			},
		},
	}

	first, err := engine.prepareMessage(context.Background(), "inbox", "message")
	if err != nil {
		t.Fatal(err)
	}
	if first.cacheable {
		t.Fatal("failed body fetch produced a cacheable plan")
	}
	if _, err := os.Stat(messageMetadataPath("inbox", "message")); !os.IsNotExist(err) {
		t.Fatalf("incomplete metadata cache exists: %v", err)
	}

	second, err := engine.prepareMessage(context.Background(), "inbox", "message")
	if err != nil {
		t.Fatal(err)
	}
	if !second.cacheable || second.item.BodyType != eas.BodyTypeHTML || second.item.Body != "<p>complete HTML</p>" {
		t.Fatalf("retry did not fetch complete HTML: %+v", second)
	}
	if htmlCalls != 2 {
		t.Fatalf("HTML fetch calls = %d, want 2", htmlCalls)
	}
	if _, err := os.Stat(messageMetadataPath("inbox", "message")); err != nil {
		t.Fatalf("complete metadata was not cached: %v", err)
	}
}

// fetchAttachmentCached 端到端：FetchAttachment 返回的 base64 原文必须解码后
// 返回并落缓存；第二次调用走缓存不再打服务器；缓存文件内容是原始字节。
// （2026-07-25 图片事故的生产路径，此前零直接覆盖——ZCode 两轮点名）
func TestFetchAttachmentCachedDecodesAndCaches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := append([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{7, 8, 9}, 100)...)
	meta := eas.Attachment{
		FileReference:     "ref-1",
		DisplayName:       "logo.png",
		EstimatedDataSize: int64(len(payload)),
	}
	calls := 0
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
				calls++
				return &eas.FetchAttachmentResult{
					Data: []byte(base64.StdEncoding.EncodeToString(payload)),
				}, nil
			},
		},
	}}

	got, err := engine.fetchAttachmentCached(context.Background(), "folder", "message", meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("第一次调用应返回解码后原始字节，got %d bytes head %x", len(got), got[:min(8, len(got))])
	}

	got2, err := engine.fetchAttachmentCached(context.Background(), "folder", "message", meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got2, payload) {
		t.Fatal("缓存命中返回内容不一致")
	}
	if calls != 1 {
		t.Fatalf("第二次调用应走缓存，FetchAttachment 被调 %d 次", calls)
	}

	// 缓存文件里存的必须是解码后的原始字节（不是 base64 文本）
	cached, err := os.ReadFile(attachmentCachePath("folder", "message", "ref-1"))
	if err != nil {
		t.Fatalf("缓存文件不存在: %v", err)
	}
	if !bytes.Equal(cached, payload) {
		t.Fatalf("缓存文件内容 = %d bytes head %x，应为原始字节", len(cached), cached[:min(8, len(cached))])
	}
}

// 下载失败不写缓存，下次调用重试（服务器抖动不固化失败）。
func TestFetchAttachmentCachedErrorNotCached(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := bytes.Repeat([]byte{1, 2, 3, 4}, 50)
	meta := eas.Attachment{FileReference: "ref-err", EstimatedDataSize: int64(len(payload))}
	calls := 0
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
				calls++
				if calls == 1 {
					return nil, errors.New("server wobble")
				}
				return &eas.FetchAttachmentResult{
					Data: []byte(base64.StdEncoding.EncodeToString(payload)),
				}, nil
			},
		},
	}}
	if _, err := engine.fetchAttachmentCached(context.Background(), "folder", "message", meta); err == nil {
		t.Fatal("第一次应返回错误")
	}
	if _, err := os.Stat(attachmentCachePath("folder", "message", "ref-err")); !os.IsNotExist(err) {
		t.Fatalf("失败后不应落缓存文件: %v", err)
	}
	got, err := engine.fetchAttachmentCached(context.Background(), "folder", "message", meta)
	if err != nil {
		t.Fatalf("第二次应重试成功: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("重试后内容不符")
	}
	if calls != 2 {
		t.Fatalf("FetchAttachment 调用 %d 次，want 2", calls)
	}
}

func TestFetchAttachmentNoDataUsesBackoffAndRecovers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := []byte("attachment recovered")
	meta := eas.Attachment{FileReference: "ref-no-data", EstimatedDataSize: int64(len(payload))}
	calls := 0
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
				calls++
				if calls == 1 {
					return nil, eas.ErrAttachmentDataMissing
				}
				return &eas.FetchAttachmentResult{
					Data: []byte(base64.StdEncoding.EncodeToString(payload)),
				}, nil
			},
		},
	}}

	if _, err := engine.fetchAttachmentCached(context.Background(), "folder", "message", meta); err == nil {
		t.Fatal("first fetch should report missing attachment data")
	}
	_, err := engine.fetchAttachmentCached(context.Background(), "folder", "message", meta)
	var backoffErr *attachmentBackoffError
	if !errors.As(err, &backoffErr) {
		t.Fatalf("second fetch error = %v, want attachmentBackoffError", err)
	}
	if calls != 1 {
		t.Fatalf("backoff should suppress remote fetch; calls = %d, want 1", calls)
	}

	key := "attachment:folder\x00message\x00ref-no-data"
	engine.attachmentBackoffMu.Lock()
	state := engine.attachmentBackoff[key]
	state.nextRetry = time.Now().Add(-time.Second)
	engine.attachmentBackoff[key] = state
	engine.attachmentBackoffMu.Unlock()

	got, err := engine.fetchAttachmentCached(context.Background(), "folder", "message", meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("recovered payload = %q, want %q", got, payload)
	}
	if calls != 2 {
		t.Fatalf("expired backoff did not retry remote fetch; calls = %d, want 2", calls)
	}
	if err := engine.attachmentBackoffError(key); err != nil {
		t.Fatalf("successful retry did not clear backoff: %v", err)
	}
}

func TestUnknownSizeAttachmentBackoffDoesNotBreakMetadataFetch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
				return nil, eas.ErrAttachmentDataMissing
			},
		},
	}}
	plan := newMessagePlan("folder", "message", eas.EmailItem{
		ServerID: "message",
		BodyType: eas.BodyTypePlain,
		Body:     "message",
		Attachments: []eas.Attachment{{
			FileReference: "unknown-size",
			DisplayName:   "unknown.bin",
		}},
	}, "message")

	structure, err := plan.bodyStructure(context.Background(), engine)
	if err != nil {
		t.Fatalf("BODYSTRUCTURE should survive unavailable unknown-size attachment: %v", err)
	}
	if structure == nil {
		t.Fatal("nil BODYSTRUCTURE")
	}
	if _, err := plan.estimatedRFC822Size(context.Background(), engine); err != nil {
		t.Fatalf("RFC822.SIZE should survive unavailable unknown-size attachment: %v", err)
	}
}

func TestAttachmentMissingRefreshesFileReferenceAndRetries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := mustLoadTestState(t)
	oldMeta := eas.Attachment{
		FileReference:     "old-ref",
		DisplayName:       "report.pdf",
		ContentType:       "application/pdf",
		EstimatedDataSize: 7,
	}
	newMeta := oldMeta
	newMeta.FileReference = "new-ref"
	st.Folders = []eas.Folder{{ServerID: "inbox", Type: eas.FolderTypeInbox}}
	st.Items["inbox"] = []eas.EmailItem{{
		ServerID:       "message",
		BodyType:       eas.BodyTypePlain,
		Body:           "summary",
		HasAttachments: true,
		Attachments:    []eas.Attachment{oldMeta},
	}}
	payload := []byte("PDFDATA")
	var refs []string
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				FetchEmailFunc: func(_ context.Context, _, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
					switch opts.BodyType {
					case eas.BodyTypeMIME:
						return &eas.EmailItem{ServerID: serverID}, nil
					case eas.BodyTypeHTML:
						return &eas.EmailItem{
							ServerID:       serverID,
							BodyType:       eas.BodyTypeHTML,
							Body:           "<p>complete</p>",
							HasAttachments: true,
							Attachments:    []eas.Attachment{newMeta},
						}, nil
					case eas.BodyTypePlain:
						return &eas.EmailItem{ServerID: serverID, BodyType: eas.BodyTypePlain, Body: "complete"}, nil
					default:
						return nil, nil
					}
				},
			},
			FolderClient: easmock.FolderClient{
				FetchAttachmentFunc: func(_ context.Context, ref string, _, _ int64) (*eas.FetchAttachmentResult, error) {
					refs = append(refs, ref)
					if ref == oldMeta.FileReference {
						return nil, eas.ErrAttachmentDataMissing
					}
					return &eas.FetchAttachmentResult{
						Data: []byte(base64.StdEncoding.EncodeToString(payload)),
					}, nil
				},
			},
		},
	}

	got, err := engine.fetchAttachmentCached(context.Background(), "inbox", "message", oldMeta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
	if fmt.Sprint(refs) != "[old-ref new-ref]" {
		t.Fatalf("attachment references = %v, want [old-ref new-ref]", refs)
	}
	data, err := os.ReadFile(messageMetadataPath("inbox", "message"))
	if err != nil {
		t.Fatal(err)
	}
	var cached cachedMessageMetadata
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatal(err)
	}
	if got := cached.Item.Attachments[0].FileReference; got != "new-ref" {
		t.Fatalf("cached refreshed FileReference = %q, want new-ref", got)
	}
	st.mu.Lock()
	inMemoryReference := st.Items["inbox"][0].Attachments[0].FileReference
	st.mu.Unlock()
	if inMemoryReference != "new-ref" {
		t.Fatalf("in-memory refreshed FileReference = %q, want new-ref", inMemoryReference)
	}
	refreshedData, err := os.ReadFile(attachmentCachePath("inbox", "message", "new-ref"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(refreshedData, payload) {
		t.Fatalf("refreshed attachment cache = %q, want %q", refreshedData, payload)
	}
}

func TestAttachmentRefreshRepeatsAfterBackoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := mustLoadTestState(t)
	oldMeta := eas.Attachment{
		FileReference:     "old-ref",
		DisplayName:       "report.pdf",
		ContentType:       "application/pdf",
		EstimatedDataSize: 7,
	}
	midMeta := oldMeta
	midMeta.FileReference = "mid-ref"
	newMeta := oldMeta
	newMeta.FileReference = "new-ref"
	st.Folders = []eas.Folder{{ServerID: "inbox", Type: eas.FolderTypeInbox}}
	st.Items["inbox"] = []eas.EmailItem{{
		ServerID:       "message",
		BodyType:       eas.BodyTypePlain,
		Body:           "summary",
		HasAttachments: true,
		Attachments:    []eas.Attachment{oldMeta},
	}}

	payload := []byte("PDFDATA")
	htmlCalls := 0
	var refs []string
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				FetchEmailFunc: func(_ context.Context, _, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
					switch opts.BodyType {
					case eas.BodyTypeMIME:
						return &eas.EmailItem{ServerID: serverID}, nil
					case eas.BodyTypeHTML:
						htmlCalls++
						meta := midMeta
						if htmlCalls > 1 {
							meta = newMeta
						}
						return &eas.EmailItem{
							ServerID:       serverID,
							BodyType:       eas.BodyTypeHTML,
							Body:           "<p>complete</p>",
							HasAttachments: true,
							Attachments:    []eas.Attachment{meta},
						}, nil
					case eas.BodyTypePlain:
						return &eas.EmailItem{ServerID: serverID, BodyType: eas.BodyTypePlain, Body: "complete"}, nil
					default:
						return nil, nil
					}
				},
			},
			FolderClient: easmock.FolderClient{
				FetchAttachmentFunc: func(_ context.Context, ref string, _, _ int64) (*eas.FetchAttachmentResult, error) {
					refs = append(refs, ref)
					if ref != newMeta.FileReference {
						return nil, eas.ErrAttachmentDataMissing
					}
					return &eas.FetchAttachmentResult{
						Data: []byte(base64.StdEncoding.EncodeToString(payload)),
					}, nil
				},
			},
		},
	}

	if _, err := engine.fetchAttachmentCached(context.Background(), "inbox", "message", oldMeta); !isAttachmentNoDataError(err) {
		t.Fatalf("first refresh error = %v, want missing attachment data", err)
	}
	key := "attachment:inbox\x00message\x00old-ref"
	engine.attachmentBackoffMu.Lock()
	backoff := engine.attachmentBackoff[key]
	if backoff.refreshed {
		engine.attachmentBackoffMu.Unlock()
		t.Fatal("backoff step did not re-enable metadata refresh")
	}
	backoff.nextRetry = time.Now().Add(-time.Second)
	engine.attachmentBackoff[key] = backoff
	engine.attachmentBackoffMu.Unlock()

	got, err := engine.fetchAttachmentCached(context.Background(), "inbox", "message", oldMeta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
	if gotRefs := fmt.Sprint(refs); gotRefs != "[old-ref mid-ref old-ref new-ref]" {
		t.Fatalf("attachment references = %s, want two metadata refresh rounds", gotRefs)
	}
	st.mu.Lock()
	inMemoryReference := st.Items["inbox"][0].Attachments[0].FileReference
	st.mu.Unlock()
	if inMemoryReference != "new-ref" {
		t.Fatalf("in-memory FileReference = %q, want new-ref", inMemoryReference)
	}
}

func TestAttachmentBackoffCapsAtOneDay(t *testing.T) {
	engine := &syncEngine{}
	for i := 0; i < 20; i++ {
		engine.trackAttachmentFailure("attachment:key", eas.ErrAttachmentDataMissing)
		engine.attachmentBackoffMu.Lock()
		state := engine.attachmentBackoff["attachment:key"]
		state.nextRetry = time.Now().Add(-time.Second)
		engine.attachmentBackoff["attachment:key"] = state
		engine.attachmentBackoffMu.Unlock()
	}
	engine.attachmentBackoffMu.Lock()
	failures := engine.attachmentBackoff["attachment:key"].failures
	engine.attachmentBackoffMu.Unlock()
	if failures != 20 {
		t.Fatalf("failures = %d, want 20", failures)
	}
	engine.trackAttachmentFailure("attachment:key", eas.ErrAttachmentDataMissing)
	engine.attachmentBackoffMu.Lock()
	remaining := time.Until(engine.attachmentBackoff["attachment:key"].nextRetry)
	engine.attachmentBackoffMu.Unlock()
	if remaining <= 23*time.Hour || remaining > 24*time.Hour {
		t.Fatalf("capped attachment backoff = %v, want (23h, 24h]", remaining)
	}
}

// 端到端：plan.bodySection 取附件部件 → 生产路径 fetchAttachmentCached →
// MIME 里的 base64 解一层必须等于原始字节（双层 base64 事故的全链路回归）。
func TestBodySectionAttachmentEndToEndBase64(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{5, 6}, 200)...) // JPEG magic
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
				return &eas.FetchAttachmentResult{
					Data: []byte(base64.StdEncoding.EncodeToString(payload)),
				}, nil
			},
		},
	}}
	plan := newMessagePlan("folder", "message", eas.EmailItem{
		ServerID: "message",
		BodyType: eas.BodyTypeHTML,
		Body:     `<p>message</p>`,
		Attachments: []eas.Attachment{
			// 两个附件：请求 part 2（regular）时 actual != all，走 render 路径
			// （单附件会走 fetchMIME 全量重建路径，那是另一条链路）
			{FileReference: "inline", DisplayName: "inline.png", ContentID: "inline", IsInline: true, EstimatedDataSize: 11},
			{
				FileReference:     "photo",
				DisplayName:       "photo.jpg",
				ContentType:       "image/jpeg",
				EstimatedDataSize: int64(len(payload)),
			},
		},
	}, "message")

	body, err := plan.bodySection(context.Background(), engine, &imap.FetchItemBodySection{Part: []int{2}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(body)))
	if err != nil {
		t.Fatalf("附件部件不是合法 base64: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("解一层后 head %x，应为 JPEG 原始字节（双层 base64 回归）", decoded[:min(8, len(decoded))])
	}
}

// R1：单附件邮件取附件部件 → bodySection 的 actual==all 分支 → fetchMIME
// 全量重建链路（prepareMessage→fetchMessageSource→render 全附件）。
// 这是生产上"打开只有一封一个附件的邮件"的真实路径，此前零覆盖。
func TestFetchMIMEFullRebuildSingleAttachment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	payload := append([]byte{0x89, 0x50, 0x4E, 0x47}, bytes.Repeat([]byte{9, 9, 9}, 100)...)
	att := eas.Attachment{
		FileReference:     "only",
		DisplayName:       "only.png",
		ContentType:       "image/png",
		EstimatedDataSize: int64(len(payload)),
	}
	st := mustLoadTestState(t)
	st.Folders = []eas.Folder{{ServerID: "folder", Type: eas.FolderTypeInbox}}
	st.Items["folder"] = []eas.EmailItem{{
		ServerID:       "message",
		Subject:        "单附件邮件",
		HasAttachments: true,
		Attachments:    []eas.Attachment{att},
	}}
	engine := &syncEngine{
		st: st,
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				FetchEmailFunc: func(_ context.Context, _, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
					switch opts.BodyType {
					case eas.BodyTypeMIME:
						return &eas.EmailItem{ServerID: serverID}, nil // 无原始 MIME → 重建路径
					case eas.BodyTypeHTML:
						return &eas.EmailItem{
							ServerID:       serverID,
							Subject:        "单附件邮件",
							From:           "sender@example.com",
							To:             "receiver@example.com",
							BodyType:       eas.BodyTypeHTML,
							Body:           `<p>hi</p>`,
							HasAttachments: true,
							Attachments:    []eas.Attachment{att},
						}, nil
					default:
						return &eas.EmailItem{ServerID: serverID, BodyType: eas.BodyTypePlain, Body: "hi"}, nil
					}
				},
			},
			FolderClient: easmock.FolderClient{
				FetchAttachmentFunc: func(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
					return &eas.FetchAttachmentResult{
						Data: []byte(base64.StdEncoding.EncodeToString(payload)),
					}, nil
				},
			},
		},
	}

	// 生产入口：prepareMessage 拿 plan → bodySection 取附件部件（actual==all → fetchMIME）
	plan, err := engine.prepareMessage(context.Background(), "folder", "message")
	if err != nil {
		t.Fatal(err)
	}
	body, err := plan.bodySection(context.Background(), engine, &imap.FetchItemBodySection{Part: []int{2}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(body)))
	if err != nil {
		t.Fatalf("附件部件不是合法 base64: %v", err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("全量重建链路解一层后 head %x，应为原始字节", decoded[:min(8, len(decoded))])
	}
	// full.eml 应已落缓存且合法
	full, err := os.ReadFile(messageFullMIMEPath("folder", "message"))
	if err != nil {
		t.Fatalf("full.eml 未落缓存: %v", err)
	}
	if !validRFC822(full) {
		t.Fatal("full.eml 不是合法 RFC822")
	}
}
