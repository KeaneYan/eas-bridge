# imeg-eas

EAS（Exchange ActiveSync）→ **IMAP + SMTP** 协议桥。让 Apple Mail 等标准邮件客户端直接连接只支持 EAS 的邮件服务器（如 Coremail 企业邮箱）。

```
Apple Mail ──IMAP :1143──┐
            ──SMTP :1025──┤ imeg-eas ──EAS(HTTPS)──> Coremail/Exchange
```

## 特性

- **IMAP**（默认 `127.0.0.1:1143`）：LIST / SELECT / FETCH（ENVELOPE·FLAGS·BODY[]）/ STORE \Seen / SEARCH UNSEEN / IDLE+NOOP 新邮件推送
- **SMTP**（默认 `127.0.0.1:1025`）：AUTH PLAIN，MIME 原样透传到 EAS SendMail（服务器自动存已发送）
- **增量同步**：EAS Sync 增量拉取，默认 60s 轮询收件箱；其他文件夹 SELECT 时同步
- **UID 稳定映射**：serverID ↔ IMAP UID 持久化，1-based 单调递增，UIDVALIDITY 时间戳（state 重置自动升位）
- **安全**：只绑回环地址（非回环拒绝启动）；凭据仅本地 config，0600 权限
- **Coremail 兼容**：纯文本邮件 EAS 不返回 MIME 时，自动用元数据构造合法 RFC822

## 快速开始

```bash
go build -o imeg-eas .

# 1. 初始化配置
./imeg-eas --init
# 2. 编辑 ~/.config/imeg-eas/config.json 填入 server/user/password
# 3. 启动
./imeg-eas
```

`config.json` 示例：

```json
{
  "server": "https://mail.example.com/Microsoft-Server-ActiveSync",
  "user": "you@example.com",
  "password": "your-password",
  "imap_addr": "127.0.0.1:1143",
  "smtp_addr": "127.0.0.1:1025",
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

## 与 webank-mail 的关系

姊妹项目 [eas-mail-macos](https://github.com/KeaneYan/eas-mail-macos)（原生 SwiftUI 客户端）共用 `third_party/go-activesync` fork。本项目独立 DeviceID + 独立 state 目录（`~/.config/imeg-eas/`），两者可同时对同一账号同步互不干扰。

## 当前限制（v1）

- 不支持删除邮件（EXPUNGE/COPY/APPEND 返回 NO）——请在网页端或其他客户端操作
- SEARCH 只支持 ALL / UNSEEN
- 已读状态只改本地，不回推服务器
- 只轮询收件箱
- Coremail 纯文本降级路径只构造 text/plain 正文（HTML 邮件暂走 EAS MIME，若服务器返回则原样透传）
- 日历/联系人未桥接（CalDAV 在 roadmap）

## Roadmap

- M3：CalDAV 日历桥（go-webdav/caldav + 循环事件展开）
- HTML 邮件正文降级构造
- 删除/移动映射到 EAS MoveItems
- 已读回推（EAS Sync Change）

## License

MIT
