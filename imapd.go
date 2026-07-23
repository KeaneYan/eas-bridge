package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/hstern/go-activesync/eas"
)

// imapd 是 IMAP 服务器实现。
type imapd struct {
	engine *syncEngine
	subsMu sync.Mutex
	subs   map[chan string]struct{} // IDLE 会话订阅表（fan-out 广播）
}

func newIMAPD(engine *syncEngine) *imapd {
	return &imapd{engine: engine, subs: map[chan string]struct{}{}}
}

// subscribe 注册一个 IDLE 通知通道，返回退订函数。
func (d *imapd) subscribe() (chan string, func()) {
	ch := make(chan string, 16)
	d.subsMu.Lock()
	d.subs[ch] = struct{}{}
	d.subsMu.Unlock()
	return ch, func() {
		d.subsMu.Lock()
		delete(d.subs, ch)
		d.subsMu.Unlock()
	}
}

// broadcast 非阻塞地向所有 IDLE 会话广播文件夹变更。
func (d *imapd) broadcast(folderID string) {
	d.subsMu.Lock()
	defer d.subsMu.Unlock()
	for ch := range d.subs {
		select {
		case ch <- folderID:
		default:
		}
	}
}

// Serve 启动 IMAP 监听（阻塞）。
func (d *imapd) Serve(addr string) error {
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &imapSession{d: d, conn: conn}, &imapserver.GreetingData{}, nil
		},
		InsecureAuth: true, // 仅 localhost 监听，无需 TLS
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("[imapd] 监听 %s", addr)
	return srv.Serve(ln)
}

// ---------- IMAP Session ----------

type imapSession struct {
	d    *imapd
	conn *imapserver.Conn

	selected string         // folderID
	snap     *mboxSnapshot  // 选中时的快照
}

type mboxSnapshot struct {
	items       []eas.EmailItem
	uidForSID   map[string]uint32 // serverID→UID
	sidForUID   map[uint32]string // UID→serverID
	uidValidity uint32
}

func (sess *imapSession) Close() error { return nil }

func (sess *imapSession) Login(username, password string) error {
	if username != sess.d.engine.cfg.User || password != sess.d.engine.cfg.Password {
		return fmt.Errorf("认证失败")
	}
	return nil
}

func (sess *imapSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	st := sess.d.engine.st
	st.mu.Lock()
	folders := append([]eas.Folder(nil), st.Folders...)
	st.mu.Unlock()

	for _, f := range folders {
		name := easFolderToIMAPName(f)
		match := len(patterns) == 0
		for _, p := range patterns {
			if p == "*" || p == "%" || strings.EqualFold(p, name) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if err := w.WriteList(&imap.ListData{
			Mailbox: name,
			Attrs:   []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
			Delim:   '/',
		}); err != nil {
			return err
		}
	}
	return nil
}

func (sess *imapSession) Create(mailbox string, options *imap.CreateOptions) error { return errNotSupported("CREATE") }
func (sess *imapSession) Delete(mailbox string) error { return errNotSupported("DELETE") }
func (sess *imapSession) Rename(mailbox, newName string, options *imap.RenameOptions) error { return errNotSupported("RENAME") }
func (sess *imapSession) Subscribe(mailbox string) error   { return nil }
func (sess *imapSession) Unsubscribe(mailbox string) error { return nil }

func (sess *imapSession) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	folder, ok := sess.d.engine.findFolder(mailbox)
	if !ok {
		return nil, fmt.Errorf("不存在的文件夹: %s", mailbox)
	}
	ctx := context.Background()
	if err := sess.d.engine.syncMail(ctx, folder.ServerID); err != nil {
		return nil, fmt.Errorf("同步失败: %w", err)
	}
	st := sess.d.engine.st
	st.mu.Lock()
	defer st.mu.Unlock()
	items := st.Items[folder.ServerID]
	var unseen uint32
	for _, it := range items {
		if !it.Read {
			unseen++
		}
	}
	next := imap.UID(st.FolderMeta[folder.ServerID].NextUID)
	return &imap.StatusData{
		Mailbox:       mailbox,
		NumMessages:   uint32Ptr(uint32(len(items))),
		UIDValidity:   st.FolderMeta[folder.ServerID].UIDValidity,
		UIDNext:       next,
		NumUnseen:     uint32Ptr(unseen),
	}, nil
}

