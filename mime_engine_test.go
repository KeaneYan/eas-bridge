package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

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
	size := 2*attachmentChunkSize + 1
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
	if len(ranges) != 3 {
		t.Fatalf("range requests = %v, want 3 chunks", ranges)
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
		EstimatedDataSize: 2*attachmentChunkSize + 1,
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
