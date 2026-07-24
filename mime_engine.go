package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/hstern/go-activesync/eas"
)

const attachmentChunkSize = int64(4 << 20)

type cachedMessageMetadata struct {
	Item      eas.EmailItem `json:"item"`
	PlainBody string        `json:"plain_body,omitempty"`
}

type messagePlan struct {
	folderID  string
	serverID  string
	item      eas.EmailItem
	root      mimeEntity
	rawMIME   []byte
	cacheable bool

	skeletonOnce sync.Once
	skeleton     []byte
	skeletonErr  error
}

func (e *syncEngine) prepareMessage(ctx context.Context, folderID, serverID string) (*messagePlan, error) {
	key := "message:" + folderID + "\x00" + serverID
	value, err := e.flights.DoContext(ctx, key, func() (any, error) {
		return e.loadOrFetchMessagePlan(ctx, folderID, serverID)
	})
	if err != nil {
		return nil, err
	}
	return value.(*messagePlan), nil
}

func (e *syncEngine) loadOrFetchMessagePlan(ctx context.Context, folderID, serverID string) (*messagePlan, error) {
	for _, path := range []string{
		messageFullMIMEPath(folderID, serverID),
		messageRawMIMEPath(folderID, serverID),
	} {
		if data, err := readCacheFile(path); err == nil && validRFC822(data) {
			return &messagePlan{
				folderID:  folderID,
				serverID:  serverID,
				rawMIME:   data,
				cacheable: true,
			}, nil
		}
	}

	if data, err := readCacheFile(messageMetadataPath(folderID, serverID)); err == nil {
		var cached cachedMessageMetadata
		if json.Unmarshal(data, &cached) == nil {
			plan := newMessagePlan(folderID, serverID, cached.Item, cached.PlainBody)
			plan.cacheable = true
			return plan, nil
		}
	}

	summary, ok := e.cachedEmailItem(folderID, serverID)
	if !ok {
		return nil, fmt.Errorf("邮件不存在: %s", serverID)
	}
	folder, ok := e.findFolder(folderID)
	if !ok {
		return nil, fmt.Errorf("不存在的文件夹: %s", folderID)
	}
	source, err := fetchMessageSource(ctx, e.c, folder.ServerID, serverID, summary)
	if err != nil {
		return nil, err
	}
	if len(source.RawMIME) > 0 {
		if err := atomicWriteFile(messageRawMIMEPath(folderID, serverID), source.RawMIME, 0600); err != nil {
			log.Printf("[mime] 写原始 MIME 缓存失败: %v", err)
		}
		return &messagePlan{
			folderID:  folderID,
			serverID:  serverID,
			item:      source.Item,
			rawMIME:   source.RawMIME,
			cacheable: true,
		}, nil
	}

	if source.Complete {
		metadata, err := json.Marshal(cachedMessageMetadata{
			Item:      source.Item,
			PlainBody: source.PlainBody,
		})
		if err != nil {
			return nil, err
		}
		if err := atomicWriteFile(messageMetadataPath(folderID, serverID), metadata, 0600); err != nil {
			log.Printf("[mime] 写邮件元数据缓存失败: %v", err)
		}
	}
	plan := newMessagePlan(folderID, serverID, source.Item, source.PlainBody)
	plan.cacheable = source.Complete
	return plan, nil
}

func newMessagePlan(folderID, serverID string, item eas.EmailItem, plainBody string) *messagePlan {
	attachments := make([]mailAttachment, len(item.Attachments))
	for i, meta := range item.Attachments {
		attachments[i] = mailAttachment{
			meta:        meta,
			contentType: meta.ContentType,
			index:       i,
		}
	}
	return &messagePlan{
		folderID: folderID,
		serverID: serverID,
		item:     item,
		root:     buildMIMEEntity(item, plainBody, serverID, attachments),
	}
}

func (e *syncEngine) cachedEmailItem(folderID, serverID string) (eas.EmailItem, bool) {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	for _, item := range e.st.Items[folderID] {
		if item.ServerID == serverID {
			return item, true
		}
	}
	return eas.EmailItem{}, false
}

