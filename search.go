package main

// IMAP SEARCH 过滤实现。纯函数核心（filterSearch）便于测试，
// Search() 只负责组装 searchContext 和结果集。
//
// 能力范围（v2）：
//   - 序号/UID 集合、日期（SINCE/BEFORE，内部日期=DateReceived）
//   - Header 字段（SUBJECT/FROM/TO/CC/BCC/REPLY-TO/SENDER，空值=存在性检查）
//   - 标志位：\Seen \Deleted \Flagged（\Answered/\Draft 无服务器数据，恒不匹配）
//   - 尺寸 LARGER/SMALLER（BodyEstimatedSize，严格大于/小于）
//   - NOT / OR / 多条件 AND
//   - BODY/TEXT：仅搜索已缓存正文（读过的邮件会落盘），未缓存邮件不参与
//     正文匹配——SEARCH 没有 ctx，为搜索触发几百封网络拉取不现实；
//     Apple Mail 的全文搜索本来也走本地索引，不受影响

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/hstern/go-activesync/eas"
)

// searchContext 提供过滤所需的数据访问。
type searchContext struct {
	snap *mboxSnapshot
	// bodyText 返回已缓存正文的纯文本（无缓存时 ok=false）。
	bodyText func(serverID string) (text string, ok bool)
}

// filterSearch 返回匹配条件的 item 下标（保持 snap.items 顺序）。
func filterSearch(sc *searchContext, c *imap.SearchCriteria) []int {
	var out []int
	for i := range sc.snap.items {
		if matchSearchItem(sc, i, c) {
			out = append(out, i)
		}
	}
	return out
}

func matchSearchItem(sc *searchContext, idx int, c *imap.SearchCriteria) bool {
	it := sc.snap.items[idx]
	seq := uint32(idx + 1)
	uid := sc.snap.uidForSID[it.ServerID]

	// 序号/UID 集合：多个集合为 AND（每个集合都必须命中）
	for _, set := range c.SeqNum {
		if !seqSetContains(set, seq) {
			return false
		}
	}
	for _, set := range c.UID {
		if !uidSetContains(set, imap.UID(uid)) {
			return false
		}
	}

	// 日期（只比日期部分，RFC 9051）。没有独立的发送日期字段，Sent* 同用 DateReceived。
	d := dateOnly(it.DateReceived)
	if !c.Since.IsZero() && d.Before(dateOnly(c.Since)) {
		return false
	}
	if !c.Before.IsZero() && !d.Before(dateOnly(c.Before)) {
		return false
	}
	if !c.SentSince.IsZero() && d.Before(dateOnly(c.SentSince)) {
		return false
	}
	if !c.SentBefore.IsZero() && !d.Before(dateOnly(c.SentBefore)) {
		return false
	}

	// Header 字段
	for _, hf := range c.Header {
		val := searchHeaderValue(it, hf.Key)
		if hf.Value == "" {
			if val == "" { // 空值 = 存在性检查
				return false
			}
		} else if !containsFold(val, hf.Value) {
			return false
		}
	}

	// TEXT：头部 + 已缓存正文
	for _, s := range c.Text {
		hay := it.Subject + "\n" + it.From + "\n" + it.To + "\n" + it.Cc + "\n" + it.Bcc
		if bt, ok := sc.bodyText(it.ServerID); ok {
			hay += "\n" + bt
		}
		if !containsFold(hay, s) {
			return false
		}
	}
	// BODY：仅已缓存正文
	for _, s := range c.Body {
		bt, ok := sc.bodyText(it.ServerID)
		if !ok || !containsFold(bt, s) {
			return false
		}
	}

	// 标志位
	for _, f := range c.Flag {
		if !itemHasFlag(sc.snap, it, f) {
			return false
		}
	}
	for _, f := range c.NotFlag {
		if itemHasFlag(sc.snap, it, f) {
			return false
		}
	}

	// 尺寸（RFC 9051：LARGER 严格大于，SMALLER 严格小于）
	size := int64(it.BodyEstimatedSize)
	if c.Larger > 0 && size <= c.Larger {
		return false
	}
	if c.Smaller > 0 && size >= c.Smaller {
		return false
	}

	// NOT / OR
	for i := range c.Not {
		if matchSearchItem(sc, idx, &c.Not[i]) {
			return false
		}
	}
	for _, pair := range c.Or {
		left, right := pair[0], pair[1]
		if !matchSearchItem(sc, idx, &left) && !matchSearchItem(sc, idx, &right) {
			return false
		}
	}
	return true
}

