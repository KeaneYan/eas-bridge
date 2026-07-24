# AGENTS.md — eas-bridge

EAS（Exchange ActiveSync）→ IMAP/SMTP/CalDAV 本地桥，让 Apple Mail/日历连接 Coremail 企业邮箱。Go 单 daemon，launchd 常驻。

## ⚠️ 动手前必读

**[docs/DEV-NOTES.md](docs/DEV-NOTES.md)** —— 全部实测踩坑（服务器怪癖/协议实战/架构不变量/测试纪律）。**不看就改代码，大概率重复事故**（真实事故率：本文档每条都对应一次线上踩坑）。

最高危五条（细节全在 DEV-NOTES）：
1. **probe/诊断脚本必须复用固定 DeviceID**（如 `easprobe`）——新建 DeviceID 会撑爆设备合作关系上限，全账号增量同步瘫痪
2. **Coremail 怪癖**：FilterType 必须显式 `FilterSixMonth`；MoveItems 成功码=3 不是 1；失效 synckey 回 Status 5 不是 3；EAS 不回显本设备变更（写操作后必须自行落本地 state）
3. **synckey 前进必须落盘**——否则重启后被服务器判失效
4. **日历上行被服务器策略拒绝**（Status 5）——写代码已实现但勿删，不是 bug
5. **写操作实测用一次性测试邮件+精确主题匹配**——SEARCH 历史教训：取 UID 列表末尾命中过真实邮件

## 构建与测试

```bash
go build ./... && go test ./... && go vet ./... && go test -race ./...
cd third_party/go-activesync && go test ./...   # fork 改动时也要跑
```

部署（改码后必做，daemon 是 launchd 常驻）：
```bash
go build -o eas-bridge . && launchctl kickstart -k gui/$(id -u)/com.keaneyan.eas-bridge
tail -f ~/Library/Logs/eas-bridge.log   # 就绪行: [eas-bridge] 就绪。IMAP...SMTP...CalDAV...
```

改 state.json 前必须 `launchctl bootout`（不是 stop，KeepAlive 会立即拉起覆盖编辑），改完 `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.keaneyan.eas-bridge.plist`。

## 实测验证（不走 UI）

python `imaplib`(1143) / `smtplib`(1025) / `curl`(CalDAV 8008) 打 localhost。凭证读 `~/.config/eas-bridge/config.json`（**不得外泄、不得写入提交**）。中文 IMAP 搜索参数传 UTF-8 bytes + `CHARSET UTF-8`。

## 代理纪律

- daemon/probe 访问 EAS：**需要** `HTTPS_PROXY=http://127.0.0.1:7897`
- git/gh 推 GitHub：**不要**设代理（TLS timeout）

## 仓库结构

- `imapd.go` `smtpd.go` `caldavd.go` `caldav_write.go` —— 三个协议端点
- `sync.go` `state.go` `folders.go` —— 同步引擎与持久化
- `search.go` —— IMAP SEARCH 过滤
- `mime.go` `mime_engine.go` `cache.go` —— MIME 构建/缓存（PR #1 架构）
- `third_party/go-activesync/` —— EAS 协议 fork（与上游差异见 [FORK-NOTES.md](FORK-NOTES.md)）
- `contrib/` —— launchd plist 模板

## 工作流

agent 实写 → 独立审查（原始需求一并给审查者）→ 独立复核审查结论（有疑义查上游源码/规范，审查者也会幻觉）→ 修复+回归测试 → 合 main 直接 push（无 PR 流程，不 force-push）。