func (plan *messagePlan) bodyStructure(ctx context.Context, engine *syncEngine) (imap.BodyStructure, error) {
	if len(plan.rawMIME) > 0 {
		return imapserver.ExtractBodyStructure(bytes.NewReader(plan.rawMIME)), nil
	}
	skeleton, err := plan.estimatedMIME(ctx, engine)
	if err != nil {
		return nil, err
	}
	structure := imapserver.ExtractBodyStructure(bytes.NewReader(skeleton))
	if structure == nil {
		return nil, errors.New("无法解析邮件 BODYSTRUCTURE")
	}
	return structure, nil
}

func (plan *messagePlan) estimatedRFC822Size(ctx context.Context, engine *syncEngine) (int64, error) {
	if len(plan.rawMIME) > 0 {
		return int64(len(plan.rawMIME)), nil
	}
	skeleton, err := plan.estimatedMIME(ctx, engine)
	if err != nil {
		return 0, err
	}
	return int64(len(skeleton)), nil
}

func (plan *messagePlan) estimatedMIME(ctx context.Context, engine *syncEngine) ([]byte, error) {
	plan.skeletonOnce.Do(func() {
		unknownSizes := make(map[int]bool)
		for i, attachment := range plan.item.Attachments {
			if attachment.EstimatedDataSize <= 0 {
				unknownSizes[i] = true
			}
		}
		plan.skeleton, plan.skeletonErr = plan.render(mimeRenderOptions{
			actualAttachments: unknownSizes,
			estimatedPayloads: true,
			resolveAttachment: func(index int) ([]byte, error) {
				return engine.fetchAttachmentCached(ctx, plan.folderID, plan.serverID, plan.item.Attachments[index])
			},
		})
	})
	return plan.skeleton, plan.skeletonErr
}

func (plan *messagePlan) bodySection(ctx context.Context, engine *syncEngine, section *imap.FetchItemBodySection) ([]byte, error) {
	if len(plan.rawMIME) > 0 {
		return imapserver.ExtractBodySection(bytes.NewReader(plan.rawMIME), section), nil
	}

	actual := make(map[int]bool)
	if len(section.Part) == 0 {
		if section.Specifier == imap.PartSpecifierNone || section.Specifier == imap.PartSpecifierText {
			actual = allAttachmentIndices(plan.root)
		}
	} else if target := plan.root.entityAtPath(section.Part); target != nil {
		if section.Specifier == imap.PartSpecifierNone || section.Specifier == imap.PartSpecifierText {
			actual = allAttachmentIndices(*target)
		}
	}

	if len(actual) == len(plan.item.Attachments) && len(actual) > 0 {
		full, err := engine.fetchMIME(ctx, plan.folderID, plan.serverID)
		if err != nil {
			return nil, err
		}
		return imapserver.ExtractBodySection(bytes.NewReader(full), section), nil
	}

	rendered, err := plan.render(mimeRenderOptions{
		actualAttachments: actual,
		resolveAttachment: func(index int) ([]byte, error) {
			return engine.fetchAttachmentCached(ctx, plan.folderID, plan.serverID, plan.item.Attachments[index])
		},
	})
	if err != nil {
		return nil, err
	}
	return imapserver.ExtractBodySection(bytes.NewReader(rendered), section), nil
}

func (plan *messagePlan) render(opts mimeRenderOptions) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeRFC822(&buf, plan.item, plan.serverID, plan.root, opts); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (e *syncEngine) fetchMIME(ctx context.Context, folderID, serverID string) ([]byte, error) {
	if data, err := readCacheFile(messageFullMIMEPath(folderID, serverID)); err == nil && validRFC822(data) {
		return data, nil
	}
	plan, err := e.prepareMessage(ctx, folderID, serverID)
	if err != nil {
		return nil, err
	}
	if len(plan.rawMIME) > 0 {
		return plan.rawMIME, nil
	}

	key := "full:" + folderID + "\x00" + serverID
	value, err := e.flights.DoContext(ctx, key, func() (any, error) {
		if data, readErr := readCacheFile(messageFullMIMEPath(folderID, serverID)); readErr == nil && validRFC822(data) {
			return data, nil
		}
		full, renderErr := plan.render(mimeRenderOptions{
			actualAttachments: allAttachmentIndices(plan.root),
			resolveAttachment: func(index int) ([]byte, error) {
				return e.fetchAttachmentCached(ctx, folderID, serverID, plan.item.Attachments[index])
			},
		})
		if renderErr != nil {
			return nil, renderErr
		}
		if plan.cacheable {
			if writeErr := atomicWriteFile(messageFullMIMEPath(folderID, serverID), full, 0600); writeErr != nil {
				log.Printf("[mime] 写完整 MIME 缓存失败: %v", writeErr)
			}
		}
		return full, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]byte), nil
}

