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
	if want := []eas.BodyType{eas.BodyTypeMIME, eas.BodyTypeHTML}; !equalBodyTypes(client.fetches, want) {
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

func TestMIMECacheFilenameIsVersionedAndPathSafe(t *testing.T) {
	got := mimeCacheFilename("../../message/id")
	if strings.Contains(got, "/") || strings.Contains(got, "..") {
		t.Fatalf("unsafe cache filename %q", got)
	}
	if !strings.HasSuffix(got, ".eml") {
		t.Fatalf("cache filename %q has no .eml suffix", got)
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
