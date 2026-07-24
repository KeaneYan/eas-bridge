package main

import (
	"strings"
	"testing"

	"github.com/hstern/go-activesync/eas"
)

func TestIMAPFolderNamesPreserveHierarchyAndFilterNonMail(t *testing.T) {
	folders := []eas.Folder{
		{ServerID: "inbox", DisplayName: "收件箱", Type: eas.FolderTypeInbox},
		{ServerID: "projects", ParentID: "inbox", DisplayName: "项目", Type: eas.FolderTypeUserMail},
		{ServerID: "archive", ParentID: "projects", DisplayName: "2026/归档", Type: eas.FolderTypeUserMail},
		{ServerID: "calendar", DisplayName: "日历", Type: eas.FolderTypeCalendar},
	}

	got := imapFolderNames(folders)
	if got["inbox"] != "INBOX" {
		t.Fatalf("inbox = %q, want INBOX", got["inbox"])
	}
	if got["projects"] != "INBOX/项目" {
		t.Fatalf("projects = %q", got["projects"])
	}
	if got["archive"] != "INBOX/项目/2026∕归档" {
		t.Fatalf("archive = %q", got["archive"])
	}
	if _, ok := got["calendar"]; ok {
		t.Fatal("calendar folder must not be exposed over IMAP")
	}
}

func TestIMAPFolderNamesDisambiguateDuplicates(t *testing.T) {
	folders := []eas.Folder{
		{ServerID: "inbox", DisplayName: "收件箱", Type: eas.FolderTypeInbox},
		{ServerID: "custom-inbox", DisplayName: "INBOX", Type: eas.FolderTypeUserMail},
		{ServerID: "a", DisplayName: "Archive", Type: eas.FolderTypeUserMail},
		{ServerID: "b", DisplayName: "Archive", Type: eas.FolderTypeUserMail},
	}

	got := imapFolderNames(folders)
	if got["inbox"] != "INBOX" {
		t.Fatalf("system inbox changed to %q", got["inbox"])
	}
	if got["custom-inbox"] == "INBOX" || !strings.HasPrefix(got["custom-inbox"], "INBOX [") {
		t.Fatalf("custom inbox was not disambiguated: %q", got["custom-inbox"])
	}
	if strings.EqualFold(got["a"], got["b"]) {
		t.Fatalf("duplicate custom names remain ambiguous: %q", got["a"])
	}
}

func TestMergeEmailChangePreservesMetadata(t *testing.T) {
	base := eas.EmailItem{
		ServerID:       "message",
		Subject:        "subject",
		From:           "sender@example.com",
		BodyPreview:    "preview",
		Read:           true,
		FlagStatus:     2,
		Attachments:    []eas.Attachment{{FileReference: "attachment"}},
		HasAttachments: true,
	}
	got := mergeEmailChange(base, eas.EmailItem{ServerID: "message", Read: false, ReadPresent: true})
	if got.Subject != base.Subject || got.From != base.From || got.BodyPreview != base.BodyPreview {
		t.Fatalf("sparse change discarded metadata: %+v", got)
	}
	if got.Read {
		t.Fatal("read=false change was not applied")
	}
	if len(got.Attachments) != 1 {
		t.Fatal("attachments were discarded")
	}
	if !emailMIMEContentEqual(base, got) {
		t.Fatal("flag-only change should not invalidate MIME content")
	}
	changedSubject := got
	changedSubject.Subject = "new subject"
	if emailMIMEContentEqual(got, changedSubject) {
		t.Fatal("subject change must invalidate MIME content")
	}

	flagOnly := mergeEmailChange(base, eas.EmailItem{
		ServerID:          "message",
		FlagStatus:        0,
		FlagStatusPresent: true,
	})
	if !flagOnly.Read {
		t.Fatal("sparse flag change cleared an omitted Read field")
	}
	if flagOnly.FlagStatus != 0 {
		t.Fatal("explicit flag clear was not applied")
	}
}
