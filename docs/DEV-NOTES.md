# 开发者避坑指南（DEV NOTES）

> 本文档沉淀 eas-bridge 开发过程中**实测踩过的坑**，按"服务器怪癖 / 协议实战 / 架构不变量 / 测试纪律"分类。
> 每条都有真实事故或实测背书。改源码前请通读——重复踩坑的代价远高于阅读成本。

## 一、Coremail 服务器怪癖（全部实测确认）

### 1. FilterType=0 是陷阱
EAS 协议里 FilterType 合法值只有 1-7（1天~6个月），**0 未定义**。fork 默认发 0，Coremail 按默认短窗口处理（实测只回最近 ~2 周，INBOX 全量仅 33 封）。
**必须显式 `DateFilter: eas.FilterSixMonth`**（协议最大窗口）——修复后 INBOX 423 封、日历 765 事件。超过 6 个月的邮件走 Sync 协议拉不到，是协议上限不是 bug。

### 2. Sync 必须按 MoreAvailable 翻页
`WindowSize=200` 单页最多 200 封（实测 Coremail 有时只给 100）。不翻页则大文件夹只见最新一页。`syncMailOnce`/`syncCalendarOnce` 都有翻页循环，synckey 逐页落盘断点续拉。

### 3. MoveItems 成功状态码是 3，不是 1
`eas.StatusOK=1` 是 **Sync** 的成功码；**MoveItems 的成功码是 3**（MS-ASCMD Status 表）。代码里有 `moveItemsStatusOK = 3` 常量。
曾按 1 判断把成功的移动误判成失败 → 本地 state 与服务器分叉（邮件在客户端里"幽灵存在"）。**误判失败≠没发生——服务器已经动了。**

### 4. EAS 不回显本设备引起的变更
MoveItems / CreateEvent 后，源和目标文件夹的增量 Sync **都永远不会**提到这些对象（fork AGENTS.md + 实测双重确认）。
因此桥必须自行落本地 state：邮件移动 = 移出侧 `removeItems` + 移入侧 `addMovedItems`（用 `MoveItemResult.DstServerID`，空则沿用原 ID）；日历写 = `upsertEvent`/`deleteEvent`。漏掉的代价：目标文件夹在客户端永远缺被移入的对象。

### 5. 失效 synckey 返回 Status 5 而非规范的 3
Coremail 不认可旧 synckey 时回 `Status 5 (ServerError)`，而 fork 只对 `Status 3 (InvalidSyncKey)` 自动重置。桥层已加自愈：遇 Status 5 → `resetSyncKey` + 全量重拉一次，再失败才报真实故障（syncMailOnce / syncCalendarOnce）。

### 6. 设备合作关系数上限 ~10（2026-07-24 事故）
当日为排查日历问题用一次性脚本建了 ~10 个 DeviceID 后，**整个账号的增量 Sync 全部 Status 5——连刚签发的新 key 都立即失效**，只有 key=0 全量引导正常。daemon 陷入"重置→全量重拉→再 5"死循环。
- **恢复方法**：网页端邮箱 → 设置 → 移动设备，删除旧/未知设备。
- **铁律：任何 probe/诊断脚本必须复用固定 DeviceID**（如 `easprobe`），绝不每次新建。
- 排查路径：新 DeviceID + bootstrap 正常 → 同会话第二次增量也 5 → 账号级同步状态配额满。

### 7. 日历上行 Sync 全拒（服务器策略）
对日历文件夹的上行 Sync 命令（Add/Change/Delete）一律回 Status 5。证据链：Add 和 Change 都拒 × 协议版本 12.1/14.0/14.1 全拒 × ApplicationData 顺序/GetChanges/DeletesAsMoves/Class 变体全试过；**对照组：同账号邮件上行（ApplyEmailChanges）Status 1 正常**。
结论：日历由会议系统/网页端主控，EAS 只读是服务器策略，任何 EAS 客户端都一样。代码保留完整写路径 + 403 优雅降级（calWriteErr），策略放开即自动可用。

