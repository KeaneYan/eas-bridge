# eas-bridge

EAS（Exchange ActiveSync）→ **IMAP + SMTP + CalDAV** 协议桥。让 Apple Mail / Apple 日历等标准客户端直接连接只支持 EAS 的邮件服务器（如 Coremail 企业邮箱）。

```
Apple Mail ──IMAP :1143──┐
            ──SMTP :1025──┤ eas-bridge ──EAS(HTTPS)──> Coremail/Exchange
Apple 日历 ─CalDAV :8008──┘
```

## 特性

- **IMAP**（默认 `127.0.0.1:1143`）：LIST / SELECT / FETCH（ENVELOPE·FLAGS·BODY[]）/ STORE \Seen / SEARCH UNSEEN / IDLE+NOOP 新邮件推送
- **SMTP**（默认 `127.0.0.1:1025`）：AUTH PLAIN，MIME 原样透传到 EAS SendMail（服务器自动存已发送）
- **CalDAV**（默认 `127.0.0.1:8008`）：日历只读桥——Basic Auth、calendar-query time-range 过滤、**循环事件 RRULE 原生透传**（客户端自行展开）、VALARM 提醒、组织者/参与人、全天事件、取消状态
- **增量同步**：EAS Sync 增量拉取，默认 60s 轮询全部邮件文件夹（每轮重读列表，新文件夹自动纳入）；邮件与日历后台预热，日历缓存过期后异步刷新
- **UID 稳定映射**：serverID ↔ IMAP UID 持久化，1-based 单调递增，UIDVALIDITY 时间戳（state 重置自动升位）
- **完整邮件呈现**：优先透传原始 MIME；Coremail 不返回 MIME 时自动用纯文本 + HTML、内嵌图片和普通附件重建标准 multipart 邮件
- **按需附件与缓存**：LIST / BODYSTRUCTURE / RFC822.SIZE 不预下载已知大小的附件，读取单个 MIME part 时只取对应附件；缓存采用并发去重、原子写入及 1 GiB / 30 天淘汰策略；服务器明确不返回附件数据时指数退避，避免客户端轮询形成请求风暴
- **文件夹兼容**：仅向 IMAP 暴露邮件文件夹；自定义文件夹保留层级，重名文件夹生成稳定且可选择的名称
- **安全**：只绑回环地址（非回环拒绝启动）；凭据仅本地 config，0600 权限
- **Coremail 兼容**：EAS 不返回原始 MIME 时，自动降级请求 HTML/纯文本并构造合法 RFC822

## 快速开始

```bash
go build -o eas-bridge .

# 1. 初始化配置
./eas-bridge --init
# 2. 编辑 ~/.config/eas-bridge/config.json 填入 server/user/password
# 3. 启动
./eas-bridge
```

`config.json` 示例：

```json
{
  "server": "https://mail.example.com/Microsoft-Server-ActiveSync",
  "user": "you@example.com",
  "password": "your-password",
  "imap_addr": "127.0.0.1:1143",
  "smtp_addr": "127.0.0.1:1025",
  "caldav_addr": "127.0.0.1:8008",
  "poll_seconds": 60,
  "calendar_poll_seconds": 60
}
```

`poll_seconds` 控制邮件后台同步间隔，默认 60 秒；`calendar_poll_seconds`
单独控制日历，省略或设为非正数时沿用 `poll_seconds`，因此旧配置无需迁移。
日历会独立定时拉取，不需要保持 Apple 日历处于打开状态。

## 后台常驻（launchd，推荐）

`contrib/com.keaneyan.eas-bridge.plist` 是 macOS launchd 服务配置（开机自启 + 崩溃自动拉起，日志到 `~/Library/Logs/eas-bridge.log`）：

```bash
# 安装（首次）
cp contrib/com.keaneyan.eas-bridge.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.keaneyan.eas-bridge.plist

# 代码更新后：重新编译并重启服务才会用上新二进制
go build -o eas-bridge .
launchctl kickstart -k gui/$(id -u)/com.keaneyan.eas-bridge

# 日常管理
launchctl stop com.keaneyan.eas-bridge     # 停（会被 KeepAlive 拉起，彻底停用用 unload）
launchctl unload ~/Library/LaunchAgents/com.keaneyan.eas-bridge.plist  # 停用
tail -f ~/Library/Logs/eas-bridge.log      # 看日志
```

