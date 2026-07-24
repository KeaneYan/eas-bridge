package main

import (
	"context"
	"fmt"
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
	var ranges [][2]int64
	engine := &syncEngine{c: &easmock.Client{
		FolderClient: easmock.FolderClient{
			FetchAttachmentFunc: func(_ context.Context, _ string, start, end int64) (*eas.FetchAttachmentResult, error) {
				ranges = append(ranges, [2]int64{start, end})
				return &eas.FetchAttachmentResult{
					Data:  make([]byte, end-start+1),
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
	if int64(len(got)) != size {
		t.Fatalf("downloaded size = %d, want %d", len(got), size)
	}
	if len(ranges) != 3 {
		t.Fatalf("range requests = %v, want 3 chunks", ranges)
	}
}
