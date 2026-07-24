package main

// CalDAV 写路径：iCal → EAS EventDraft 转换 + PUT/DELETE 处理。
// 与 mail 写操作同一原则：EAS 不回显本设备变更，写成功后必须自行落本地 state。

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/hstern/go-activesync/eas"
)

// PutCalendarObject 创建或更新事件。path 里带 serverID 为更新，否则创建。
func (b *caldavBackend) PutCalendarObject(ctx context.Context, path string, calendar *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	if path != caldavCalendarPath && !strings.HasPrefix(path, caldavCalendarPath) {
		return nil, webdavNotFound("日历不存在: " + path)
	}
	ev := firstVEvent(calendar)
	if ev == nil {
		return nil, webdavBadRequest("VCALENDAR 里没有 VEVENT")
	}

	folderID, err := b.engine.calendarFolderID()
	if err != nil {
		return nil, err
	}

	// 更新场景：path 里的 serverID 能在本地找到即视为更新
	serverID, _ := objectPathToServerID(path)
	b.engine.st.mu.Lock()
	existing, isUpdate := b.engine.st.Events[serverID]
	b.engine.st.mu.Unlock()

	draft, uid, err := icalEventToDraft(ev, isUpdate)
	if err != nil {
		return nil, webdavBadRequest(err.Error())
	}
	draft.UID = uid

	wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if isUpdate {
		// 保留服务器侧字段：组织者/会议状态/时区原文/修改型例外
		draft.TimeZoneRaw = existing.TimeZoneRaw
		if draft.TimeZoneRaw == "" {
			draft.TimeZoneRaw = eas.EncodeTimeZone(time.Local)
		}
		if err := b.engine.c.UpdateEvent(wctx, folderID, serverID, draft); err != nil {
			return nil, calWriteErr("更新事件", err)
		}
		updated := draftToEventItem(serverID, uid, draft)
		updated.OrganizerName = existing.OrganizerName
		updated.OrganizerEmail = existing.OrganizerEmail
		updated.MeetingStatus = existing.MeetingStatus
		updated.TimeZoneRaw = existing.TimeZoneRaw
		updated.Exceptions = mergeExceptions(existing.Exceptions, draft.Exceptions)
		if err := b.engine.st.upsertEvent(updated); err != nil {
			return nil, err
		}
		return eventToCalendarObject(updated), nil
	}

	draft.TimeZoneRaw = eas.EncodeTimeZone(time.Local)
	newID, err := b.engine.c.CreateEvent(wctx, folderID, draft)
	if err != nil {
		return nil, calWriteErr("创建事件", err)
	}
	if newID == "" {
		return nil, fmt.Errorf("服务器创建事件未返回 ID")
	}
	created := draftToEventItem(newID, uid, draft)
	created.OrganizerEmail = b.engine.cfg.User
	if err := b.engine.st.upsertEvent(created); err != nil {
		return nil, err
	}
	return eventToCalendarObject(created), nil
}

// DeleteCalendarObject 删除事件并本地落库。
func (b *caldavBackend) DeleteCalendarObject(ctx context.Context, path string) error {
	serverID, err := objectPathToServerID(path)
	if err != nil {
		return webdavNotFound("对象不存在: " + path)
	}
	folderID, err := b.engine.calendarFolderID()
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := b.engine.c.DeleteEvent(wctx, folderID, serverID); err != nil {
		return calWriteErr("删除事件", err)
	}
	return b.engine.st.deleteEvent(serverID)
}

// calWriteErr 把日历写失败翻译为 CalDAV 错误。Coremail 对日历文件夹的上行
// Sync 命令一律回 Status 5（实测 Add/Change 跨 12.1/14.0/14.1 均拒绝，
// 邮件上行正常）——映射为 403 并给出明确原因，而不是笼统的 500。
func calWriteErr(op string, err error) error {
	if eas.IsStatusCode(err, 5) {
		return webdavErr(http.StatusForbidden,
			op+"：服务器拒绝日历写入（Coremail 策略——日历经 EAS 只读，请在网页端/会议系统操作）")
	}
	return fmt.Errorf("服务器%s失败: %w", op, err)
}

