package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/hstern/go-activesync/eas"
)

type fakeMessageContentClient struct {
	fetches     []eas.BodyType
	attachments []string
}

func (c *fakeMessageContentClient) FetchEmail(_ context.Context, _, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
	c.fetches = append(c.fetches, opts.BodyType)
	switch opts.BodyType {
	case eas.BodyTypeMIME:
		return &eas.EmailItem{ServerID: serverID}, nil
	case eas.BodyTypeHTML:
		return &eas.EmailItem{
			ServerID: serverID,
			Subject:  "格式化邮件",
			BodyType: eas.BodyTypeHTML,
			Body:     `<html><body><h1>标题</h1><img src="cid:logo"></body></html>`,
			Attachments: []eas.Attachment{
				{DisplayName: "logo.png", FileReference: "inline-ref", ContentID: "logo", IsInline: true, ContentType: "image/png"},
				{DisplayName: "报告.pdf", FileReference: "file-ref", ContentType: "application/pdf"},
			},
			HasAttachments: true,
		}, nil
	case eas.BodyTypePlain:
		return &eas.EmailItem{
			ServerID: serverID,
			BodyType: eas.BodyTypePlain,
			Body:     "标题",
		}, nil
	default:
		return nil, nil
	}
}

func (c *fakeMessageContentClient) FetchAttachment(_ context.Context, fileReference string, _, _ int64) (*eas.FetchAttachmentResult, error) {
	c.attachments = append(c.attachments, fileReference)
	return &eas.FetchAttachmentResult{Data: []byte("data:" + fileReference)}, nil
}

type parsedLeaf struct {
	mediaType   string
	disposition string
	filename    string
	contentID   string
	body        []byte
}

type bodyScenarioClient struct {
	html     *eas.EmailItem
	htmlErr  error
	plain    *eas.EmailItem
	plainErr error
}

func (c *bodyScenarioClient) FetchEmail(_ context.Context, _, serverID string, opts eas.FetchEmailOptions) (*eas.EmailItem, error) {
	switch opts.BodyType {
	case eas.BodyTypeMIME:
		return &eas.EmailItem{ServerID: serverID}, nil
	case eas.BodyTypeHTML:
		return c.html, c.htmlErr
	case eas.BodyTypePlain:
		return c.plain, c.plainErr
	default:
		return nil, nil
	}
}

func (*bodyScenarioClient) FetchAttachment(context.Context, string, int64, int64) (*eas.FetchAttachmentResult, error) {
	return nil, nil
}

