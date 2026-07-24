package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

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
			ctx, cancel := context.WithCancel(context.Background())
			return &imapSession{d: d, conn: conn, ctx: ctx, cancel: cancel}, &imapserver.GreetingData{}, nil
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
	d      *imapd
	conn   *imapserver.Conn
	ctx    context.Context
	cancel context.CancelFunc

	selected string        // folderID
	snap     *mboxSnapshot // 选中时的快照
}

type mboxSnapshot struct {
	items       []eas.EmailItem
	uidForSID   map[string]uint32
	sidForUID   map[uint32]string
	uidValidity uint32
	deleted     map[string]bool // serverID → 已标记 \Deleted
}

func (sess *imapSession) Close() error {
	if sess.cancel != nil {
		sess.cancel()
	}
	return nil
}

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

	names := imapFolderNames(folders)
	type namedFolder struct {
		folder eas.Folder
		name   string
	}
	listed := make([]namedFolder, 0, len(names))
	for _, folder := range folders {
		if name := names[folder.ServerID]; name != "" {
			listed = append(listed, namedFolder{folder: folder, name: name})
		}
	}
	sort.Slice(listed, func(i, j int) bool {
		return strings.ToLower(listed[i].name) < strings.ToLower(listed[j].name)
	})

	for _, entry := range listed {
		name := entry.name
		match := len(patterns) == 0
		for _, p := range patterns {
			if imapserver.MatchList(name, '/', ref, p) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		attrs := []imap.MailboxAttr{imap.MailboxAttrHasNoChildren}
		for _, other := range listed {
			if strings.HasPrefix(other.name, name+"/") {
				attrs[0] = imap.MailboxAttrHasChildren
				break
			}
		}
		if specialUse := folderSpecialUse(entry.folder.Type); specialUse != "" {
			attrs = append(attrs, specialUse)
		}
		if err := w.WriteList(&imap.ListData{
			Mailbox: name,
			Attrs:   attrs,
			Delim:   '/',
		}); err != nil {
			return err
		}
	}
	return nil
}

func (sess *imapSession) Create(mailbox string, options *imap.CreateOptions) error {
	return errNotSupported("CREATE")
}
func (sess *imapSession) Delete(mailbox string) error { return errNotSupported("DELETE") }
func (sess *imapSession) Rename(mailbox, newName string, options *imap.RenameOptions) error {
	return errNotSupported("RENAME")
}
func (sess *imapSession) Subscribe(mailbox string) error   { return nil }
func (sess *imapSession) Unsubscribe(mailbox string) error { return nil }

func (sess *imapSession) Status(mailbox string, options *imap.StatusOptions) (*imap.StatusData, error) {
	folder, ok := sess.d.engine.findFolder(mailbox)
	if !ok {
		return nil, fmt.Errorf("不存在的文件夹: %s", mailbox)
	}
	ctx, cancel := context.WithTimeout(sess.ctx, 90*time.Second)
	defer cancel()
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
		Mailbox:     mailbox,
		NumMessages: uint32Ptr(uint32(len(items))),
		UIDValidity: st.FolderMeta[folder.ServerID].UIDValidity,
		UIDNext:     next,
		NumUnseen:   uint32Ptr(unseen),
	}, nil
}