// ---------- iCal → EAS 转换 ----------

func firstVEvent(cal *ical.Calendar) *ical.Event {
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent {
			return &ical.Event{Component: c}
		}
	}
	return nil
}

// icalEventToDraft 把 VEVENT 转成 EventDraft，返回 iCal UID。
// isUpdate 为 true 时放宽必填校验（客户端可能只回传部分字段——不，Apple 总回传全量，保持严格）。
func icalEventToDraft(e *ical.Event, isUpdate bool) (eas.EventDraft, string, error) {
	var d eas.EventDraft
	prop := func(name string) *ical.Prop { return e.Props.Get(name) }
	text := func(name string) string {
		if p := prop(name); p != nil {
			return strings.TrimSpace(p.Value)
		}
		return ""
	}

	d.Subject = text(ical.PropSummary)
	d.Location = text(ical.PropLocation)
	d.Body = text(ical.PropDescription)
	uid := text(ical.PropUID)

	start, allDay, err := parseICalDateTime(prop(ical.PropDateTimeStart))
	if err != nil {
		return d, uid, fmt.Errorf("DTSTART 解析失败: %w", err)
	}
	end, endAllDay, err := parseICalDateTime(prop(ical.PropDateTimeEnd))
	if err != nil {
		// 无 DTEND：全日事件默认 1 天，定时事件默认 1 小时
		if allDay {
			end = start.Add(24 * time.Hour)
		} else {
			end = start.Add(time.Hour)
		}
	} else if endAllDay != allDay {
		return d, uid, fmt.Errorf("DTSTART/DTEND 全天属性不一致")
	}
	d.StartTime = start
	d.EndTime = end
	d.AllDayEvent = allDay

	// 忙碌状态：TRANSPARENT=空闲；缺省/OPAQUE=忙
	d.BusyStatus = 2
	if strings.EqualFold(text(ical.PropTransparency), "TRANSPARENT") {
		d.BusyStatus = 0
	}
	// 私密
	switch strings.ToUpper(text(ical.PropClass)) {
	case "PRIVATE":
		d.Sensitivity = 2
	case "CONFIDENTIAL":
		d.Sensitivity = 3
	}
	// 提醒：取第一个 DISPLAY VALARM 的 -PTxM / -PTxH
	d.Reminder = parseICalAlarmMinutes(e)
	// 参与人
	for _, p := range e.Props[ical.PropAttendee] {
		email := strings.TrimPrefix(p.Value, "mailto:")
		email = strings.TrimPrefix(email, "MAILTO:")
		if email == "" {
			continue
		}
		d.Attendees = append(d.Attendees, eas.EventAttendee{
			Email: email,
			Name:  p.Params.Get(ical.ParamCommonName),
		})
	}
	// 循环
	if p := prop(ical.PropRecurrenceRule); p != nil {
		if r := rruleToEASRecurrence(p.Value); r != nil {
			d.Recurrence = r
		} else {
			return d, uid, fmt.Errorf("暂不支持的循环规则: %s", p.Value)
		}
	}
	// EXDATE → 删除型例外（修改型例外 v1 忽略，与读路径一致）
	exdates := e.Props[ical.PropExceptionDates]
	for i := range exdates {
		t, _, err := parseICalDateTime(&exdates[i])
		if err != nil {
			continue
		}
		d.Exceptions = append(d.Exceptions, eas.Exception{
			ExceptionStartTime: t,
			Deleted:            true,
		})
	}
	if d.Subject == "" {
		return d, uid, fmt.Errorf("事件缺少标题（SUMMARY）")
	}
	return d, uid, nil
}

