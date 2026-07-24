package main

import (
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/hstern/go-activesync/eas"
)

// newSearchFixture 构造 4 封特征各异的邮件快照。
func newSearchFixture() *searchContext {
	items := []eas.EmailItem{
		{ServerID: "m1", Subject: "季度财报评审", From: "张三 <zhangsan@webank.com>",
			To: "keaneyan@webank.com", DateReceived: time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC),
			Read: true, BodyEstimatedSize: 5000},
		{ServerID: "m2", Subject: "团建通知", From: "HR <hr@webank.com>",
			To: "all@webank.com", Cc: "keaneyan@webank.com",
			DateReceived: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC),
			Read: false, BodyEstimatedSize: 2000, FlagStatus: 2},
		{ServerID: "m3", Subject: "RE: 季度财报评审", From: "李四 <lisi@webank.com>",
			To: "zhangsan@webank.com", DateReceived: time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC),
			Read: false, BodyEstimatedSize: 8000},
		{ServerID: "m4", Subject: " lunch ", From: "王五 <wangwu@webank.com>",
			To: "keaneyan@webank.com", DateReceived: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
			Read: true, BodyEstimatedSize: 500},
	}
	snap := &mboxSnapshot{
		items:     items,
		uidForSID: map[string]uint32{"m1": 1, "m2": 2, "m3": 3, "m4": 4},
		deleted:   map[string]bool{"m4": true},
	}
	return &searchContext{
		snap: snap,
		bodyText: func(sid string) (string, bool) {
			bodies := map[string]string{
				"m1": "本季度营收同比增长，详见附件报表",
				"m3": "同意你的看法，周五前定稿",
			}
			b, ok := bodies[sid]
			return b, ok
		},
	}
}

func matchSIDs(sc *searchContext, c *imap.SearchCriteria) []string {
	var out []string
	for _, i := range filterSearch(sc, c) {
		out = append(out, sc.snap.items[i].ServerID)
	}
	return out
}

func assertSIDs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSearchAll(t *testing.T) {
	assertSIDs(t, matchSIDs(newSearchFixture(), &imap.SearchCriteria{}), "m1", "m2", "m3", "m4")
}

func TestSearchSubject(t *testing.T) {
	sc := newSearchFixture()
	c := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{
		{Key: "Subject", Value: "财报"},
	}}
	assertSIDs(t, matchSIDs(sc, c), "m1", "m3")
}

func TestSearchFrom(t *testing.T) {
	sc := newSearchFixture()
	c := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{
		{Key: "From", Value: "hr@"},
	}}
	assertSIDs(t, matchSIDs(sc, c), "m2")
}

func TestSearchToAndCc(t *testing.T) {
	sc := newSearchFixture()
	c := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{
		{Key: "Cc", Value: "keaneyan"},
	}}
	assertSIDs(t, matchSIDs(sc, c), "m2")
}

func TestSearchHeaderExists(t *testing.T) {
	sc := newSearchFixture()
	c := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{
		{Key: "Cc", Value: ""}, // 空值 = 存在性检查
	}}
	assertSIDs(t, matchSIDs(sc, c), "m2")
}

func TestSearchFlags(t *testing.T) {
	sc := newSearchFixture()
	// 未读
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		NotFlag: []imap.Flag{"\\Seen"},
	}), "m2", "m3")
	// 已读
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		Flag: []imap.Flag{"\\Seen"},
	}), "m1", "m4")
	// 星标
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		Flag: []imap.Flag{"\\Flagged"},
	}), "m2")
	// 已删除标记
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		Flag: []imap.Flag{"\\Deleted"},
	}), "m4")
	// 未删除
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		NotFlag: []imap.Flag{"\\Deleted"},
	}), "m1", "m2", "m3")
}

func TestSearchDates(t *testing.T) {
	sc := newSearchFixture()
	// SINCE 7-22（含当天）
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		Since: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
	}), "m2", "m3", "m4")
	// BEFORE 7-22（不含当天）
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		Before: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
	}), "m1")
	// 区间
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{
		Since:  time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		Before: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}), "m2", "m3")
}

