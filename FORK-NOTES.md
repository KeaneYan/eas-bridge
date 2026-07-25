# 本仓库对 hstern/go-activesync v1.1.0 的 fork 补丁说明
# 上游: https://github.com/hstern/go-activesync (MIT, © 2026 Henry Stern)
# fork 位置: third_party/go-activesync（go.mod replace 指向）
#
# 与上游 v1.1.0 的差异:
#
# 【eas/email.go】
# 1. 新增 eas.Attachment 结构体 + EmailItem.Attachments 字段
#    （DisplayName/FileReference/Method/EstimatedDataSize/ContentId/
#      ContentLocation/IsInline/ContentType）
# 2. parseEmailFieldAirSyncBase 的 "Attachments" case 从"只置 HasAttachments"
#    改为 parseAttachments() 解析全部附件元数据
#
# 【eas/calendar.go】（2026-07-24，日历写路径）
# 3. buildEventApp 子元素按 Z-Push SyncAppointment 线序输出（原实现无序，
#    严格服务器可能拒绝）；新增 TestBuildEventApp_wireOrder 锁定顺序
# 4. buildEventApp 补 DTStamp（Sync Add 必填；wbxml 标签名 "DTStamp"）
# 5. EventDraft 新增 UID 字段：更新沿用原 UID，原实现每次随机生成会割裂系列
# 6. sendSyncCommands 的 Collection 补 GetChanges=0/DeletesAsMoves=1/Class=Calendar
#
# 动机: 上游丢弃 AirSyncBase Attachments 元素内容，拿不到 FileReference，
# 导致 FetchAttachment 无法使用、EAS 级附件（含 cid: 内嵌图）完全不可达。
#
# 注意: FetchAttachment 返回的 Data 库层不做 base64 解码（保持上游语义与
# 其测试约定），解码在本项目 mime.go decodeAttachmentData 做
# （尺寸引导判据；分块路径用同文件 decodeBase64Chunk 逐块严格解码）。上游测试在改动后保持全绿: go test ./eas/