// parseICalDateTime 解析 DTSTART/DTEND 属性：VALUE=DATE 为全天（UTC 零点），
// DATE-TIME 统一转 UTC。返回 (时间, 是否全天, 错误)。
func parseICalDateTime(p *ical.Prop) (time.Time, bool, error) {
	if p == nil {
		return time.Time{}, false, fmt.Errorf("属性缺失")
	}
	v := strings.TrimSpace(p.Value)
	// VALUE=DATE：YYYYMMDD
	if len(v) == 8 && !strings.ContainsAny(v, "T") {
		t, err := time.ParseInLocation("20060102", v, time.UTC)
		return t, true, err
	}
	// DATE-TIME：带 Z 为 UTC，否则按浮点时间用本地时区解释
	layout := "20060102T150405"
	if strings.HasSuffix(v, "Z") {
		t, err := time.Parse(layout+"Z", v)
		return t.UTC(), false, err
	}
	t, err := time.ParseInLocation(layout, v, time.Local)
	return t.UTC(), false, err
}

// parseICalAlarmMinutes 从 VEVENT 的第一个 VALARM 提取提前分钟数（-PT15M / -PT1H30M / -P1D）。
func parseICalAlarmMinutes(e *ical.Event) int {
	for _, c := range e.Component.Children {
		if c.Name != ical.CompAlarm {
			continue
		}
		trig := c.Props.Get(ical.PropTrigger)
		if trig == nil {
			continue
		}
		if m, ok := parseICalDurationMinutes(trig.Value); ok {
			return m
		}
	}
	return 0
}

// parseICalDurationMinutes 解析 "-PT15M" / "-PT1H30M" / "-P1D" 为提前分钟数。
// 只处理负向（提前）触发；正向（事后）返回 0。
func parseICalDurationMinutes(v string) (int, bool) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" || !strings.HasPrefix(v, "-P") {
		return 0, false
	}
	v = strings.TrimPrefix(v, "-P")
	total := 0
	inTime := false
	num := ""
	for _, ch := range v {
		switch {
		case ch == 'T':
			inTime = true
		case ch >= '0' && ch <= '9':
			num += string(ch)
		default:
			n, _ := strconv.Atoi(num)
			num = ""
			switch ch {
			case 'D':
				total += n * 24 * 60
			case 'W':
				total += n * 7 * 24 * 60
			case 'H':
				if inTime {
					total += n * 60
				}
			case 'M':
				if inTime {
					total += n
				}
			}
		}
	}
	return total, true
}

// draftToEventItem 把写成功的 draft 物化为本地 EventItem。
func draftToEventItem(serverID, uid string, d eas.EventDraft) eas.EventItem {
	return eas.EventItem{
		ServerID:    serverID,
		UID:         uid,
		Subject:     d.Subject,
		Location:    d.Location,
		Body:        d.Body,
		StartTime:   d.StartTime,
		EndTime:     d.EndTime,
		AllDayEvent: d.AllDayEvent,
		BusyStatus:  d.BusyStatus,
		Sensitivity: d.Sensitivity,
		Reminder:    d.Reminder,
		Attendees:   d.Attendees,
		Recurrence:  d.Recurrence,
		Exceptions:  d.Exceptions,
		TimeZoneRaw: d.TimeZoneRaw,
	}
}

// mergeExceptions 更新事件时合并例外：保留服务器原有的修改型例外，
// 用客户端新提交的删除型例外覆盖同实例的旧删除项。
func mergeExceptions(existing, fromClient []eas.Exception) []eas.Exception {
	out := append([]eas.Exception(nil), existing...)
	for _, nc := range fromClient {
		found := false
		for i, ex := range out {
			if ex.ExceptionStartTime.Equal(nc.ExceptionStartTime) {
				out[i] = nc
				found = true
				break
			}
		}
		if !found {
			out = append(out, nc)
		}
	}
	return out
}

// ---------- RRULE → EAS Recurrence ----------

var dowCodeToBit = map[string]eas.DayOfWeek{
	"SU": eas.DowSunday, "MO": eas.DowMonday, "TU": eas.DowTuesday,
	"WE": eas.DowWednesday, "TH": eas.DowThursday, "FR": eas.DowFriday, "SA": eas.DowSaturday,
}

