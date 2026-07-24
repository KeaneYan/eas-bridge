package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
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

const mimeCacheVersion = "v2"

type messageContentClient interface {
	FetchEmail(ctx context.Context, folderID, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error)
	FetchAttachment(ctx context.Context, fileReference string, rangeStart, rangeEnd int64) (*eas.FetchAttachmentResult, error)
}

type mailAttachment struct {
	meta        eas.Attachment
	data        []byte
	contentType string
}

type mimeEntity struct {
	contentType      string
	transferEncoding string
	disposition      string
	contentID        string
	contentLocation  string
	body             []byte
	boundary         string
	children         []mimeEntity
}

func mimeCacheFilename(serverID string) string {
	sum := sha256.Sum256([]byte(mimeCacheVersion + "\x00" + serverID))
	return hex.EncodeToString(sum[:]) + ".eml"
}

func fetchAndBuildMIME(ctx context.Context, c messageContentClient, folderID, serverID string, summary eas.EmailItem) ([]byte, bool) {
	raw, rawErr := c.FetchEmail(ctx, folderID, serverID, eas.FetchEmailOptions{BodyType: eas.BodyTypeMIME})
	if rawErr == nil && raw != nil && validRFC822(raw.BodyMIME) {
		return raw.BodyMIME, true
	}
	if rawErr != nil {
		log.Printf("[mime] 原始 MIME 不可用，改用 HTML 重建: %v", rawErr)
	}

	item := summary
	full, htmlErr := c.FetchEmail(ctx, folderID, serverID, eas.FetchEmailOptions{BodyType: eas.BodyTypeHTML})
	if htmlErr == nil && full != nil {
		item = mergeEmailItem(item, *full)
	} else if htmlErr != nil {
		log.Printf("[mime] HTML 正文拉取失败，使用同步缓存: %v", htmlErr)
	}

	if item.Body == "" || item.BodyTruncated {
		plain, plainErr := c.FetchEmail(ctx, folderID, serverID, eas.FetchEmailOptions{BodyType: eas.BodyTypePlain})
		if plainErr == nil && plain != nil {
			item = mergeEmailItem(item, *plain)
		} else if plainErr != nil {
			log.Printf("[mime] 完整纯文本正文拉取失败，使用同步缓存: %v", plainErr)
		}
	}

	attachments := make([]mailAttachment, 0, len(item.Attachments))
	complete := true
	for i, meta := range item.Attachments {
		if meta.FileReference == "" {
			complete = false
			log.Printf("[mime] 第 %d 个附件缺少 FileReference，已跳过", i+1)
			continue
		}
		got, err := c.FetchAttachment(ctx, meta.FileReference, 0, 0)
		if err != nil {
			complete = false
			log.Printf("[mime] 第 %d 个附件下载失败，将在下次读取时重试: %v", i+1, err)
			continue
		}
		attachments = append(attachments, mailAttachment{
			meta:        meta,
			data:        got.Data,
			contentType: firstNonEmpty(meta.ContentType, got.ContentType),
		})
	}
	if item.HasAttachments && len(item.Attachments) == 0 {
		complete = false
		log.Printf("[mime] 邮件声明有附件但服务器未返回附件元数据，将在下次读取时重试")
	}

	return constructRFC822WithAttachments(item, serverID, attachments), complete
}

func validRFC822(raw []byte) bool {
	if len(raw) <= 10 {
		return false
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	return err == nil && len(msg.Header) > 0
}

func mergeEmailItem(base, fetched eas.EmailItem) eas.EmailItem {
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
	if fetched.Cc != "" {
		out.Cc = fetched.Cc
	}
	if fetched.ReplyTo != "" {
		out.ReplyTo = fetched.ReplyTo
	}
	if !fetched.DateReceived.IsZero() {
		out.DateReceived = fetched.DateReceived
	}
	if fetched.BodyType != eas.BodyTypeNone {
		out.BodyType = fetched.BodyType
	}
	if fetched.Body != "" {
		out.Body = fetched.Body
	}
	if fetched.BodyEstimatedSize != 0 {
		out.BodyEstimatedSize = fetched.BodyEstimatedSize
	}
	out.BodyTruncated = fetched.BodyTruncated
	if len(fetched.Attachments) > 0 {
		out.Attachments = fetched.Attachments
	}
	out.HasAttachments = out.HasAttachments || fetched.HasAttachments || len(out.Attachments) > 0
	return out
}

func constructRFC822(item eas.EmailItem) []byte {
	return constructRFC822WithAttachments(item, item.ServerID, nil)
}

func constructRFC822WithAttachments(item eas.EmailItem, seed string, attachments []mailAttachment) []byte {
	var buf bytes.Buffer
	writeHeader := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&buf, "%s: %s\r\n", name, sanitizeHeader(value))
		}
	}

	writeHeader("From", formatAddressHeader(item.From))
	writeHeader("To", formatAddressHeader(item.To))
	writeHeader("Cc", formatAddressHeader(item.Cc))
	writeHeader("Reply-To", formatAddressHeader(item.ReplyTo))
	writeHeader("Subject", encodeHeaderWord(item.Subject))
	if !item.DateReceived.IsZero() {
		writeHeader("Date", item.DateReceived.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	}
	if seed != "" {
		sum := sha256.Sum256([]byte(seed))
		writeHeader("Message-ID", "<"+hex.EncodeToString(sum[:12])+"@eas-bridge.local>")
	}
	writeHeader("MIME-Version", "1.0")

	entity := buildMIMEEntity(item, seed, attachments)
	entity.writeTo(&buf)
	return buf.Bytes()
}

