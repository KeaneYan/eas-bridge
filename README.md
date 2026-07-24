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
- **增量同步**：EAS Sync 增量拉取，默认 60s 轮询收件箱；邮件与日历后台预热，日历缓存过期后异步刷新
- **UID 稳定映射**：serverID ↔ IMAP UID 持久化，1-based 单调递增，UIDVALIDITY 时间戳（state 重置自动升位）
- **完整邮件呈现**：优先透传原始 MIME；Coremail 不返回 MIME 时自动用纯文本 + HTML、内嵌图片和普通附件重建标准 multipart 邮件
- **按需附件与缓存**：LIST / BODYSTRUCTURE / RFC822.SIZE 不预下载已知大小的附件，读取单个 MIME part 时只取对应附件；缓存采用并发去重、原子写入及 1 GiB / 30 天淘汰策略
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
  "poll_seconds": 60
}
```

## Apple Mail 配置

添加账户 → 其他邮件账户：

| 项 | 值 |
|---|---|
| 电子邮件地址 | you@example.com |
| 用户名 | 同 config 的 user |
| 密码 | 同 config 的 password |
| 收件服务器 | 127.0.0.1，端口 1143，**不使用 SSL** |
| 发件服务器 | 127.0.0.1，端口 1025，**不使用 SSL** |

## Apple 日历配置

系统设置 → 互联网账户 → 添加其他账户 → CalDAV 账户 → 手动：

| 项 | 值 |
|---|---|
| 用户名 | 同 config 的 user |
| 密码 | 同 config 的 password |
| 服务器地址 | 127.0.0.1 |
| 服务器路径 | /user/calendars/default/ |
| 端口 | 8008，**不使用 SSL** |

日历为**只读**（新建/修改/删除事件请在网页端或其他客户端操作）。

## 与 webank-mail 的关系

姊妹项目 [eas-mail-macos](https://github.com/KeaneYan/eas-mail-macos)（原生 SwiftUI 客户端）共用 `third_party/go-activesync` fork。本项目独立 DeviceID + 独立 state 目录（`~/.config/eas-bridge/`），两者可同时对同一账号同步互不干扰。

## 当前限制（v1）

- 不支持删除邮件（EXPUNGE/COPY/APPEND 返回 NO）——请在网页端或其他客户端操作
- SEARCH 只支持 ALL / UNSEEN
- 已读状态只改本地，不回推服务器
- 只轮询收件箱
- **日历只读**（PUT/DELETE 返回 403）；修改型循环例外（Exceptions 非 Deleted）暂忽略

## Roadmap

- 删除/移动映射到 EAS MoveItems
- 已读回推（EAS Sync Change）
- 日历写操作（EAS CreateEvent/UpdateEvent/DeleteEvent）
- 循环修改型例外 → RECURRENCE-ID 覆盖事件

## License

MIT