func TestSearchSize(t *testing.T) {
	sc := newSearchFixture()
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{Larger: 4000}), "m1", "m3")
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{Smaller: 2000}), "m4")
}

func TestSearchBodyCachedOnly(t *testing.T) {
	sc := newSearchFixture()
	// m1 缓存里有"营收"
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{Body: []string{"营收"}}), "m1")
	// m2/m4 无缓存，即使搜常见词也不命中
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{Body: []string{"通知"}}))
}

func TestSearchText(t *testing.T) {
	sc := newSearchFixture()
	// TEXT 命中头部
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{Text: []string{"团建"}}), "m2")
	// TEXT 命中缓存正文
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{Text: []string{"定稿"}}), "m3")
}

func TestSearchNot(t *testing.T) {
	sc := newSearchFixture()
	c := &imap.SearchCriteria{Not: []imap.SearchCriteria{{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: "财报"}},
	}}}
	assertSIDs(t, matchSIDs(sc, c), "m2", "m4")
}

func TestSearchOr(t *testing.T) {
	sc := newSearchFixture()
	c := &imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{
		{Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: "hr@"}}},
		{Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: "wangwu"}}},
	}}}
	assertSIDs(t, matchSIDs(sc, c), "m2", "m4")
}

func TestSearchUIDAndSeqSets(t *testing.T) {
	sc := newSearchFixture()
	// UID 2,4
	var us imap.UIDSet
	us.AddNum(2)
	us.AddNum(4)
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{UID: []imap.UIDSet{us}}), "m2", "m4")
	// 序号 1:2
	var ss imap.SeqSet
	ss.AddRange(1, 2)
	assertSIDs(t, matchSIDs(sc, &imap.SearchCriteria{SeqNum: []imap.SeqSet{ss}}), "m1", "m2")
}

func TestSearchCombined(t *testing.T) {
	sc := newSearchFixture()
	// 未读 + 主题含"财报" + 7-21 之后 → m3
	c := &imap.SearchCriteria{
		NotFlag: []imap.Flag{"\\Seen"},
		Header:  []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: "财报"}},
		Since:   time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC),
	}
	assertSIDs(t, matchSIDs(sc, c), "m3")
}

func TestExtractSearchableText(t *testing.T) {
	raw := []byte("Subject: =?UTF-8?B?5a2m5Lmg6K6h5YiS?=\r\n" +
		"From: a@b.com\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"hello=20world=E4=B8=AD=E6=96=87\r\n")
	text := extractSearchableText(raw)
	if !containsFold(text, "hello world中文") {
		t.Fatalf("QP 正文未解码: %q", text)
	}
	if !containsFold(text, "学习计划") {
		t.Fatalf("编码头未解码: %q", text)
	}
	// HTML 去标签
	rawHTML := []byte("Content-Type: text/html\r\n\r\n<p>开会<b>通知</b></p>")
	if got := extractSearchableText(rawHTML); !containsFold(got, "开会通知") || containsFold(got, "<b>") {
		t.Fatalf("HTML 提取: %q", got)
	}
}

// TestExtractNestedMultipart：嵌套 multipart（mixed→alternative→plain/html）
// 的正文必须被递归提取（ZCode M1 修复验证）。
func TestExtractNestedMultipart(t *testing.T) {
	raw := []byte("Content-Type: multipart/mixed; boundary=OUT\r\n" +
		"\r\n" +
		"--OUT\r\n" +
		"Content-Type: multipart/alternative; boundary=IN\r\n" +
		"\r\n" +
		"--IN\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"嵌套纯文本正文关键词甲\r\n" +
		"--IN\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>嵌套<b>网页</b>正文关键词乙</p>\r\n" +
		"--IN--\r\n" +
		"--OUT\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQ=\r\n" +
		"--OUT--\r\n")
	text := extractSearchableText(raw)
	if !containsFold(text, "关键词甲") {
		t.Fatalf("嵌套 plain 未提取: %q", text)
	}
	if !containsFold(text, "嵌套网页正文关键词乙") {
		t.Fatalf("嵌套 html 未提取: %q", text)
	}
	if containsFold(text, "JVBERi") {
		t.Fatalf("附件不应进入正文: %q", text)
	}
}