### 8. 其他
- **纯文本邮件** FetchEmail `BodyTypeMIME` 返回空 BodyMIME → 降级用 EAS 元数据构造 RFC822（`constructRFC822`）。
- **HTML 邮件原始 MIME 常不可用** → 降级请求 HTML+纯文本组装 multipart/alternative（PR #1）。
- **EAS Sync Change 是稀疏增量**：常见只有 Read/Flag 变更，merge 时必须保留未携带字段（`ReadPresent`/`FlagStatusPresent`）。
- **ItemOperations Fetch 可能不回原始 MIME**。
- 轮询偶发 EOF（长连接被掐）：记日志继续，不崩。
- 16.x 协议版本不可用：fork 的 wbxml codepage 缺 16.x 的 AirSyncBase 标签，Sync 响应都解不了。

## 二、EAS 协议实战

### ApplicationData 子元素线序：对齐 Z-Push
`buildEventApp`（fork calendar.go）按 **Z-Push SyncAppointment mapping 数组**的线序输出：
`TimeZone→DTStamp→StartTime→Subject→UID→Location→EndTime→Recurrence→Sensitivity→BusyStatus→AllDayEvent→Reminder→Attendees→Body→Exceptions`，由 `TestBuildEventApp_wireOrder` 锁定。
**考据教训**："MS-ASCAL schema 顺序"的说法经审查挑战后无法证实——Z-Push 实战线序、我们此前的排列、审查者主张的顺序三者互不相同；MS 原文 XSD 不可得，以对真实 Exchange 久经验证的 Z-Push 线序为准。

### 日历 Add 必带 DTStamp；标签名是 `DTStamp` 不是 `DtStamp`
wbxml codepage 大小写敏感，写错报 `unknown tag`。

### 更新事件必须沿用原 UID
`EventDraft.UID` 为空时每次随机生成——会割裂日程系列。更新路径必须传 existing UID。

### EAS 没有服务端 copy 语义
IMAP COPY 用 MoveItems 实现（移动语义，与 DavMail 一致）；APPEND 不支持。

### 上行 Sync 请求建议带 GetChanges=0 + DeletesAsMoves=1
只上传变更时，避免服务器同帧下发变更。

### FetchAttachment 的 Data 是未解码 base64 原文，调用方必须解码（2026-07-25 图片全挂事故）
fork 保持上游语义不解码（FORK-NOTES）。eas-bridge 曾直接用 `got.Data` 构建 MIME → `writeBase64` 二次编码 → 双层 base64，Apple Mail 解一层后拿到 `iVBORw0K...` 文本，**所有图片/附件全坏**。修复：解码收拢进 `downloadAttachment`（尺寸引导 `decodeAttachmentData`，与 webank-mail 逐字节一致）。
**分块路径两个叠加坑**（ZCode B-1/H-1）：① 每块的 Data 是**独立** base64 编码各自带 padding（4MB 不被 3 整除，中间块必有 `==`），拼接后整体解码必然在中间 padding 处失败；② Range 作用于**原始字节**（MS-ASCMD：附件 range applies to the file content），偏移必须按**解码后**长度推进，按 base64 文本长度推进会跳 ~1/3 内容。正确姿势：逐块 `decodeBase64Chunk` → 拼接原始字节。
**改 MIME 构建后必须 bump `mimeCacheVersion`**——已污染的 .eml/.bin 缓存不会自愈。

### Status 5 风暴必须退避，不能每分钟全量重拉（2026-07-25 凌晨 6 小时事故）
Coremail 对失效 synckey 回 Status 5；但**服务器整体故障时全文件夹都回 5**。此时"清 key + 全量重拉"每轮询周期一次 = 每分钟对每个文件夹全拉 6 个月邮件，打服务器+毁本地状态。`syncEngine.backoff`（纯内存）：成功清零、Status 5 按 1m→5m→15m→30m 升档、退避期 `skipBackoff` 短路返回 nil（本地 state 照常服务 IMAP/CalDAV 读）。
**两条并发纪律**：`trackSyncResult` 必须放 singleflight fn **内**（flight 合并后每个调用者都会拿到同一 err，放外面会重复升档，ZCode H-1）；poller 退避期**不得 onChange 广播**（无新数据却刷醒客户端）。
**后续（2026-07-25 P3）**：日历退避跳过改返回 `ErrSyncBackoffSkip` sentinel（maybeSyncCalendar 内部消化，lastCalSync 只在真成功推进）；syncMail 仍返回 nil（IMAP 会把错误透传客户端）。优雅退出：main 信号后 cancel→CalDAV/SMTP 渐进 Shutdown(10s)→IMAP Close；`imapd.shuttingDown` 标志区分"信号抢先于监听"时 go-imap 未导出 errClosed 与真故障，防 log.Fatal 误判。