注意：plist 中 `ProgramArguments`/`WorkingDirectory` 写死了本仓库绝对路径，移动仓库目录后需同步修改。

## Apple Mail 配置

邮件 → 添加账户 → 其他"邮件"账户 → 手动配置：

| 项 | 值 |
|---|---|
| 电子邮件地址 | 完整邮箱，如 `you@webank.com` |
| 用户名 | **完整邮箱地址（必须带 `@webank.com` 后缀）**，与 config 的 user 一致 |
| 密码 | 同 config 的 password |
| 收件服务器（IMAP） | `127.0.0.1`，端口 `1143` |
| 发件服务器（SMTP） | `127.0.0.1`，端口 `1025` |

**两个必踩的坑**：

1. **用户名必须带域名后缀**。只填 `you` 会认证失败——桥会把用户名原样传给 EAS 服务器，Coremail 要求完整地址。
2. **IMAP 和 SMTP 都要在"高级"里勾上"允许不安全的连接"**。桥只听 localhost 所以不走 TLS，但 Apple Mail 默认拒绝明文认证，不勾会反复弹密码框、怎么输都不对：
   - 收件：邮件 → 设置 → 账户 → 选中账户 → 服务器设置 → **高级 IMAP 设置** → 勾选「允许不安全的连接」
   - 发件：同页 **高级 SMTP 设置** → 同样勾选（两处是独立的，只勾一边另一边还是连不上）

> 安全性说明：明文只存在于 `127.0.0.1` 本机回环，不出网卡；桥到 EAS 服务器之间仍是 HTTPS。

## Apple 日历配置

系统设置 → 互联网账户 → 添加其他账户 → CalDAV 账户 → 手动：

| 项 | 值 |
|---|---|
| 用户名 | **完整邮箱地址（同 Apple Mail，必须带 `@webank.com` 后缀）** |
| 密码 | 同 config 的 password |
| 服务器地址 | 127.0.0.1 |
| 服务器路径 | /user/calendars/default/ |
| 端口 | 8008，**不使用 SSL** |

日历为**只读**（新建/修改/删除事件请在网页端或其他客户端操作）。

## 与 webank-mail 的关系

姊妹项目 [eas-mail-macos](https://github.com/KeaneYan/eas-mail-macos)（原生 SwiftUI 客户端）共用 `third_party/go-activesync` fork。本项目独立 DeviceID + 独立 state 目录（`~/.config/eas-bridge/`），两者可同时对同一账号同步互不干扰。

## 开发

**[docs/DEV-NOTES.md](docs/DEV-NOTES.md)**：开发者避坑指南——Coremail 服务器怪癖（全部实测背书）、EAS 协议实战、架构不变量、测试纪律。**改代码前必读**，都是真实事故换来的。

## 当前限制（v1）

- 删除邮件 = 移到服务器"已删除"文件夹（可恢复）；无永久删除
- COPY 实为移动（EAS 无服务端复制语义，与 DavMail 一致）；APPEND 不支持
- SEARCH 支持 SUBJECT/FROM/TO/CC 等头部、日期、尺寸、标志位、NOT/OR 组合；TEXT/BODY 只覆盖已缓存正文（读过的邮件），未缓存的正文不参与匹配（Apple Mail 全文搜索走本地索引，不受影响）；不支持 \Answered/\Draft 标志（服务器无此数据）
- 已读回推失败时不重试（本地状态保留，下次增量同步收敛）
- **日历写入被服务器拒绝**：Coremail 对日历文件夹的上行 Sync 命令一律回 Status 5（实测 Add/Change 跨协议版本 12.1/14.0/14.1 均拒绝，邮件上行正常）——代码已完整实现创建/更新/删除，但服务器策略使日历实为只读，写操作会收到 403 及明确提示；若服务器策略变更则自动可用

## Roadmap

- SEARCH 支持 SUBJECT/FROM 等常用条件
- 日历写操作（EAS CreateEvent/UpdateEvent/DeleteEvent）
- 循环修改型例外 → RECURRENCE-ID 覆盖事件

## License

MIT