func TestFetchAndBuildMIMEFallsBackToHTMLAndAttachments(t *testing.T) {
	client := &fakeMessageContentClient{}
	got, complete := fetchAndBuildMIME(context.Background(), client, "inbox", "message-1", eas.EmailItem{
		ServerID: "message-1",
		From:     "发件人 <sender@example.com>",
		To:       "receiver@example.com",
	})
	if !complete {
		t.Fatal("complete = false, want true")
	}
	if want := []eas.BodyType{eas.BodyTypeMIME, eas.BodyTypeHTML, eas.BodyTypePlain}; !equalBodyTypes(client.fetches, want) {
		t.Fatalf("FetchEmail body types = %v, want %v", client.fetches, want)
	}
	if got, want := strings.Join(client.attachments, ","), "inline-ref,file-ref"; got != want {
		t.Fatalf("FetchAttachment refs = %q, want %q", got, want)
	}

	msg, err := mail.ReadMessage(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	decodedSubject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if decodedSubject != "格式化邮件" {
		t.Fatalf("Subject = %q", decodedSubject)
	}

	leaves := collectLeaves(t, textproto.MIMEHeader(msg.Header), msg.Body)
	assertLeaf(t, leaves, "text/plain", "", "", []byte("标题"))
	assertLeaf(t, leaves, "text/html", "", "", []byte(`<html><body><h1>标题</h1><img src="cid:logo"></body></html>`))
	assertLeaf(t, leaves, "image/png", "inline", "logo.png", []byte("data:inline-ref"))
	assertLeaf(t, leaves, "application/pdf", "attachment", "报告.pdf", []byte("data:file-ref"))
}

func TestConstructRFC822PlainText(t *testing.T) {
	got := constructRFC822(eas.EmailItem{
		ServerID:     "plain-1",
		Subject:      "你好",
		From:         "张三 <sender@example.com>",
		To:           "receiver@example.com",
		DateReceived: time.Date(2026, 7, 24, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
		BodyType:     eas.BodyTypePlain,
		Body:         "第一行\n第二行",
	})
	msg, err := mail.ReadMessage(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	leaves := collectLeaves(t, textproto.MIMEHeader(msg.Header), msg.Body)
	assertLeaf(t, leaves, "text/plain", "", "", []byte("第一行\r\n第二行"))
}

func TestTruncatedHTMLIsNotOverwrittenOrCached(t *testing.T) {
	client := &bodyScenarioClient{
		html: &eas.EmailItem{
			ServerID:      "message",
			BodyType:      eas.BodyTypeHTML,
			Body:          "<p>truncated HTML</p>",
			BodyTruncated: true,
		},
		plain: &eas.EmailItem{
			ServerID: "message",
			BodyType: eas.BodyTypePlain,
			Body:     "complete plain text",
		},
	}
	source, err := fetchMessageSource(context.Background(), client, "inbox", "message", eas.EmailItem{ServerID: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if source.Item.BodyType != eas.BodyTypeHTML || source.Item.Body != "<p>truncated HTML</p>" {
		t.Fatalf("HTML was overwritten: %+v", source.Item)
	}
	if source.PlainBody != "complete plain text" {
		t.Fatalf("PlainBody = %q", source.PlainBody)
	}
	if source.Complete {
		t.Fatal("truncated HTML source must not be cached")
	}
}

func TestMalformedUnicodeAddressFallbackIsRFC2047Safe(t *testing.T) {
	got := constructRFC822(eas.EmailItem{
		ServerID: "address",
		From:     "Doe, 张三 <sender@example.com>",
		To:       "receiver@example.com",
		BodyType: eas.BodyTypePlain,
		Body:     "body",
	})
	headerEnd := bytes.Index(got, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		t.Fatal("missing header terminator")
	}
	for _, b := range got[:headerEnd] {
		if b >= 0x80 {
			t.Fatalf("header contains raw UTF-8 byte 0x%x", b)
		}
	}
	msg, err := mail.ReadMessage(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	address, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil {
		t.Fatalf("ParseAddress: %v; header=%q", err, msg.Header.Get("From"))
	}
	if address.Name != "Doe, 张三" || address.Address != "sender@example.com" {
		t.Fatalf("From = %+v", address)
	}
}

func TestContentIDOnlyIsInlineWhenReferenced(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		disposition string
	}{
		{name: "unreferenced", body: "<p>no image</p>", disposition: "attachment"},
		{name: "referenced", body: `<img src="cid:asset-id">`, disposition: "inline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := eas.EmailItem{
				ServerID: "message",
				BodyType: eas.BodyTypeHTML,
				Body:     test.body,
			}
			got := constructRFC822WithAttachments(item, "message", []mailAttachment{{
				meta: eas.Attachment{
					DisplayName:   "asset.bin",
					FileReference: "asset",
					ContentID:     "asset-id",
				},
				data: []byte("asset"),
			}})
			msg, err := mail.ReadMessage(bytes.NewReader(got))
			if err != nil {
				t.Fatal(err)
			}
			leaves := collectLeaves(t, textproto.MIMEHeader(msg.Header), msg.Body)
			assertLeaf(t, leaves, "application/octet-stream", test.disposition, "asset.bin", []byte("asset"))
		})
	}
}

func TestMergeEmailMetadataUnionsAttachments(t *testing.T) {
	base := eas.EmailItem{
		Attachments: []eas.Attachment{
			{FileReference: "one", DisplayName: "one.bin", EstimatedDataSize: 10},
			{FileReference: "two", DisplayName: "two.bin"},
		},
	}
	fetched := eas.EmailItem{
		Attachments: []eas.Attachment{
			{FileReference: "one", ContentType: "application/one"},
			{FileReference: "three", DisplayName: "three.bin"},
		},
	}
	got := mergeEmailMetadata(base, fetched)
	if len(got.Attachments) != 3 {
		t.Fatalf("attachments = %+v", got.Attachments)
	}
	if got.Attachments[0].DisplayName != "one.bin" ||
		got.Attachments[0].ContentType != "application/one" ||
		got.Attachments[0].EstimatedDataSize != 10 {
		t.Fatalf("merged attachment = %+v", got.Attachments[0])
	}
	if got.Attachments[1].FileReference != "two" || got.Attachments[2].FileReference != "three" {
		t.Fatalf("attachment union order = %+v", got.Attachments)
	}
}

func TestSanitizeFilenameRejectsDotDot(t *testing.T) {
	if got := sanitizeFilename("../.."); got != "" {
		t.Fatalf("sanitizeFilename = %q", got)
	}
}

func collectLeaves(t *testing.T, header textproto.MIMEHeader, body io.Reader) []parsedLeaf {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("ParseMediaType(%q): %v", header.Get("Content-Type"), err)
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		reader := multipart.NewReader(body, params["boundary"])
		var leaves []parsedLeaf
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				return leaves
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			leaves = append(leaves, collectLeaves(t, part.Header, part)...)
		}
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	switch strings.ToLower(header.Get("Content-Transfer-Encoding")) {
	case "base64":
		raw, err = io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(raw)))
	case "quoted-printable":
		raw, err = io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
	}
	if err != nil {
		t.Fatalf("decode body: %v", err)
	}
	disposition, dispositionParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	return []parsedLeaf{{
		mediaType:   mediaType,
		disposition: disposition,
		filename:    dispositionParams["filename"],
		contentID:   strings.Trim(header.Get("Content-ID"), "<>"),
		body:        bytes.TrimSuffix(raw, []byte("\r\n")),
	}}
}

