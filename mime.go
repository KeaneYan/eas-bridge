package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/hstern/go-activesync/eas"
)

type messageContentClient interface {
	FetchEmail(ctx context.Context, folderID, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error)
	FetchAttachment(ctx context.Context, fileReference string, rangeStart, rangeEnd int64) (*eas.FetchAttachmentResult, error)
}

type messageSource struct {
	Item      eas.EmailItem
	PlainBody string
	RawMIME   []byte
	Complete  bool
}

type mailAttachment struct {
	meta        eas.Attachment
	data        []byte
	contentType string
	index       int
	inline      bool
}

type mimeEntity struct {
	contentType      string
	transferEncoding string
	disposition      string
	contentID        string
	contentLocation  string
	body             []byte
	attachment       *mailAttachment
	boundary         string
	children         []mimeEntity
}

type mimeRenderOptions struct {
	actualAttachments map[int]bool
	estimatedPayloads bool
	resolveAttachment func(index int) ([]byte, error)
}

func fetchMessageSource(ctx context.Context, c messageContentClient, folderID, serverID string, summary eas.EmailItem) (messageSource, error) {
	raw, rawErr := c.FetchEmail(ctx, folderID, serverID, eas.FetchEmailOptions{BodyType: eas.BodyTypeMIME})
	if rawErr == nil && raw != nil && validRFC822(raw.BodyMIME) {
		return messageSource{Item: mergeEmailItem(summary, *raw), RawMIME: raw.BodyMIME, Complete: true}, nil
	}
	if rawErr != nil {
		log.Printf("[mime] 原始 MIME 不可用，改用 HTML 重建: %v", rawErr)
	} else {
		log.Printf("[mime] 服务器返回的原始 MIME 为空或无效，改用 HTML 重建")
	}

	item := summary
	complete := true
	full, htmlErr := c.FetchEmail(ctx, folderID, serverID, eas.FetchEmailOptions{BodyType: eas.BodyTypeHTML})
	htmlFetched := htmlErr == nil && full != nil
	if htmlFetched {
		item = mergeEmailItem(item, *full)
		if full.Body == "" && summary.Body != "" {
			complete = false
			log.Printf("[mime] HTML 正文响应缺少正文，本次结果不缓存")
		}
		if full.BodyTruncated {
			complete = false
			log.Printf("[mime] HTML 正文被服务器截断，本次结果不缓存")
		}
	} else {
		complete = false
		if htmlErr != nil {
			log.Printf("[mime] HTML 正文拉取失败，使用同步缓存: %v", htmlErr)
		} else {
			log.Printf("[mime] HTML 正文响应为空，使用同步缓存")
		}
	}

	var plainBody string
	if item.BodyType == eas.BodyTypeHTML || item.Body == "" || item.BodyTruncated {
		plain, plainErr := c.FetchEmail(ctx, folderID, serverID, eas.FetchEmailOptions{BodyType: eas.BodyTypePlain})
		if plainErr == nil && plain != nil {
			if plain.BodyTruncated {
				complete = false
				log.Printf("[mime] 纯文本正文被服务器截断，本次结果不缓存")
			}
			if plain.BodyType == eas.BodyTypePlain && plain.Body != "" {
				plainBody = plain.Body
			}
			item = mergeEmailMetadata(item, *plain)
			if item.Body == "" && plain.Body != "" {
				item.Body = plain.Body
				item.BodyType = eas.BodyTypePlain
				item.BodyTruncated = plain.BodyTruncated
			}
		} else {
			complete = false
			if plainErr != nil {
				log.Printf("[mime] 完整纯文本正文拉取失败，使用同步缓存: %v", plainErr)
			} else {
				log.Printf("[mime] 纯文本正文响应为空，使用同步缓存")
			}
		}
	}
	attachmentMetadataComplete := !item.HasAttachments || len(item.Attachments) > 0
	return messageSource{
		Item:      item,
		PlainBody: plainBody,
		Complete:  complete && attachmentMetadataComplete,
	}, nil
}

