# eas-bridge 后续跟进项（BACKLOG）

> 2026-07-25 整理：当日两轮修复（附件双层 base64、Status 5 退避）的审查遗留 + 前日全量审查（ZCode full-review）未做项。按优先级排序，完成即删。

## P2 · 性能

- [ ] **events.json 10.5MB 全量重写 → append-only JSONL + 周期压实**（full-review ROI-4）：读路径零影响（内存 map 不变），优于按年分片（避开跨年 time-range 合并多片）。
- [ ] **poller 串行同步改并发**（full-review ROI-5）：60s 一轮串行所有文件夹，大文件夹拖慢其他文件夹时效。

## P3 · 语义/体验

- [ ] **CalDAV `lastCalSync` 退避期误推进**（backoff-review M-2）：退避短路也刷新"上次同步时间"，客户端看不出数据降级。用 sentinel 错误区分。
- [ ] daemon 优雅退出（full-review ROI-7）：停止接收新连接+等在途请求。低优先，已评估。

## P4 · 代码卫生（攒批顺手清）

- [ ] `decodeBase64Chunk` 返回 compactLen 复用，消除与 `decodeAttachmentData` 的双份空白字符表耦合（attach-review2 L-1）
- [ ] `fetchAndBuildMIME` 死代码删除（attach-review L-3；保留决策已做，下次确认后删）
- [ ] `imapd.go:347/416` 变量命名改 `emailChanges`（full-review LOW）
- [ ] FORK-NOTES.md 路径漂移修正（attach-review2 建议）