func (sess *imapSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	folder, ok := sess.d.engine.findFolder(mailbox)
	if !ok {
		return nil, fmt.Errorf("不存在的文件夹: %s", mailbox)
	}
	ctx, cancel := context.WithTimeout(sess.ctx, 90*time.Second)
	defer cancel()
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
	sess.snap = &mboxSnapshot{items: items, uidForSID: uidForSID, sidForUID: sidForUID, uidValidity: fm.UIDValidity, deleted: snapshotDeleted(st, folder.ServerID)}

	return &imap.SelectData{
		Flags:             []imap.Flag{"\\Seen", "\\Flagged", "\\Deleted"},
		PermanentFlags:    []imap.Flag{"\\Seen", "\\Flagged", "\\Deleted"},
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
	ctx, cancel := context.WithTimeout(sess.ctx, 5*time.Minute)
	defer cancel()

	// 按 NumSet 类型解析消息序列
	return forEachItem(numSet, snap, func(seqNum uint32, item eas.EmailItem) error {
		uid := snap.uidForSID[item.ServerID]
		rw := w.CreateMessage(seqNum)
		rw.WriteUID(imap.UID(uid))

		if options.Flags {
			rw.WriteFlags(itemFlags(item, snap.deleted[item.ServerID]))
		}
		if options.InternalDate {
			rw.WriteInternalDate(item.DateReceived)
		}
		if options.Envelope {
			rw.WriteEnvelope(buildEnvelope(item))
		}
		needsMessage := options.RFC822Size || options.BodyStructure != nil || len(options.BodySection) > 0
		var plan *messagePlan
		if needsMessage {
			var err error
			plan, err = engine.prepareMessage(ctx, sess.selected, item.ServerID)
			if err != nil {
				return err
			}
		}
		if options.RFC822Size {
			size, err := plan.estimatedRFC822Size(ctx, engine)
			if err != nil {
				return err
			}
			rw.WriteRFC822Size(size)
		}
		if options.BodyStructure != nil {
			structure, err := plan.bodyStructure(ctx, engine)
			if err != nil {
				return err
			}
			rw.WriteBodyStructure(structure)
		}
		for _, bs := range options.BodySection {
			buf, err := plan.bodySection(ctx, engine, bs)
			if err != nil {
				return err
			}
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
}

// flagMutation 记录一次 STORE 变更的邮件定位与结果 flags，供响应写回使用。
type flagMutation struct {
	seqNum   uint32
	uid      uint32
	serverID string
	flags    []imap.Flag
}

// flagsFrom 按已读/删除标记构造 flags 列表。
func flagsFrom(read, deleted bool) []imap.Flag {
	var flags []imap.Flag
	if read {
		flags = append(flags, "\\Seen")
	}
	if deleted {
		flags = append(flags, "\\Deleted")
	}
	return flags
}

// applyFlagMutations 应用 STORE 的 \Seen/\Deleted 变更到本地 state，
// 返回需要回推服务器的已读变更与写回定位。失败即中止，已应用的不回滚。
// 结果 flags 在本函数内随变更一并算出，避免写回时二次加锁 O(n) 查找，
// 也避免查找落空写出空 flags（ZCode HIGH-1）。
func (sess *imapSession) applyFlagMutations(numSet imap.NumSet, storeFlags *imap.StoreFlags) ([]eas.EmailChange, []flagMutation, error) {
	snap := sess.snap
	st := sess.d.engine.st
	var readChanges []eas.EmailChange
	var mutated []flagMutation
	err := forEachItem(numSet, snap, func(seqNum uint32, item eas.EmailItem) error {
		serverID := item.ServerID
		read := item.Read
		deleted := snap.deleted[serverID]
		for _, f := range storeFlags.Flags {
			switch f {
			case "\\Seen":
				switch storeFlags.Op {
				case imap.StoreFlagsSet, imap.StoreFlagsAdd:
					if err := st.markRead(sess.selected, serverID); err != nil {
						return err
					}
					read = true
					readChanges = append(readChanges, eas.EmailChange{ServerID: serverID, Read: boolPtr(true)})
				case imap.StoreFlagsDel:
					if err := st.markUnread(sess.selected, serverID); err != nil {
						return err
					}
					read = false
					readChanges = append(readChanges, eas.EmailChange{ServerID: serverID, Read: boolPtr(false)})
				}
			case "\\Deleted":
				switch storeFlags.Op {
				case imap.StoreFlagsSet, imap.StoreFlagsAdd:
					if err := st.setDeleted(sess.selected, serverID, true); err != nil {
						return err
					}
					deleted = true
				case imap.StoreFlagsDel:
					if err := st.setDeleted(sess.selected, serverID, false); err != nil {
						return err
					}
					deleted = false
				}
			}
		}
		mutated = append(mutated, flagMutation{
			seqNum:   seqNum,
			uid:      snap.uidForSID[serverID],
			serverID: serverID,
			flags:    flagsFrom(read, deleted),
		})
		return nil
	})
	return readChanges, mutated, err
}

// pushReadChanges 批量回推已读状态到服务器。失败只记日志——本地状态已生效，
// 若服务器随后以未读覆盖，下次增量同步会自然收敛。
func (sess *imapSession) pushReadChanges(ctx context.Context, readChanges []eas.EmailChange) {
	if len(readChanges) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := sess.d.engine.c.ApplyEmailChanges(ctx, sess.selected, readChanges); err != nil {
		log.Printf("[imap] 已读状态回推失败（本地已生效）: %v", err)
	}
}

func (sess *imapSession) Store(w *imapserver.FetchWriter, numSet imap.NumSet, storeFlags *imap.StoreFlags, options *imap.StoreOptions) error {
	if sess.snap == nil {
		return fmt.Errorf("未选中文件夹")
	}
	readChanges, mutated, err := sess.applyFlagMutations(numSet, storeFlags)
	if err != nil {
		return err
	}

	for _, m := range mutated {
		rw := w.CreateMessage(m.seqNum)
		rw.WriteUID(imap.UID(m.uid))
		rw.WriteFlags(m.flags)
		if err := rw.Close(); err != nil {
			return err
		}
	}

	sess.pushReadChanges(sess.ctx, readChanges)
	return nil
}

func (sess *imapSession) Search(numKind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	if sess.snap == nil {
		return nil, fmt.Errorf("未选中文件夹")
	}
	snap := sess.snap
	folderID := sess.selected
	sc := &searchContext{
		snap: snap,
		bodyText: func(serverID string) (string, bool) {
			return peekCachedBodyText(folderID, serverID)
		},
	}
	matches := filterSearch(sc, criteria)
	data := &imap.SearchData{}
	switch numKind {
	case imapserver.NumKindSeq:
		var seqSet imap.SeqSet
		for _, i := range matches {
			seqSet.AddNum(uint32(i + 1))
		}
		data.All = seqSet
	case imapserver.NumKindUID:
		var uidSet imap.UIDSet
		for _, i := range matches {
			uidSet.AddNum(imap.UID(snap.uidForSID[snap.items[i].ServerID]))
		}
		data.All = uidSet
	}
	return data, nil
}

// peekCachedBodyText 只读缓存获取邮件正文文本（无缓存返回 ok=false）。
// SEARCH 没有 ctx 可用，正文搜索不触发网络拉取（否则一次正文搜索可能
// 引发数百封逐个拉取）。Apple Mail 的全文搜索走本地索引，不受此限。
func peekCachedBodyText(folderID, serverID string) (string, bool) {
	for _, path := range []string{
		messageFullMIMEPath(folderID, serverID),
		messageRawMIMEPath(folderID, serverID),
	} {
		if data, err := readCacheFile(path); err == nil && validRFC822(data) {
			return extractSearchableText(data), true
		}
	}
	if data, err := readCacheFile(messageMetadataPath(folderID, serverID)); err == nil {
		var cached cachedMessageMetadata
		if json.Unmarshal(data, &cached) == nil && cached.PlainBody != "" {
			return cached.PlainBody, true
		}
	}
	return "", false
}

// moveItemsStatusOK 是 MS-ASCMD MoveItems 的成功状态码（注意：不是 eas.StatusOK=1）。
const moveItemsStatusOK = 3

// expungeMarked 把已标记 \Deleted（且若给定在 uids 集合内）的邮件移到服务器垃圾箱，
// 清理本地 state 与缓存，返回被移除邮件的 seqNum 列表供 EXPUNGE 响应。
func (sess *imapSession) expungeMarked(uids *imap.UIDSet) ([]uint32, error) {
	snap := sess.snap
	engine := sess.d.engine

	type victim struct {
		serverID string
		seq      uint32
	}
	var victims []victim
	for i, it := range snap.items {
		if !snap.deleted[it.ServerID] {
			continue
		}
		uid := snap.uidForSID[it.ServerID]
		if uids != nil && !uids.Contains(imap.UID(uid)) {
			continue
		}
		victims = append(victims, victim{serverID: it.ServerID, seq: uint32(i + 1)})
	}
	if len(victims) == 0 {
		return nil, nil
	}

	trashID, err := engine.trashFolderID()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(victims))
	for _, v := range victims {
		ids = append(ids, v.serverID)
	}
	ctx, cancel := context.WithTimeout(sess.ctx, 60*time.Second)
	defer cancel()
	results, err := engine.c.MoveItems(ctx, sess.selected, trashID, ids)
	if err != nil {
		return nil, fmt.Errorf("服务器删除失败: %w", err)
	}

	// 按 SrcServerID 筛成功项（EAS 删除=移到已删除文件夹，可恢复）
	dstIDOf := map[string]string{}
	for _, r := range results {
		if r.Status == moveItemsStatusOK {
			dstIDOf[r.SrcServerID] = r.DstServerID
		}
	}
	var moved []string
	var seqs []uint32
	var movedItems []eas.EmailItem
	for _, v := range victims {
		dstID, ok := dstIDOf[v.serverID]
		if !ok {
			continue
		}
		moved = append(moved, v.serverID)
		seqs = append(seqs, v.seq)
		if dstID == "" {
			dstID = v.serverID // 服务器未分配新 ID 时沿用原 ID
		}
		it := snap.items[v.seq-1]
		it.ServerID = dstID // 服务器在目标文件夹分配的新 ID（Coremail 会改）
		movedItems = append(movedItems, it)
	}
	if len(moved) == 0 {
		return nil, fmt.Errorf("服务器拒绝删除全部 %d 封邮件", len(ids))
	}
	if err := engine.st.removeItems(sess.selected, moved...); err != nil {
		return nil, err
	}
	// EAS 不回显本设备引起的变更：目标文件夹的增量同步永远看不到移入的邮件，
	// 必须自己落进本地 state，否则垃圾箱在 Mail 里永远缺这些邮件。
	if err := engine.st.addMovedItems(trashID, movedItems); err != nil {
		return nil, err
	}
	engine.invalidateMessageCache(sess.selected, moved...)
	sess.refreshSnapshot()
	sess.d.broadcast(sess.selected)
	// 后台补一次双向同步，让垃圾箱尽快反映新邮件
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = engine.syncMail(ctx, sess.selected)
		_ = engine.syncMail(ctx, trashID)
	}()
	return seqs, nil
}

func (sess *imapSession) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	if sess.snap == nil {
		return fmt.Errorf("未选中文件夹")
	}
	seqs, err := sess.expungeMarked(uids)
	if err != nil {
		return err
	}
	for _, seq := range seqs {
		if err := w.WriteExpunge(seq); err != nil {
			return err
		}
	}
	return nil
}

func (sess *imapSession) Copy(numSet imap.NumSet, dest string) (*imap.CopyData, error) {
	if sess.snap == nil {
		return nil, fmt.Errorf("未选中文件夹")
	}
	snap := sess.snap
	engine := sess.d.engine

	dst, ok := engine.findFolder(dest)
	if !ok {
		return nil, fmt.Errorf("不存在的文件夹: %s", dest)
	}
	var ids []string
	if err := forEachItem(numSet, snap, func(seqNum uint32, item eas.EmailItem) error {
		ids = append(ids, item.ServerID)
		return nil
	}); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// EAS 无服务端复制语义，COPY 以 MoveItems 实现（与 DavMail 一致）：
	// 邮件移入目标文件夹后不再留在原文件夹。
	ctx, cancel := context.WithTimeout(sess.ctx, 60*time.Second)
	defer cancel()
	results, err := engine.c.MoveItems(ctx, sess.selected, dst.ServerID, ids)
	if err != nil {
		return nil, fmt.Errorf("服务器移动失败: %w", err)
	}
	dstIDOf := map[string]string{}
	for _, r := range results {
		if r.Status == moveItemsStatusOK {
			dstIDOf[r.SrcServerID] = r.DstServerID
		}
	}
	itemByID := map[string]eas.EmailItem{}
	for _, it := range snap.items {
		itemByID[it.ServerID] = it
	}
	var moved []string
	var movedItems []eas.EmailItem
	for _, id := range ids {
		dstID, ok := dstIDOf[id]
		if !ok {
			continue
		}
		moved = append(moved, id)
		if dstID == "" {
			dstID = id
		}
		it := itemByID[id]
		it.ServerID = dstID
		movedItems = append(movedItems, it)
	}
	if len(moved) > 0 {
		if err := engine.st.removeItems(sess.selected, moved...); err != nil {
			return nil, err
		}
		// EAS 不回显本设备引起的变更：目标文件夹靠自己落库才能看到移入的邮件。
		if err := engine.st.addMovedItems(dst.ServerID, movedItems); err != nil {
			return nil, err
		}
		engine.invalidateMessageCache(sess.selected, moved...)
		sess.refreshSnapshot()
		sess.d.broadcast(sess.selected)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			_ = engine.syncMail(ctx, sess.selected)
			_ = engine.syncMail(ctx, dst.ServerID)
		}()
	}
	if len(moved) < len(ids) {
		return nil, fmt.Errorf("部分邮件移动失败（%d/%d 成功）", len(moved), len(ids))
	}
	// 未广告 UIDPLUS，不返回目标 UID 映射，客户端会自行重新同步
	return nil, nil
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
	sess.snap = &mboxSnapshot{items: items, uidForSID: uidForSID, sidForUID: sidForUID, uidValidity: fm.UIDValidity, deleted: snapshotDeleted(st, sess.selected)}
}

// snapshotDeleted 提取文件夹的 \Deleted 标记快照。调用方需持有 st.mu。
func snapshotDeleted(st *diskState, folderID string) map[string]bool {
	out := map[string]bool{}
	for id := range st.Deleted[folderID] {
		out[id] = true
	}
	return out
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

func folderSpecialUse(folderType eas.FolderType) imap.MailboxAttr {
	switch folderType {
	case eas.FolderTypeSentItems:
		return imap.MailboxAttrSent
	case eas.FolderTypeDrafts:
		return imap.MailboxAttrDrafts
	case eas.FolderTypeDeletedItems:
		return imap.MailboxAttrTrash
	default:
		return ""
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

func itemFlags(item eas.EmailItem, deleted bool) []imap.Flag {
	return flagsFrom(item.Read, deleted)
}

func boolPtr(v bool) *bool { return &v }

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
		Text: fmt.Sprintf("%s 暂不支持（eas-bridge v1）", op),
	}
}

func uint32Ptr(v uint32) *uint32 { return &v }