func (sess *imapSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	folder, ok := sess.d.engine.findFolder(mailbox)
	if !ok {
		return nil, fmt.Errorf("不存在的文件夹: %s", mailbox)
	}
	ctx := context.Background()
	if err := sess.d.engine.syncMail(ctx, folder.ServerID); err != nil {
		return nil, fmt.Errorf("同步失败: %w", err)
	}
	st := sess.d.engine.st
	st.mu.Lock()
	defer st.mu.Unlock()
	items := st.Items[folder.ServerID]
	fm := st.FolderMeta[folder.ServerID]

	// 构建双向 UID 映射快照
	uidForSID := map[string]uint32{}
	sidForUID := map[uint32]string{}
	for _, e := range st.UIDs[folder.ServerID] {
		uidForSID[e.ServerID] = e.UID
		sidForUID[e.UID] = e.ServerID
	}

	var firstUnseenSeq uint32
	for i, it := range items {
		if !it.Read && firstUnseenSeq == 0 {
			firstUnseenSeq = uint32(i + 1)
		}
	}
	sess.selected = folder.ServerID
	sess.snap = &mboxSnapshot{items: items, uidForSID: uidForSID, sidForUID: sidForUID, uidValidity: fm.UIDValidity}

	return &imap.SelectData{
		Flags:             []imap.Flag{"\\Seen", "\\Flagged"},
		PermanentFlags:    []imap.Flag{"\\Seen", "\\Flagged"},
		NumMessages:       uint32(len(items)),
		FirstUnseenSeqNum: firstUnseenSeq,
		UIDValidity:       fm.UIDValidity,
		UIDNext:           imap.UID(fm.NextUID),
	}, nil
}

func (sess *imapSession) Unselect() error {
	sess.selected = ""
	sess.snap = nil
	return nil
}

func (sess *imapSession) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	return nil, errNotSupported("APPEND")
}

func (sess *imapSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	if sess.snap == nil {
		return fmt.Errorf("未选中文件夹")
	}
	snap := sess.snap
	engine := sess.d.engine

	// 按 NumSet 类型解析消息序列
	forEachItem(numSet, snap, func(seqNum uint32, item eas.EmailItem) error {
		uid := snap.uidForSID[item.ServerID]
		rw := w.CreateMessage(seqNum)
		rw.WriteUID(imap.UID(uid))

		if options.Flags {
			rw.WriteFlags(itemFlags(item))
		}
		if options.InternalDate {
			rw.WriteInternalDate(item.DateReceived)
		}
		if options.Envelope {
			rw.WriteEnvelope(buildEnvelope(item))
		}
		if options.RFC822Size {
			mimeBytes, _ := engine.fetchMIME(context.Background(), sess.selected, item.ServerID)
			rw.WriteRFC822Size(int64(len(mimeBytes)))
		}
		if options.BodyStructure != nil {
			mimeBytes, err := engine.fetchMIME(context.Background(), sess.selected, item.ServerID)
			if err != nil {
				return err
			}
			rw.WriteBodyStructure(imapserver.ExtractBodyStructure(bytes.NewReader(mimeBytes)))
		}
		for _, bs := range options.BodySection {
			mimeBytes, err := engine.fetchMIME(context.Background(), sess.selected, item.ServerID)
			if err != nil {
				return err
			}
			buf := imapserver.ExtractBodySection(bytes.NewReader(mimeBytes), bs)
			wc := rw.WriteBodySection(bs, int64(len(buf)))
			if _, err := wc.Write(buf); err != nil {
				return err
			}
			if err := wc.Close(); err != nil {
				return err
			}
		}
		return rw.Close()
	})
	return nil
}

func (sess *imapSession) Store(w *imapserver.FetchWriter, numSet imap.NumSet, storeFlags *imap.StoreFlags, options *imap.StoreOptions) error {
	if sess.snap == nil {
		return fmt.Errorf("未选中文件夹")
	}
	snap := sess.snap
	st := sess.d.engine.st

	return forEachItem(numSet, snap, func(seqNum uint32, item eas.EmailItem) error {
		serverID := item.ServerID
		uid := snap.uidForSID[serverID]
		for _, f := range storeFlags.Flags {
			if f == "\\Seen" {
				switch storeFlags.Op {
				case imap.StoreFlagsSet, imap.StoreFlagsAdd:
					st.markRead(sess.selected, serverID)
				case imap.StoreFlagsDel:
					st.markUnread(sess.selected, serverID)
				}
			}
		}
		rw := w.CreateMessage(seqNum)
		rw.WriteUID(imap.UID(uid))
		// 读回最新 flags（markRead/Unread 已改内存）
		st.mu.Lock()
		var flags []imap.Flag
		for _, it := range st.Items[sess.selected] {
			if it.ServerID == serverID {
				flags = itemFlags(it)
				break
			}
		}
		st.mu.Unlock()
		rw.WriteFlags(flags)
		return rw.Close()
	})
}

func (sess *imapSession) Search(numKind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	if sess.snap == nil {
		return nil, fmt.Errorf("未选中文件夹")
	}
	snap := sess.snap
	// 检查是否有 "NOT \Seen" 条件（未读过滤）
	unseenOnly := false
	for _, f := range criteria.NotFlag {
		if f == "\\Seen" {
			unseenOnly = true
			break
		}
	}
	var allIDs imap.UIDSet
	for _, it := range snap.items {
		if unseenOnly && it.Read {
			continue
		}
		uid := snap.uidForSID[it.ServerID]
		allIDs.AddNum(imap.UID(uid))
	}
	data := &imap.SearchData{}
	switch numKind {
	case imapserver.NumKindSeq:
		var seqSet imap.SeqSet
		for i, it := range snap.items {
			if unseenOnly && it.Read { continue }
			seqSet.AddNum(uint32(i+1))
		}
		data.All = seqSet
	case imapserver.NumKindUID:
		var uidSet imap.UIDSet
		for _, it := range snap.items {
			if unseenOnly && it.Read { continue }
			uidSet.AddNum(imap.UID(snap.uidForSID[it.ServerID]))
		}
		data.All = uidSet
	}
	return data, nil
}

