package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/hstern/go-activesync/eas"
)

const attachmentChunkSize = int64(4 << 20)

type attachmentBackoff struct {
	failures  int
	nextRetry time.Time
	cause     error
	refreshed bool
}

type attachmentBackoffError struct {
	retryAfter time.Duration
	cause      error
}

func (e *attachmentBackoffError) Error() string {
	return fmt.Sprintf("附件暂不可用，约 %s 后重试: %v", e.retryAfter.Round(time.Second), e.cause)
}

func (e *attachmentBackoffError) Unwrap() error {
	return e.cause
}

var attachmentBackoffSteps = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
}

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
				data, err := engine.fetchAttachmentCached(ctx, plan.folderID, plan.serverID, plan.item.Attachments[index])
				if isAttachmentNoDataError(err) {
					// BODYSTRUCTURE/RFC822.SIZE describe message shape. One
					// temporarily unavailable unknown-size attachment must not
					// make the whole metadata FETCH fail.
					return nil, nil
				}
				return data, err
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
			e.clearAttachmentBackoff(key)
			return data, nil
		}
		if backoffErr := e.attachmentBackoffError(key); backoffErr != nil {
			return nil, backoffErr
		}
		data, fetchErr := e.downloadAttachment(ctx, meta)
		if fetchErr != nil {
			if isAttachmentNoDataError(fetchErr) && e.markAttachmentMetadataRefreshed(key) {
				refreshed, changed, refreshErr := e.refreshAttachmentMetadata(ctx, folderID, serverID, meta)
				if refreshErr != nil {
					log.Printf("[mime] 刷新附件元数据失败，继续按原引用退避: %v", refreshErr)
				} else if changed {
					log.Printf("[mime] 附件 FileReference 已刷新，立即重试下载")
					data, fetchErr = e.downloadAttachment(ctx, refreshed)
					if fetchErr == nil {
						e.clearAttachmentBackoff(key)
						if writeErr := atomicWriteFile(path, data, 0600); writeErr != nil {
							log.Printf("[mime] 写附件缓存失败: %v", writeErr)
						}
						refreshedPath := attachmentCachePath(folderID, serverID, refreshed.FileReference)
						if refreshedPath != path {
							if writeErr := atomicWriteFile(refreshedPath, data, 0600); writeErr != nil {
								log.Printf("[mime] 写刷新引用的附件缓存失败: %v", writeErr)
							}
						}
						return data, nil
					}
				}
			}
			e.trackAttachmentFailure(key, fetchErr)
			return nil, fetchErr
		}
		e.clearAttachmentBackoff(key)
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

func isAttachmentNoDataError(err error) bool {
	return errors.Is(err, eas.ErrAttachmentDataMissing)
}

func (e *syncEngine) markAttachmentMetadataRefreshed(key string) bool {
	e.attachmentBackoffMu.Lock()
	defer e.attachmentBackoffMu.Unlock()
	state := e.attachmentBackoff[key]
	if state.refreshed {
		return false
	}
	state.refreshed = true
	if e.attachmentBackoff == nil {
		e.attachmentBackoff = map[string]attachmentBackoff{}
	}
	e.attachmentBackoff[key] = state
	return true
}

func (e *syncEngine) refreshAttachmentMetadata(
	ctx context.Context,
	folderID, serverID string,
	previous eas.Attachment,
) (eas.Attachment, bool, error) {
	if e.st == nil {
		return eas.Attachment{}, false, errors.New("同步状态不可用")
	}
	summary, ok := e.cachedEmailItem(folderID, serverID)
	if !ok {
		return eas.Attachment{}, false, fmt.Errorf("邮件不存在: %s", serverID)
	}
	folder, ok := e.findFolder(folderID)
	if !ok {
		return eas.Attachment{}, false, fmt.Errorf("不存在的文件夹: %s", folderID)
	}
	source, err := fetchMessageSource(ctx, e.c, folder.ServerID, serverID, summary)
	if err != nil {
		return eas.Attachment{}, false, err
	}
	if len(source.RawMIME) > 0 {
		if err := atomicWriteFile(messageRawMIMEPath(folderID, serverID), source.RawMIME, 0600); err != nil {
			log.Printf("[mime] 写刷新后的原始 MIME 缓存失败: %v", err)
		}
	}
	// The caller may still hold a plan built with an older FileReference while
	// the in-memory summary already contains the successor found by an earlier
	// refresh. Match against that current successor so another server-side
	// rotation can be discovered after the next backoff step.
	matchBase := previous
	summaryHasPrevious := false
	for _, attachment := range summary.Attachments {
		if attachment.FileReference == previous.FileReference {
			summaryHasPrevious = true
			break
		}
	}
	if !summaryHasPrevious {
		if current, ok := findRefreshedAttachment(previous, summary.Attachments); ok {
			matchBase = current
		}
	}
	refreshed, matched := findRefreshedAttachment(matchBase, source.Item.Attachments)
	changed := matched && refreshed.FileReference != "" && refreshed.FileReference != matchBase.FileReference
	if changed {
		attachments := make([]eas.Attachment, 0, len(source.Item.Attachments))
		for _, attachment := range source.Item.Attachments {
			if attachment.FileReference != previous.FileReference &&
				attachment.FileReference != matchBase.FileReference {
				attachments = append(attachments, attachment)
			}
		}
		source.Item.Attachments = attachments
		e.updateCachedAttachmentMetadata(folderID, serverID, matchBase, refreshed)
	}
	if source.Complete && len(source.RawMIME) == 0 {
		metadata, err := json.Marshal(cachedMessageMetadata{Item: source.Item, PlainBody: source.PlainBody})
		if err != nil {
			return eas.Attachment{}, false, err
		}
		if err := atomicWriteFile(messageMetadataPath(folderID, serverID), metadata, 0600); err != nil {
			log.Printf("[mime] 写刷新后的邮件元数据缓存失败: %v", err)
		}
	}
	if !changed {
		return eas.Attachment{}, false, nil
	}
	return refreshed, true, nil
}