func assertLeaf(t *testing.T, leaves []parsedLeaf, mediaType, disposition, filename string, body []byte) {
	t.Helper()
	for _, leaf := range leaves {
		if leaf.mediaType == mediaType && leaf.disposition == disposition && leaf.filename == filename {
			if !bytes.Equal(leaf.body, body) {
				t.Fatalf("%s/%s body = %q, want %q", mediaType, filename, leaf.body, body)
			}
			return
		}
	}
	t.Fatalf("missing leaf mediaType=%q disposition=%q filename=%q; got %+v", mediaType, disposition, filename, leaves)
}

func equalBodyTypes(a, b []eas.BodyType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDecodeAttachmentData(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 1, 2, 3, 4, 5, 6, 7, 8}
	b64 := base64.StdEncoding.EncodeToString(png)

	// fork 库返回未解码 base64 原文 + 声明大小匹配解码结果 → 解码
	if got := decodeAttachmentData([]byte(b64), int64(len(png))); !bytes.Equal(got, png) {
		t.Fatalf("base64 with declared size: got %x", got)
	}
	// 带换行的 base64（Coremail 实测会折行）→ 解码
	folded := b64[:12] + "\r\n" + b64[12:]
	if got := decodeAttachmentData([]byte(folded), int64(len(png))); !bytes.Equal(got, png) {
		t.Fatalf("folded base64: got %x", got)
	}
	// 原文长度 == 声明大小 → 按原文（已是二进制）
	if got := decodeAttachmentData(png, int64(len(png))); !bytes.Equal(got, png) {
		t.Fatalf("raw binary with declared size: got %x", got)
	}
	// 无声明大小 + 合法 base64 且 >=16 → 解码
	if got := decodeAttachmentData([]byte(b64), 0); !bytes.Equal(got, png) {
		t.Fatalf("base64 without declared size: got %x", got)
	}
	// 短输入一律按原文（"test" 也是合法 base64，防误判）
	if got := decodeAttachmentData([]byte("test"), 0); string(got) != "test" {
		t.Fatalf("short input: got %q", got)
	}
	// 非法 base64 按原文
	raw := []byte("data:not-base64!!")
	if got := decodeAttachmentData(raw, 0); !bytes.Equal(got, raw) {
		t.Fatalf("invalid base64: got %q", got)
	}
}

type base64AttachmentClient struct {
	fakeMessageContentClient
	payload []byte
}

func (c *base64AttachmentClient) FetchAttachment(_ context.Context, fileReference string, _, _ int64) (*eas.FetchAttachmentResult, error) {
	c.attachments = append(c.attachments, fileReference)
	return &eas.FetchAttachmentResult{
		Data: []byte(base64.StdEncoding.EncodeToString(c.payload)),
	}, nil
}

// 回归：fork 库 FetchAttachment 返回未解码 base64 原文，构建 MIME 时必须解码，
// 否则写出双层 base64，客户端解一层后拿到文本而非图片（2026-07-25 图片全挂事故）。
func TestFetchAndBuildMIMEDecodesBase64Attachment(t *testing.T) {
	payload := bytes.Repeat([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, 3) // PNG magic x3，base64 >=16 字符
	client := &base64AttachmentClient{payload: payload}
	got, complete := fetchAndBuildMIME(context.Background(), client, "inbox", "message-1", eas.EmailItem{
		ServerID: "message-1",
		From:     "sender@example.com",
		To:       "receiver@example.com",
	})
	if !complete {
		t.Fatal("complete = false, want true")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	leaves := collectLeaves(t, textproto.MIMEHeader(msg.Header), msg.Body)
	assertLeaf(t, leaves, "image/png", "inline", "logo.png", payload)
	assertLeaf(t, leaves, "application/pdf", "attachment", "报告.pdf", payload)
}