## 三、架构不变量

1. **IMAP UID 1-based 单调递增、持久化**（serverID↔UID 双向映射 + folderMeta.NextUID/UIDValidity）；UIDVALIDITY 恒定，state 重置才 bump（客户端会全量重下）。
2. **state 分片落盘**（2026-07-25 重构，原单文件 11.5MB 全量重写债）：`state.json` 主文件只存 DeviceID/PolicyKey/SyncKeys/Folders/FolderMeta（KB 级）；`folders/<fid>.json` 存 Items/UIDs/Deleted；`events.json` 存日历。落盘定向化：单封操作只写本文件夹分片，synckey 类只写主文件，syncMailOnce 用 `mutateFolder` 写两者。旧单文件格式首次加载自动迁移（迁移任一分片写失败必须中止，否则主文件瘦身后丢数据）。**synckey 前进必须落盘**（saveNow→主文件），失效 key 重启恢复会被 Coremail 判失效回 Status 5。
3. **日历事件 = 快照+增量日志**（2026-07-25，原 10.5MB 全量重写债）：写路径只追加 `events.jsonl`（每行 upsert/delete，fsync），读走内存 map，重启快照+幂等重放；坏尾行（崩溃截断）跳过；压实=超 16MB 写快照+截断（启动时+每日）。不变量：事件批次与 synckey 落盘同节奏（崩溃丢的变更 key 也未推进，重拉补回）；"先快照后截断"窗口靠重放幂等安全；所有 jsonl 写者都在 st.mu 下（无 O_APPEND 并发截断竞争）。
3. **assignUIDs 按传入列表全量重建 UID 映射**——调用方必须传文件夹**全量** items（`addMovedItems` 曾只传增量，清空了目标文件夹原有 UID 映射；ZCode MEDIUM-3 实锤）。
4. **缓存只存完整结果**：`complete=false` 不写缓存（服务器抖动不固化残缺邮件）。raw.eml 始终缓存。
5. **只监听 localhost**；InsecureAuth 仅限此场景。config 被改成 0.0.0.0 要拒绝启动。
6. **singleflight 防并发风暴**：`sync-mail:<folderID>`、`sync-calendar`、`message:<folderID>\x00<serverID>` 等。
7. **与 webank-mail（姊妹项目）共存铁律**：独立 DeviceID + 独立 state 目录。EAS 按 (账号, 设备) 跟踪 synckey，共用 DeviceID 会互踢同步状态。

## 四、测试与操作纪律

### 写操作实测必须用"一次性测试邮件 + 精确主题匹配"
M6 之前 SEARCH 未实现 SUBJECT 过滤会返回全部 UID，取 `uids[-1]` 命中的可能是刚到达的真实邮件（曾误把真实 newsletter 标记已读并移走，后完整恢复）。
正确做法：FETCH 每封 header、RFC2047 解码 Subject 后精确匹配测试邮件主题再操作。

### 改 state.json 前必须 bootout 而不是 stop
launchd `KeepAlive` 下 `launchctl stop` 会立即拉起新进程，旧进程内存里的 state 会在下次 mutate 时覆盖你的编辑（表现为"全量重同步结果和编辑前一模一样"）。正确流程：
```bash
launchctl bootout gui/$(id -u)/com.keaneyan.eas-bridge
# 编辑 state.json
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.keaneyan.eas-bridge.plist
```

### 代理纪律
- daemon/probe 访问 EAS 服务器**需要** `HTTPS_PROXY=http://127.0.0.1:7897`
- gh/git 推 GitHub **不要**设代理（会 TLS timeout）

### 实测工具
python `imaplib`（IMAP 1143）+ `smtplib`（SMTP 1025）+ `curl`（CalDAV 8008）直接打 localhost。中文搜索参数需传 UTF-8 bytes + `CHARSET UTF-8`。

### 审查工作流
agent 实写 → ZCode 独立只读审查（原始需求一并给）→ 独立复核审查结论（有疑义时用上游源码/规范查证，曾推翻过审查者的"规范顺序"幻觉）→ 修复+回归测试 → 复审 PASS → 合 main 推送。