func (e *syncEngine) updateCachedAttachmentMetadata(
	folderID, serverID string,
	previous, refreshed eas.Attachment,
) {
	e.st.mu.Lock()
	defer e.st.mu.Unlock()
	items := e.st.Items[folderID]
	for itemIndex := range items {
		if items[itemIndex].ServerID != serverID {
			continue
		}
		for attachmentIndex := range items[itemIndex].Attachments {
			if items[itemIndex].Attachments[attachmentIndex].FileReference != previous.FileReference {
				continue
			}
			items[itemIndex].Attachments[attachmentIndex] = mergeAttachmentMetadata(
				items[itemIndex].Attachments[attachmentIndex],
				refreshed,
			)
			return
		}
	}
}

func findRefreshedAttachment(previous eas.Attachment, candidates []eas.Attachment) (eas.Attachment, bool) {
	bestScore := 0
	var best eas.Attachment
	ambiguous := false
	for _, candidate := range candidates {
		if candidate.FileReference == previous.FileReference {
			continue
		}
		score := 0
		switch {
		case previous.ContentID != "" &&
			strings.EqualFold(normalizeContentID(previous.ContentID), normalizeContentID(candidate.ContentID)):
			score = 4
		case previous.DisplayName != "" &&
			previous.DisplayName == candidate.DisplayName &&
			previous.EstimatedDataSize > 0 &&
			previous.EstimatedDataSize == candidate.EstimatedDataSize:
			score = 3
		case previous.DisplayName != "" &&
			previous.DisplayName == candidate.DisplayName &&
			previous.ContentType != "" &&
			strings.EqualFold(previous.ContentType, candidate.ContentType):
			score = 2
		}
		if score > bestScore {
			bestScore = score
			best = candidate
			ambiguous = false
		} else if score > 0 && score == bestScore {
			ambiguous = true
		}
	}
	return best, bestScore > 0 && !ambiguous
}

func (e *syncEngine) trackAttachmentFailure(key string, err error) {
	if !isAttachmentNoDataError(err) {
		return
	}
	e.attachmentBackoffMu.Lock()
	defer e.attachmentBackoffMu.Unlock()
	state := e.attachmentBackoff[key]
	state.failures++
	step := attachmentBackoffSteps[min(state.failures, len(attachmentBackoffSteps))-1]
	state.nextRetry = time.Now().Add(step)
	state.cause = err
	// One metadata refresh is allowed per remote attempt. Reset after entering
	// a backoff step so a second FileReference rotation can be discovered when
	// the next retry becomes eligible.
	state.refreshed = false
	if e.attachmentBackoff == nil {
		e.attachmentBackoff = map[string]attachmentBackoff{}
	}
	e.attachmentBackoff[key] = state
	log.Printf("[mime] 附件服务器未返回数据，连续第 %d 次，退避 %s 后再请求", state.failures, step)
}

func (e *syncEngine) attachmentBackoffError(key string) error {
	e.attachmentBackoffMu.Lock()
	defer e.attachmentBackoffMu.Unlock()
	state, ok := e.attachmentBackoff[key]
	if !ok {
		return nil
	}
	remaining := time.Until(state.nextRetry)
	if remaining <= 0 {
		return nil
	}
	return &attachmentBackoffError{retryAfter: remaining, cause: state.cause}
}

func (e *syncEngine) clearAttachmentBackoff(key string) {
	e.attachmentBackoffMu.Lock()
	delete(e.attachmentBackoff, key)
	e.attachmentBackoffMu.Unlock()
}

func (e *syncEngine) clearAttachmentBackoffsForMessage(folderID, serverID string) {
	prefix := "attachment:" + folderID + "\x00" + serverID + "\x00"
	e.attachmentBackoffMu.Lock()
	for key := range e.attachmentBackoff {
		if strings.HasPrefix(key, prefix) {
			delete(e.attachmentBackoff, key)
		}
	}
	e.attachmentBackoffMu.Unlock()
}

// downloadAttachment 下载附件并返回解码后的原始字节。
// fork 库保持上游语义：FetchAttachment 的 Data 是未解码的 base64 原文，
// 解码统一在本函数内完成（缓存与下游拿到的都是原始字节）。
func (e *syncEngine) downloadAttachment(ctx context.Context, meta eas.Attachment) ([]byte, error) {
	if meta.EstimatedDataSize <= attachmentChunkSize {
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
		chunk, _, err := decodeBase64Chunk(result.Data)
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
		e.clearAttachmentBackoffsForMessage(folderID, serverID)
		if err := removeMessageCache(folderID, serverID); err != nil {
			log.Printf("[mime] 清理邮件缓存失败: %v", err)
		}
	}
}

// scheduleCachePrune 启动时立即修剪一次，之后每 24h 定期修剪。
// ctx 取消后协程退出，避免服务关闭后继续访问缓存与 state。
func (e *syncEngine) scheduleCachePrune(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		prune := func() {
			_, err := e.flights.Do("cache-prune", func() (any, error) {
				return nil, pruneMIMECache(time.Now())
			})
			if err != nil {
				log.Printf("[mime] 清理缓存失败: %v", err)
			}
			e.st.compactEventLogIfNeeded()
		}
		prune()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prune()
			}
		}
	}()
	return done
}