func (sess *imapSession) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	return errNotSupported("EXPUNGE")
}

func (sess *imapSession) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	return nil, errNotSupported("COPY")
}

func (sess *imapSession) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if sess.selected == "" || sess.snap == nil {
		return nil
	}
	oldCount := uint32(len(sess.snap.items))
	sess.refreshSnapshot()
	if newCount := uint32(len(sess.snap.items)); newCount > oldCount {
		w.WriteNumMessages(newCount)
	}
	return nil
}

// refreshSnapshot 从最新 state 重建选中文件夹快照。
// B1 修复：poller 追加新邮件后必须刷新快照，否则客户端按新 seqnum FETCH 永远取不到。
func (sess *imapSession) refreshSnapshot() {
	if sess.selected == "" {
		return
	}
	st := sess.d.engine.st
	st.mu.Lock()
	defer st.mu.Unlock()
	items := st.Items[sess.selected]
	fm := st.FolderMeta[sess.selected]
	uidForSID := map[string]uint32{}
	sidForUID := map[uint32]string{}
	for _, e := range st.UIDs[sess.selected] {
		uidForSID[e.ServerID] = e.UID
		sidForUID[e.UID] = e.ServerID
	}
	sess.snap = &mboxSnapshot{items: items, uidForSID: uidForSID, sidForUID: sidForUID, uidValidity: fm.UIDValidity}
}

func (sess *imapSession) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	notify, unsub := sess.d.subscribe()
	defer unsub()
	for {
		select {
		case <-stop:
			return nil
		case folderID := <-notify:
			if sess.selected == "" || sess.snap == nil {
				continue
			}
			_ = folderID // v1 只轮询收件箱，任何变更都刷新当前选中文件夹
			oldCount := uint32(len(sess.snap.items))
			sess.refreshSnapshot()
			if newCount := uint32(len(sess.snap.items)); newCount > oldCount {
				w.WriteNumMessages(newCount)
			}
		}
	}
}

// ---------- 辅助 ----------

func easFolderToIMAPName(f eas.Folder) string {
	switch f.Type {
	case eas.FolderTypeInbox:
		return "INBOX"
	case eas.FolderTypeSentItems:
		return "Sent"
	case eas.FolderTypeDrafts:
		return "Drafts"
	case eas.FolderTypeDeletedItems:
		return "Trash"
	default:
		return f.DisplayName
	}
}

func buildEnvelope(item eas.EmailItem) *imap.Envelope {
	return &imap.Envelope{
		Date:    item.DateReceived,
		Subject: item.Subject,
		From:    parseEASAddrs(item.From),
		Sender:  parseEASAddrs(item.Sender),
		ReplyTo: parseEASAddrs(item.ReplyTo),
		To:      parseEASAddrs(item.To),
		Cc:      parseEASAddrs(item.Cc),
		Bcc:     parseEASAddrs(item.Bcc),
	}
}

func parseEASAddrs(raw string) []imap.Address {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []imap.Address
	for _, p := range strings.Split(raw, ";") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		a := imap.Address{}
		if i := strings.LastIndex(p, "<"); i >= 0 && strings.HasSuffix(p, ">") {
			a.Name = strings.Trim(p[:i], " \"")
			email := p[i+1 : len(p)-1]
			if at := strings.LastIndex(email, "@"); at >= 0 {
				a.Mailbox = email[:at]
				a.Host = email[at+1:]
			}
		} else if at := strings.LastIndex(p, "@"); at >= 0 {
			a.Mailbox = p[:at]
			a.Host = p[at+1:]
		} else {
			a.Mailbox = p
		}
		out = append(out, a)
	}
	return out
}

func itemFlags(item eas.EmailItem) []imap.Flag {
	if item.Read {
		return []imap.Flag{"\\Seen"}
	}
	return nil
}

// forEachItem 按 NumSet 遍历快照中的消息，调用 fn(seqNum, item)。
func forEachItem(numSet imap.NumSet, snap *mboxSnapshot, fn func(uint32, eas.EmailItem) error) error {
	switch ns := numSet.(type) {
	case imap.SeqSet:
		for i, item := range snap.items {
			seq := uint32(i + 1)
			if ns.Contains(seq) {
				if err := fn(seq, item); err != nil {
					return err
				}
			}
		}
	case imap.UIDSet:
		for i, item := range snap.items {
			uid := imap.UID(snap.uidForSID[item.ServerID])
			if ns.Contains(uid) {
				if err := fn(uint32(i+1), item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func errNotSupported(op string) error {
	return &imap.Error{
		Type: imap.StatusResponseTypeNo,
		Text: fmt.Sprintf("%s 暂不支持（imeg-eas v1）", op),
	}
}

func uint32Ptr(v uint32) *uint32 { return &v }