func fetchAndBuildMIME(ctx context.Context, c messageContentClient, folderID, serverID string, summary eas.EmailItem) ([]byte, bool) {
	source, err := fetchMessageSource(ctx, c, folderID, serverID, summary)
	if err != nil {
		return nil, false
	}
	if len(source.RawMIME) > 0 {
		return source.RawMIME, true
	}

	attachments := make([]mailAttachment, 0, len(source.Item.Attachments))
	complete := source.Complete
	for i, meta := range source.Item.Attachments {
		if meta.FileReference == "" {
			complete = false
			log.Printf("[mime] 第 %d 个附件缺少 FileReference，已跳过", i+1)
			continue
		}
		got, fetchErr := c.FetchAttachment(ctx, meta.FileReference, 0, 0)
		if fetchErr != nil {
			complete = false
			log.Printf("[mime] 第 %d 个附件下载失败，将在下次读取时重试: %v", i+1, fetchErr)
			continue
		}
		attachments = append(attachments, mailAttachment{
			meta:        meta,
			data:        decodeAttachmentData(got.Data, meta.EstimatedDataSize),
			contentType: firstNonEmpty(meta.ContentType, got.ContentType),
			index:       i,
		})
	}
	if source.Item.HasAttachments && len(source.Item.Attachments) == 0 {
		complete = false
		log.Printf("[mime] 邮件声明有附件但服务器未返回附件元数据，将在下次读取时重试")
	}

	return constructRFC822Message(source.Item, source.PlainBody, serverID, attachments), complete
}