// itemHasFlag 判定邮件是否带某标志。无服务器数据支撑的标志（\Answered/
// \Draft/\Recent）恒不匹配——比"返回全部"更符合 RFC 语义。
func itemHasFlag(snap *mboxSnapshot, it eas.EmailItem, f imap.Flag) bool {
	switch strings.ToUpper(string(f)) {
	case "\\SEEN":
		return it.Read
	case "\\DELETED":
		return snap.deleted[it.ServerID]
	case "\\FLAGGED":
		return it.FlagStatus == 2 // active
	default:
		return false
	}
}

// searchHeaderValue 把 SEARCH 的 header 字段名映射到 EmailItem。
// 未知字段返回空串（存在性检查不匹配，值匹配也不匹配）。
func searchHeaderValue(it eas.EmailItem, key string) string {
	switch strings.ToUpper(key) {
	case "SUBJECT":
		return it.Subject
	case "FROM":
		return it.From
	case "TO":
		return it.To
	case "CC":
		return it.Cc
	case "BCC":
		return it.Bcc
	case "REPLY-TO":
		return it.ReplyTo
	case "SENDER":
		return it.Sender
	case "DATE":
		if !it.DateReceived.IsZero() {
			return it.DateReceived.Format(time.RFC1123Z)
		}
		return ""
	default:
		return ""
	}
}

// containsFold 大小写不敏感的子串匹配（Unicode 简单折叠）。
func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

// extractSearchableText 从原始 RFC822 提取可搜索文本：头部关键字段 +
// 所有 text/plain 与 text/html（去标签）正文，传输编码（QP/base64）解码。
// 用于 BODY/TEXT 搜索的缓存命中路径；解析失败回退为原始字节。
func extractSearchableText(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return string(raw)
	}
	var sb strings.Builder
	dec := new(mime.WordDecoder)
	for _, h := range []string{"Subject", "From", "To", "Cc", "Bcc", "Date"} {
		if v := msg.Header.Get(h); v != "" {
			if dv, err := dec.DecodeHeader(v); err == nil {
				v = dv
			}
			sb.WriteString(v)
			sb.WriteByte('\n')
		}
	}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		sb.WriteString(string(raw))
		return sb.String()
	}
	extractPartText(&sb, mediaType, params, msg.Body, msg.Header.Get("Content-Transfer-Encoding"), 0)
	return sb.String()
}

// extractPartText 递归遍历 MIME 树提取文本（深度上限 10 防畸形嵌套）。
func extractPartText(sb *strings.Builder, mediaType string, params map[string]string, body io.Reader, cte string, depth int) {
	if depth > 10 {
		return
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			ct := part.Header.Get("Content-Type")
			mt, pp, _ := mime.ParseMediaType(ct)
			if mt == "" {
				mt = "text/plain" // 无 Content-Type 的 part 按 RFC 2046 默认 text/plain
			}
			if strings.HasPrefix(mt, "multipart/") {
				extractPartText(sb, mt, pp, part, part.Header.Get("Content-Transfer-Encoding"), depth+1)
				continue
			}
			if mt != "text/plain" && mt != "text/html" {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(part, 1<<20))
			if err != nil {
				continue
			}
			text := decodeTransfer(data, part.Header.Get("Content-Transfer-Encoding"))
			if mt == "text/html" {
				text = stripHTMLTags(text)
			}
			sb.WriteString(text)
			sb.WriteByte('\n')
		}
		return
	}
	data, _ := io.ReadAll(io.LimitReader(body, 1<<20))
	text := decodeTransfer(data, cte)
	if mediaType == "text/html" {
		text = stripHTMLTags(text)
	}
	sb.WriteString(text)
}

func decodeTransfer(data []byte, encoding string) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		if d, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(data))); err == nil {
			return string(d)
		}
	case "base64":
		if d, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(data))); err == nil {
			return string(d)
		}
	}
	return string(data)
}

// stripHTMLTags 粗粒度去标签（搜索用途足够，不做实体解码）。
func stripHTMLTags(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func seqSetContains(set imap.SeqSet, n uint32) bool {
	return set.Contains(n)
}

func uidSetContains(set imap.UIDSet, n imap.UID) bool {
	return set.Contains(n)
}