func (e *syncEngine) fetchAttachmentCached(ctx context.Context, folderID, serverID string, meta eas.Attachment) ([]byte, error) {
	if meta.FileReference == "" {
		return nil, errors.New("附件缺少 FileReference")
	}
	path := attachmentCachePath(folderID, serverID, meta.FileReference)
	if data, err := readCacheFile(path); err == nil {
		return data, nil
	}

	key := "attachment:" + folderID + "\x00" + serverID + "\x00" + meta.FileReference
	value, err := e.flights.DoContext(ctx, key, func() (any, error) {
		if data, readErr := readCacheFile(path); readErr == nil {
			return data, nil
		}
		data, fetchErr := e.downloadAttachment(ctx, meta)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if writeErr := atomicWriteFile(path, data, 0600); writeErr != nil {
			log.Printf("[mime] 写附件缓存失败: %v", writeErr)
		}
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]byte), nil
}

// downloadAttachment 下载附件并返回解码后的原始字节。
// fork 库保持上游语义：FetchAttachment 的 Data 是未解码的 base64 原文，
// 解码统一在本函数内完成（缓存与下游拿到的都是原始字节）。
func (e *syncEngine) downloadAttachment(ctx context.Context, meta eas.Attachment) ([]byte, error) {
	if meta.EstimatedDataSize <= 2*attachmentChunkSize {
		result, err := e.c.FetchAttachment(ctx, meta.FileReference, 0, 0)
		if err != nil {
			return nil, err
		}
		return decodeAttachmentData(result.Data, meta.EstimatedDataSize), nil
	}

	// 大附件分块：Range 作用于原始字节（MS-ASCMD：附件的 range applies to
	// the file content），但每个分块的 Data 是**独立** base64 编码（各自带
	// padding）。必须逐块解码后拼接原始字节、按解码后长度推进偏移——直接
	// 拼接 base64 文本会在流中间出现 padding 导致整体解码失败，按 base64
	// 文本长度推进偏移则会跳过 ~1/3 内容（2026-07-25 ZCode B-1/H-1）。
	var buf bytes.Buffer
	for start := int64(0); start < meta.EstimatedDataSize; {
		end := min(start+attachmentChunkSize-1, meta.EstimatedDataSize-1)
		result, err := e.c.FetchAttachment(ctx, meta.FileReference, start, end)
		if err != nil {
			return nil, err
		}
		if result.Range == "" {
			// 服务器忽略 Range 直接返回完整附件
			return decodeAttachmentData(result.Data, meta.EstimatedDataSize), nil
		}
		if len(result.Data) == 0 {
			return nil, fmt.Errorf("附件分块 %d-%d 返回空数据", start, end)
		}
		chunk, err := decodeBase64Chunk(result.Data)
		if err != nil {
			return nil, fmt.Errorf("附件分块 %d-%d base64 解码失败: %w", start, end, err)
		}
		buf.Write(chunk)
		start += int64(len(chunk))
	}
	return buf.Bytes(), nil
}

func (e *syncEngine) invalidateMessageCache(folderID string, serverIDs ...string) {
	for _, serverID := range serverIDs {
		if err := removeMessageCache(folderID, serverID); err != nil {
			log.Printf("[mime] 清理邮件缓存失败: %v", err)
		}
	}
}

// scheduleCachePrune 启动时立即修剪一次，之后每 24h 定期修剪，
// 避免长运行 daemon 的 MIME 缓存在重启间隔内无界增长。
func (e *syncEngine) scheduleCachePrune() {
	go func() {
		prune := func() {
			_, err := e.flights.Do("cache-prune", func() (any, error) {
				return nil, pruneMIMECache(time.Now())
			})
			if err != nil {
				log.Printf("[mime] 清理缓存失败: %v", err)
			}
		}
		prune()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			prune()
		}
	}()
}