func validRFC822(raw []byte) bool {
	if len(raw) <= 10 {
		return false
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	return err == nil && len(msg.Header) > 0
}

// decodeBase64Chunk 严格解码一段 base64（去空白）。用于附件分块等"确定是
// base64"的场景——分块路径不存在"其实是原文"的歧义，故不做短输入保护。
func decodeBase64Chunk(raw []byte) ([]byte, error) {
	compact := make([]byte, 0, len(raw))
	for _, b := range raw {
		if b != '\r' && b != '\n' && b != ' ' && b != '	' {
			compact = append(compact, b)
		}
	}
	dec := make([]byte, base64.StdEncoding.DecodedLen(len(compact)))
	n, err := base64.StdEncoding.Decode(dec, compact)
	if err != nil {
		return nil, err
	}
	return dec[:n], nil
}

// decodeAttachmentData 解码 FetchAttachment 返回的附件数据。
// fork 库保持上游语义：Data 是未解码的 base64 原文（见 FORK-NOTES.md）。
// 判定规则（与 webank-mail 保持一致）：
//  1. 声明大小 EstimatedDataSize：解码后长度==声明大小 → 是 base64；原文长度==声明大小 → 是文本
//  2. 无声明大小时：合法 canonical base64 且长度>=16 才解码（短输入按文本处理防误判）
func decodeAttachmentData(raw []byte, declaredSize int64) []byte {
	dec, err := decodeBase64Chunk(raw)
	if err != nil {
		return raw // 不是合法 base64，按原文
	}
	n := len(dec)
	compactLen := 0
	for _, b := range raw {
		if b != '\r' && b != '\n' && b != ' ' && b != '	' {
			compactLen++
		}
	}
	if declaredSize > 0 {
		if int64(n) == declaredSize {
			return dec
		}
		if int64(len(raw)) == declaredSize {
			return raw
		}
		// 声明大小对不上任何一边：仍按"更像 base64"处理（服务器大小字段是估计值）
		if compactLen >= 16 {
			return dec
		}
		return raw
	}
	if compactLen >= 16 {
		return dec
	}
	return raw
}

func mergeEmailItem(base, fetched eas.EmailItem) eas.EmailItem {
	out := mergeEmailMetadata(base, fetched)
	if fetched.Body != "" {
		out.Body = fetched.Body
		if fetched.BodyType != eas.BodyTypeNone {
			out.BodyType = fetched.BodyType
		}
		out.BodyTruncated = fetched.BodyTruncated
	}
	if len(fetched.BodyMIME) > 0 {
		out.BodyMIME = fetched.BodyMIME
	}
	return out
}

func mergeEmailMetadata(base, fetched eas.EmailItem) eas.EmailItem {
	out := base
	if fetched.ServerID != "" {
		out.ServerID = fetched.ServerID
	}
	if fetched.Subject != "" {
		out.Subject = fetched.Subject
	}
	if fetched.From != "" {
		out.From = fetched.From
	}
	if fetched.To != "" {
		out.To = fetched.To
	}
	if fetched.DisplayTo != "" {
		out.DisplayTo = fetched.DisplayTo
	}
	if fetched.Cc != "" {
		out.Cc = fetched.Cc
	}
	if fetched.Bcc != "" {
		out.Bcc = fetched.Bcc
	}
	if fetched.ReplyTo != "" {
		out.ReplyTo = fetched.ReplyTo
	}
	if fetched.Sender != "" {
		out.Sender = fetched.Sender
	}
	if !fetched.DateReceived.IsZero() {
		out.DateReceived = fetched.DateReceived
	}
	if fetched.BodyEstimatedSize != 0 {
		out.BodyEstimatedSize = fetched.BodyEstimatedSize
	}
	if len(fetched.Attachments) > 0 {
		out.Attachments = mergeAttachments(out.Attachments, fetched.Attachments)
	}
	out.HasAttachments = out.HasAttachments || fetched.HasAttachments || len(out.Attachments) > 0
	if fetched.ThreadTopic != "" {
		out.ThreadTopic = fetched.ThreadTopic
	}
	if len(fetched.ConversationID) > 0 {
		out.ConversationID = fetched.ConversationID
	}
	if fetched.MessageClass != "" {
		out.MessageClass = fetched.MessageClass
	}
	if fetched.BodyPreview != "" {
		out.BodyPreview = fetched.BodyPreview
	}
	if len(fetched.Categories) > 0 {
		out.Categories = fetched.Categories
	}
	return out
}

func mergeAttachments(base, fetched []eas.Attachment) []eas.Attachment {
	out := append([]eas.Attachment(nil), base...)
	byReference := make(map[string]int, len(out))
	for i, attachment := range out {
		if attachment.FileReference != "" {
			byReference[attachment.FileReference] = i
		}
	}
	for _, attachment := range fetched {
		if i, ok := byReference[attachment.FileReference]; attachment.FileReference != "" && ok {
			out[i] = mergeAttachmentMetadata(out[i], attachment)
			continue
		}
		if attachment.FileReference != "" {
			byReference[attachment.FileReference] = len(out)
		}
		out = append(out, attachment)
	}
	return out
}

func mergeAttachmentMetadata(base, fetched eas.Attachment) eas.Attachment {
	out := base
	if fetched.DisplayName != "" {
		out.DisplayName = fetched.DisplayName
	}
	if fetched.FileReference != "" {
		out.FileReference = fetched.FileReference
	}
	if fetched.Method != 0 {
		out.Method = fetched.Method
	}
	if fetched.EstimatedDataSize > 0 {
		out.EstimatedDataSize = fetched.EstimatedDataSize
	}
	if fetched.ContentID != "" {
		out.ContentID = fetched.ContentID
	}
	if fetched.ContentLocation != "" {
		out.ContentLocation = fetched.ContentLocation
	}
	if fetched.ContentType != "" {
		out.ContentType = fetched.ContentType
	}
	out.IsInline = out.IsInline || fetched.IsInline
	return out
}

func constructRFC822(item eas.EmailItem) []byte {
	return constructRFC822Message(item, "", item.ServerID, nil)
}

func constructRFC822WithAttachments(item eas.EmailItem, seed string, attachments []mailAttachment) []byte {
	for i := range attachments {
		attachments[i].index = i
	}
	return constructRFC822Message(item, "", seed, attachments)
}

func constructRFC822Message(item eas.EmailItem, plainBody, seed string, attachments []mailAttachment) []byte {
	root := buildMIMEEntity(item, plainBody, seed, attachments)
	var buf bytes.Buffer
	if err := writeRFC822(&buf, item, seed, root, mimeRenderOptions{
		actualAttachments: allAttachmentIndices(root),
	}); err != nil {
		return nil
	}
	return buf.Bytes()
}

func writeRFC822(w io.Writer, item eas.EmailItem, seed string, root mimeEntity, opts mimeRenderOptions) error {
	writeHeader := func(name, value string) error {
		if value == "" {
			return nil
		}
		_, err := fmt.Fprintf(w, "%s: %s\r\n", name, sanitizeHeader(value))
		return err
	}

	for _, header := range [][2]string{
		{"From", formatAddressHeader(item.From)},
		{"To", formatAddressHeader(item.To)},
		{"Cc", formatAddressHeader(item.Cc)},
		{"Reply-To", formatAddressHeader(item.ReplyTo)},
		{"Subject", encodeHeaderWord(item.Subject)},
	} {
		if err := writeHeader(header[0], header[1]); err != nil {
			return err
		}
	}
	if !item.DateReceived.IsZero() {
		if err := writeHeader("Date", item.DateReceived.Format("Mon, 02 Jan 2006 15:04:05 -0700")); err != nil {
			return err
		}
	}
	if seed != "" {
		sum := sha256.Sum256([]byte(seed))
		if err := writeHeader("Message-ID", "<"+hex.EncodeToString(sum[:12])+"@eas-bridge.local>"); err != nil {
			return err
		}
	}
	if err := writeHeader("MIME-Version", "1.0"); err != nil {
		return err
	}
	return root.writeTo(w, opts)
}

func buildMIMEEntity(item eas.EmailItem, plainBody, seed string, attachments []mailAttachment) mimeEntity {
	bodyType := "text/plain"
	if item.BodyType == eas.BodyTypeHTML {
		bodyType = "text/html"
	}
	body := item.Body
	if body == "" {
		body = "(no body)"
	}
	htmlOrPlain := textEntity(bodyType, body)
	content := htmlOrPlain
	if bodyType == "text/html" && plainBody != "" {
		plain := textEntity("text/plain", plainBody)
		content = mimeEntity{
			contentType: mime.FormatMediaType("multipart/alternative", map[string]string{
				"boundary": mimeBoundary(seed, "alternative"),
			}),
			boundary: mimeBoundary(seed, "alternative"),
			children: []mimeEntity{plain, htmlOrPlain},
		}
	}

	var inline, regular []mimeEntity
	for i := range attachments {
		attachment := attachments[i]
		attachment.inline = isInlineAttachment(item.Body, attachment.meta)
		entity := attachmentEntity(attachment)
		if attachment.inline {
			inline = append(inline, entity)
		} else {
			regular = append(regular, entity)
		}
	}

	if len(inline) > 0 {
		content = mimeEntity{
			contentType: mime.FormatMediaType("multipart/related", map[string]string{
				"boundary": mimeBoundary(seed, "related"),
				"type":     content.mediaType(),
			}),
			boundary: mimeBoundary(seed, "related"),
			children: append([]mimeEntity{content}, inline...),
		}
	}
	if len(regular) > 0 {
		content = mimeEntity{
			contentType: mime.FormatMediaType("multipart/mixed", map[string]string{
				"boundary": mimeBoundary(seed, "mixed"),
			}),
			boundary: mimeBoundary(seed, "mixed"),
			children: append([]mimeEntity{content}, regular...),
		}
	}
	return content
}

func textEntity(mediaType, body string) mimeEntity {
	return mimeEntity{
		contentType:      mime.FormatMediaType(mediaType, map[string]string{"charset": "utf-8"}),
		transferEncoding: "quoted-printable",
		body:             []byte(body),
	}
}

func attachmentEntity(attachment mailAttachment) mimeEntity {
	name := sanitizeFilename(attachment.meta.DisplayName)
	contentType := normalizeContentType(attachment.contentType, name, attachment.data, attachment.meta.Method)
	contentTypeParams := map[string]string{}
	if name != "" {
		contentTypeParams["name"] = name
	}
	disposition := "attachment"
	if attachment.inline {
		disposition = "inline"
	}
	dispositionParams := map[string]string{}
	if name != "" {
		dispositionParams["filename"] = name
	}
	attachmentCopy := attachment
	return mimeEntity{
		contentType:      mime.FormatMediaType(contentType, contentTypeParams),
		transferEncoding: "base64",
		disposition:      mime.FormatMediaType(disposition, dispositionParams),
		contentID:        normalizeContentID(attachment.meta.ContentID),
		contentLocation:  sanitizeHeader(attachment.meta.ContentLocation),
		attachment:       &attachmentCopy,
	}
}

func (entity mimeEntity) mediaType() string {
	mediaType, _, err := mime.ParseMediaType(entity.contentType)
	if err != nil {
		return "application/octet-stream"
	}
	return mediaType
}

func (entity mimeEntity) writeTo(w io.Writer, opts mimeRenderOptions) error {
	if _, err := fmt.Fprintf(w, "Content-Type: %s\r\n", entity.contentType); err != nil {
		return err
	}
	for _, header := range [][2]string{
		{"Content-Transfer-Encoding", entity.transferEncoding},
		{"Content-Disposition", entity.disposition},
	} {
		if header[1] != "" {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", header[0], header[1]); err != nil {
				return err
			}
		}
	}
	if entity.contentID != "" {
		if _, err := fmt.Fprintf(w, "Content-ID: <%s>\r\n", entity.contentID); err != nil {
			return err
		}
	}
	if entity.contentLocation != "" {
		if _, err := fmt.Fprintf(w, "Content-Location: %s\r\n", entity.contentLocation); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "\r\n"); err != nil {
		return err
	}

	if len(entity.children) > 0 {
		for _, child := range entity.children {
			if _, err := fmt.Fprintf(w, "--%s\r\n", entity.boundary); err != nil {
				return err
			}
			if err := child.writeTo(w, opts); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(w, "--%s--\r\n", entity.boundary)
		return err
	}

	if entity.attachment != nil {
		attachment := entity.attachment
		if opts.actualAttachments[attachment.index] {
			data := attachment.data
			if data == nil && opts.resolveAttachment != nil {
				var err error
				data, err = opts.resolveAttachment(attachment.index)
				if err != nil {
					return err
				}
			}
			return writeBase64(w, data)
		}
		if opts.estimatedPayloads {
			return writeBase64Placeholder(w, attachment.meta.EstimatedDataSize)
		}
		_, err := io.WriteString(w, "\r\n")
		return err
	}

	switch entity.transferEncoding {
	case "quoted-printable":
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write(entity.body); err != nil {
			_ = qp.Close()
			return err
		}
		if err := qp.Close(); err != nil {
			return err
		}
		_, err := io.WriteString(w, "\r\n")
		return err
	default:
		if _, err := w.Write(entity.body); err != nil {
			return err
		}
		_, err := io.WriteString(w, "\r\n")
		return err
	}
}

func (entity mimeEntity) entityAtPath(path []int) *mimeEntity {
	current := &entity
	if len(current.children) == 0 && len(path) > 0 && path[0] == 1 {
		path = path[1:]
	}
	for _, part := range path {
		if part <= 0 || part > len(current.children) {
			return nil
		}
		current = &current.children[part-1]
	}
	return current
}

func allAttachmentIndices(entity mimeEntity) map[int]bool {
	out := make(map[int]bool)
	var walk func(mimeEntity)
	walk = func(current mimeEntity) {
		if current.attachment != nil {
			out[current.attachment.index] = true
		}
		for _, child := range current.children {
			walk(child)
		}
	}
	walk(entity)
	return out
}

func writeBase64(w io.Writer, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 0 {
		n := min(76, len(encoded))
		if _, err := io.WriteString(w, encoded[:n]+"\r\n"); err != nil {
			return err
		}
		encoded = encoded[n:]
	}
	if len(data) == 0 {
		_, err := io.WriteString(w, "\r\n")
		return err
	}
	return nil
}

func writeBase64Placeholder(w io.Writer, rawSize int64) error {
	if rawSize <= 0 {
		_, err := io.WriteString(w, "\r\n")
		return err
	}
	encodedSize := ((rawSize + 2) / 3) * 4
	const placeholder = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for encodedSize > 0 {
		n := min(int64(len(placeholder)), encodedSize)
		if _, err := io.WriteString(w, placeholder[:n]+"\r\n"); err != nil {
			return err
		}
		encodedSize -= n
	}
	return nil
}

// splitEASAddresses 按 EAS 的分号分隔切地址列表，忽略引号内（含转义）的分号。
// 显示名可能含字面分号（如 "a;b" <x@y>）——直接 ReplaceAll/Split 会把名字
// 切开或篡改（2026-07-25 ZCode full-review LOW）。
func splitEASAddresses(value string) []string {
	var out []string
	var b strings.Builder
	inQuote, escaped := false, false
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for _, ch := range value {
		switch {
		case escaped:
			b.WriteRune(ch)
			escaped = false
		case ch == '\\' && inQuote:
			b.WriteRune(ch)
			escaped = true
		case ch == '"':
			inQuote = !inQuote
			b.WriteRune(ch)
		case ch == ';' && !inQuote:
			flush()
		default:
			b.WriteRune(ch)
		}
	}
	flush()
	return out
}

func formatAddressHeader(value string) string {
	value = sanitizeHeader(value)
	if value == "" {
		return ""
	}
	addresses, err := mail.ParseAddressList(strings.Join(splitEASAddresses(value), ", "))
	if err != nil {
		return formatAddressHeaderFallback(value)
	}
	formatted := make([]string, 0, len(addresses))
	for _, address := range addresses {
		formatted = append(formatted, address.String())
	}
	return strings.Join(formatted, ", ")
}

func formatAddressHeaderFallback(value string) string {
	parts := splitEASAddresses(value)
	formatted := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		open := strings.LastIndex(part, "<")
		if open >= 0 && strings.HasSuffix(part, ">") {
			displayName := strings.Trim(strings.TrimSpace(part[:open]), "\"'")
			address := strings.TrimSpace(part[open+1 : len(part)-1])
			if parsed, err := mail.ParseAddress(address); err == nil && parsed.Address != "" {
				formatted = append(formatted, (&mail.Address{Name: displayName, Address: parsed.Address}).String())
				continue
			}
		}
		formatted = append(formatted, encodeHeaderWord(part))
	}
	return strings.Join(formatted, ", ")
}