// rruleToEASRecurrence 解析 iCal RRULE 为 EAS Recurrence。
// 支持 FREQ=DAILY/WEEKLY/MONTHLY/YEARLY + INTERVAL/COUNT/UNTIL/BYDAY/BYMONTHDAY/BYMONTH，
// 与 easRecurrenceToRRULE 严格互逆。不支持的组合返回 nil。
func rruleToEASRecurrence(s string) *eas.Recurrence {
	kv := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		kv[strings.ToUpper(strings.TrimSpace(k))] = strings.ToUpper(strings.TrimSpace(v))
	}
	r := &eas.Recurrence{CalendarType: 1}
	freq := kv["FREQ"]
	interval, _ := strconv.Atoi(kv["INTERVAL"])
	r.Interval = interval
	if n, err := strconv.Atoi(kv["COUNT"]); err == nil && n > 0 {
		r.Occurrences = n
	}
	if u := kv["UNTIL"]; u != "" {
		if t, err := time.Parse("20060102T150405Z", u); err == nil {
			r.Until = t
		} else if t, err := time.ParseInLocation("20060102", u, time.UTC); err == nil {
			r.Until = t
		}
	}
	byday := kv["BYDAY"]
	bits, weekOfMonth := parseByDay(byday)

	switch freq {
	case "DAILY":
		r.Type = eas.RecurrenceDaily
	case "WEEKLY":
		r.Type = eas.RecurrenceWeekly
		r.DayOfWeek = bits
	case "MONTHLY":
		if dom, err := strconv.Atoi(kv["BYMONTHDAY"]); err == nil && dom > 0 {
			r.Type = eas.RecurrenceMonthlyDate
			r.DayOfMonth = dom
		} else if byday != "" && weekOfMonth > 0 {
			r.Type = eas.RecurrenceMonthlyByDay
			r.DayOfWeek = bits
			r.WeekOfMonth = weekOfMonth
		} else {
			return nil
		}
	case "YEARLY":
		moy, _ := strconv.Atoi(kv["BYMONTH"])
		r.MonthOfYear = moy
		if dom, err := strconv.Atoi(kv["BYMONTHDAY"]); err == nil && dom > 0 {
			r.Type = eas.RecurrenceYearlyDate
			r.DayOfMonth = dom
		} else if byday != "" && weekOfMonth > 0 {
			r.Type = eas.RecurrenceYearlyByDay
			r.DayOfWeek = bits
			r.WeekOfMonth = weekOfMonth
		} else {
			return nil
		}
	default:
		return nil
	}
	return r
}

// parseByDay 解析 "MO,WE" / "2TU" / "-1MO" 为 (位掩码, 第几周)。
// 无位置前缀时 weekOfMonth=0；-1 映射为 EAS 的 5（最后一周）。
func parseByDay(s string) (eas.DayOfWeek, int) {
	var bits eas.DayOfWeek
	weekOfMonth := 0
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if len(tok) < 2 {
			continue
		}
		code := tok[len(tok)-2:]
		pos := strings.TrimSuffix(tok, code)
		if bit, ok := dowCodeToBit[code]; ok {
			bits |= bit
		}
		if pos != "" {
			if n, err := strconv.Atoi(pos); err == nil {
				if n == -1 {
					weekOfMonth = 5
				} else if n >= 1 && n <= 4 {
					weekOfMonth = n
				}
			}
		}
	}
	return bits, weekOfMonth
}

// ---------- WebDAV 错误 ----------

func webdavErr(code int, msg string) error {
	return webdav.NewHTTPError(code, fmt.Errorf("%s", msg))
}

func webdavNotFound(msg string) error {
	return webdavErr(http.StatusNotFound, msg)
}

func webdavBadRequest(msg string) error {
	return webdavErr(http.StatusBadRequest, msg)
}