func buildMIMEEntity(item eas.EmailItem, seed string, attachments []mailAttachment) mimeEntity {
	bodyType := "text/plain"
	if item.BodyType == eas.BodyTypeHTML {
		bodyType = "text/html"
	}
	body := item.Body
	if body == "" {
		body = "(no body)"
	}
	bodyEntity := mimeEntity{
		contentType:      mime.FormatMediaType(bodyType, map[string]string{"charset": "utf-8"}),
		transferEncoding: "quoted-printable",
		body:             []byte(body),
	}

	var inline, regular []mimeEntity
	for _, attachment := range attachments {
		entity := attachmentEntity(attachment)
		if attachment.meta.IsInline || attachment.meta.Method == 6 || attachment.meta.ContentID != "" {
			inline = append(inline, entity)
		} else {
			regular = append(regular, entity)
		}
	}

	content := bodyEntity
	if len(inline) > 0 {
		content = mimeEntity{
			contentType: mime.FormatMediaType("multipart/related", map[string]string{
				"boundary": mimeBoundary(seed, "related"),
				"type":     bodyType,
			}),
			boundary: mimeBoundary(seed, "related"),
			children: append([]mimeEntity{bodyEntity}, inline...),
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

func attachmentEntity(attachment mailAttachment) mimeEntity {
	name := sanitizeFilename(attachment.meta.DisplayName)
	contentType := normalizeContentType(attachment.contentType, name, attachment.data, attachment.meta.Method)
	contentTypeParams := map[string]string{}
	if name != "" {
		contentTypeParams["name"] = name
	}
	disposition := "attachment"
	if attachment.meta.IsInline || attachment.meta.Method == 6 || attachment.meta.ContentID != "" {
		disposition = "inline"
	}
	dispositionParams := map[string]string{}
	if name != "" {
		dispositionParams["filename"] = name
	}
	return mimeEntity{
		contentType:      mime.FormatMediaType(contentType, contentTypeParams),
		transferEncoding: "base64",
		disposition:      mime.FormatMediaType(disposition, dispositionParams),
		contentID:        normalizeContentID(attachment.meta.ContentID),
		contentLocation:  sanitizeHeader(attachment.meta.ContentLocation),
		body:             attachment.data,
	}
}

func (entity mimeEntity) writeTo(buf *bytes.Buffer) {
	fmt.Fprintf(buf, "Content-Type: %s\r\n", entity.contentType)
	if entity.transferEncoding != "" {
		fmt.Fprintf(buf, "Content-Transfer-Encoding: %s\r\n", entity.transferEncoding)
	}
	if entity.disposition != "" {
		fmt.Fprintf(buf, "Content-Disposition: %s\r\n", entity.disposition)
	}
	if entity.contentID != "" {
		fmt.Fprintf(buf, "Content-ID: <%s>\r\n", entity.contentID)
	}
	if entity.contentLocation != "" {
		fmt.Fprintf(buf, "Content-Location: %s\r\n", entity.contentLocation)
	}
	buf.WriteString("\r\n")

	if len(entity.children) > 0 {
		for _, child := range entity.children {
			fmt.Fprintf(buf, "--%s\r\n", entity.boundary)
			child.writeTo(buf)
			ensureCRLF(buf)
		}
		fmt.Fprintf(buf, "--%s--\r\n", entity.boundary)
		return
	}

	switch entity.transferEncoding {
	case "base64":
		writeBase64(buf, entity.body)
	case "quoted-printable":
		qp := quotedprintable.NewWriter(buf)
		_, _ = qp.Write(entity.body)
		_ = qp.Close()
		ensureCRLF(buf)
	default:
		buf.Write(entity.body)
		ensureCRLF(buf)
	}
}

func writeBase64(buf *bytes.Buffer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 0 {
		n := min(76, len(encoded))
		buf.WriteString(encoded[:n])
		buf.WriteString("\r\n")
		encoded = encoded[n:]
	}
}

func ensureCRLF(buf *bytes.Buffer) {
	b := buf.Bytes()
	if len(b) >= 2 && b[len(b)-2] == '\r' && b[len(b)-1] == '\n' {
		return
	}
	if len(b) > 0 && b[len(b)-1] == '\n' {
		return
	}
	buf.WriteString("\r\n")
}

func formatAddressHeader(value string) string {
	value = sanitizeHeader(value)
	if value == "" {
		return ""
	}
	addresses, err := mail.ParseAddressList(strings.ReplaceAll(value, ";", ","))
	if err != nil {
		return value
	}
	formatted := make([]string, 0, len(addresses))
	for _, address := range addresses {
		formatted = append(formatted, address.String())
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
	if value == "." || value == "/" {
		return ""
	}
	return value
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