func encodeHeaderWord(value string) string {
	value = sanitizeHeader(value)
	if value == "" || utf8.RuneCountInString(value) == len(value) {
		return value
	}
	return mime.QEncoding.Encode("UTF-8", value)
}

func sanitizeHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}

func sanitizeFilename(value string) string {
	value = sanitizeHeader(value)
	value = filepath.Base(strings.ReplaceAll(value, "\\", "/"))
	if value == "." || value == ".." || value == "/" {
		return ""
	}
	return value
}

func isInlineAttachment(body string, attachment eas.Attachment) bool {
	if attachment.IsInline || attachment.Method == 6 {
		return true
	}
	contentID := strings.TrimPrefix(strings.ToLower(normalizeContentID(attachment.ContentID)), "cid:")
	if contentID == "" {
		return false
	}
	return strings.Contains(strings.ToLower(body), "cid:"+contentID)
}

func normalizeContentID(value string) string {
	return strings.Trim(sanitizeHeader(value), "<>")
}

func normalizeContentType(reported, filename string, data []byte, method int) string {
	if method == 5 {
		return "message/rfc822"
	}
	for _, candidate := range []string{reported, mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))} {
		if mediaType, _, err := mime.ParseMediaType(candidate); err == nil && strings.Contains(mediaType, "/") {
			return mediaType
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

func mimeBoundary(seed, kind string) string {
	sum := sha256.Sum256([]byte(seed + "\x00" + kind))
	return "eas-bridge-" + kind + "-" + hex.EncodeToString(sum[:12])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
