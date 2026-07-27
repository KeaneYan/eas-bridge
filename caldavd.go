package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/hstern/go-activesync/eas"
)

// go-webdav caldav 按路径深度识别资源类型：1=principal 2=homeSet 3=calendar 4=object
const (
	caldavPrincipalPath = "/user/"
	caldavHomeSetPath   = "/user/calendars/"
	caldavCalendarPath  = "/user/calendars/default/"
	calSyncTTL          = 60 * time.Second
)

// caldavBackend 实现 caldav.Backend：EAS 日历事件 → iCalendar 只读桥。
type caldavBackend struct {
	engine *syncEngine

	calMu       sync.Mutex
	lastCalSync time.Time
	calSyncing  bool
}

// ---------- Backend 接口 ----------

func (b *caldavBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return caldavPrincipalPath, nil
}

func (b *caldavBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return caldavHomeSetPath, nil
}

func (b *caldavBackend) CreateCalendar(ctx context.Context, calendar *caldav.Calendar) error {
	return errCalDAVReadOnly("CreateCalendar")
}

func (b *caldavBackend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	if err := b.maybeSyncCalendar(ctx); err != nil {
		log.Printf("[caldav] 同步失败（用缓存数据）: %v", err)
	}
	return []caldav.Calendar{{
		Path:                  caldavCalendarPath,
		Name:                  "日历",
		Description:           "eas-bridge 桥接的 EAS 日历",
		MaxResourceSize:       10 << 20,
		SupportedComponentSet: []string{ical.CompEvent},
	}}, nil
}

func (b *caldavBackend) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	if path != caldavCalendarPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("日历不存在: %s", path))
	}
	cals, err := b.ListCalendars(ctx)
	if err != nil {
		return nil, err
	}
	return &cals[0], nil
}

func (b *caldavBackend) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	if err := b.maybeSyncCalendar(ctx); err != nil {
		log.Printf("[caldav] 同步失败（用缓存数据）: %v", err)
	}
	serverID, err := objectPathToServerID(path)
	if err != nil {
		return nil, webdav.NewHTTPError(http.StatusNotFound, err)
	}
	b.engine.st.mu.Lock()
	ev, ok := b.engine.st.Events[serverID]
	b.engine.st.mu.Unlock()
	if !ok {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("事件不存在: %s", serverID))
	}
	return eventToCalendarObject(ev), nil
}

func (b *caldavBackend) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	if path != caldavCalendarPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("日历不存在: %s", path))
	}
	if err := b.maybeSyncCalendar(ctx); err != nil {
		log.Printf("[caldav] 同步失败（用缓存数据）: %v", err)
	}
	return b.allObjects(), nil
}

func (b *caldavBackend) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	if path != caldavCalendarPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("日历不存在: %s", path))
	}
	if err := b.maybeSyncCalendar(ctx); err != nil {
		log.Printf("[caldav] 同步失败（用缓存数据）: %v", err)
	}
	// caldav.Filter 自带 time-range 匹配（含 RRULE 展开判断）
	return caldav.Filter(query, b.allObjects())
}

// PutCalendarObject / DeleteCalendarObject 的写路径实现在 caldav_write.go。

// ---------- 内部 ----------

// maybeSyncCalendar uses stale-while-revalidate: when cached events exist, an
// expired cache is refreshed in the background so CalDAV reads stay responsive.
func (b *caldavBackend) maybeSyncCalendar(ctx context.Context) error {
	b.calMu.Lock()
	if time.Since(b.lastCalSync) < calSyncTTL {
		b.calMu.Unlock()
		return nil
	}
	if b.calSyncing {
		b.calMu.Unlock()
		return nil
	}
	b.engine.st.mu.Lock()
	hasCachedEvents := len(b.engine.st.Events) > 0
	b.engine.st.mu.Unlock()
	b.calSyncing = true
	b.calMu.Unlock()

	if hasCachedEvents {
		go func() {
			syncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			b.finishCalendarSync(b.engine.syncCalendar(syncCtx))
		}()
		return nil
	}
	err := b.engine.syncCalendar(ctx)
	b.finishCalendarSync(err)
	if errors.Is(err, ErrSyncBackoffSkip) {
		return nil // 退避跳过不是故障，lastCalSync 不推进（下轮查询会再试）
	}
	return err
}

func (b *caldavBackend) finishCalendarSync(err error) {
	b.calMu.Lock()
	defer b.calMu.Unlock()
	b.calSyncing = false
	if err == nil {
		b.lastCalSync = time.Now()
	}
}

// allObjects 把缓存中全部事件转成 CalendarObject（按 StartTime 排序，稳定输出）。
func (b *caldavBackend) allObjects() []caldav.CalendarObject {
	b.engine.st.mu.Lock()
	events := make([]eas.EventItem, 0, len(b.engine.st.Events))
	for _, ev := range b.engine.st.Events {
		events = append(events, ev)
	}
	b.engine.st.mu.Unlock()
	sort.Slice(events, func(i, j int) bool {
		if events[i].StartTime.Equal(events[j].StartTime) {
			return events[i].ServerID < events[j].ServerID
		}
		return events[i].StartTime.Before(events[j].StartTime)
	})
	objs := make([]caldav.CalendarObject, 0, len(events))
	for _, ev := range events {
		objs = append(objs, *eventToCalendarObject(ev))
	}
	return objs
}

// objectPathToServerID 从 /calendars/user/default/<escaped>.ics 还原 serverID。
func objectPathToServerID(path string) (string, error) {
	if !strings.HasPrefix(path, caldavCalendarPath) || !strings.HasSuffix(path, ".ics") {
		return "", fmt.Errorf("非法对象路径: %s", path)
	}
	escaped := strings.TrimSuffix(strings.TrimPrefix(path, caldavCalendarPath), ".ics")
	serverID, err := url.PathUnescape(escaped)
	if err != nil {
		return "", fmt.Errorf("路径解码失败: %w", err)
	}
	return serverID, nil
}

// eventToCalendarObject 把 EAS 事件转成 CalDAV 对象（含 ETag/ContentLength）。
func eventToCalendarObject(ev eas.EventItem) *caldav.CalendarObject {
	cal := eventToICal(ev)
	var sb strings.Builder
	if err := ical.NewEncoder(&sb).Encode(cal); err != nil {
		log.Printf("[caldav] 编码事件 %s 失败: %v", ev.ServerID, err)
	}
	data := sb.String()
	sum := sha1.Sum([]byte(data))
	return &caldav.CalendarObject{
		Path:          caldavCalendarPath + url.PathEscape(ev.ServerID) + ".ics",
		ModTime:       ev.StartTime, // 稳定值（ZCode L1）：time.Now() 会让 getlastmodified 每次变
		ContentLength: int64(len(data)),
		ETag:          `"` + hex.EncodeToString(sum[:8]) + `"`,
		Data:          cal,
	}
}

// eventToICal 把 EAS EventItem 转成 iCalendar（RRULE 原生透传不展开，客户端自行展开）。
func eventToICal(ev eas.EventItem) *ical.Calendar {
	// go-ical 的 SetText 只转义 \n 不转 \r，EAS 文本常带 \r\n——统一先清洗
	clean := func(s string) string {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		return strings.ReplaceAll(s, "\r", "")
	}
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//eas-bridge//EAS CalDAV Bridge//ZH")

	e := ical.NewEvent()
	uid := ev.UID
	if uid == "" {
		uid = ev.ServerID + "@eas-bridge"
	}
	e.Props.SetText(ical.PropUID, clean(uid))
	e.Props.SetText(ical.PropSummary, clean(ev.Subject))
	if ev.Location != "" {
		e.Props.SetText(ical.PropLocation, clean(ev.Location))
	}
	if ev.Body != "" {
		e.Props.SetText(ical.PropDescription, clean(ev.Body))
	}
	if ev.AllDayEvent {
		e.Props.SetDate(ical.PropDateTimeStart, ev.StartTime)
		e.Props.SetDate(ical.PropDateTimeEnd, ev.EndTime)
	} else {
		e.Props.SetDateTime(ical.PropDateTimeStart, ev.StartTime.UTC())
		e.Props.SetDateTime(ical.PropDateTimeEnd, ev.EndTime.UTC())
	}
	e.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())

	// 忙碌状态：0=Free 1=Tentative 2=Busy 3=OutOfOffice
	if ev.BusyStatus == 0 {
		e.Props.SetText(ical.PropTransparency, "TRANSPARENT")
	}
	// 私密
	if ev.Sensitivity > 0 {
		e.Props.SetText(ical.PropClass, "PRIVATE")
	}
	// 取消（MeetingStatus bit 2 = cancelled：3=会议已取消，5/7 同理取低两位判断）
	if ev.MeetingStatus == 3 || ev.MeetingStatus == 5 || ev.MeetingStatus == 7 {
		e.Props.SetText(ical.PropStatus, "CANCELLED")
	}
	// 组织者/参与人
	if ev.OrganizerEmail != "" {
		org := ical.NewProp(ical.PropOrganizer)
		org.Value = "mailto:" + ev.OrganizerEmail
		if ev.OrganizerName != "" {
			org.Params[ical.ParamCommonName] = []string{clean(ev.OrganizerName)}
		}
		e.Props.Set(org)
	}
	for _, a := range ev.Attendees {
		if a.Email == "" {
			continue
		}
		p := ical.NewProp(ical.PropAttendee)
		p.Value = "mailto:" + a.Email
		if a.Name != "" {
			p.Params[ical.ParamCommonName] = []string{clean(a.Name)}
		}
		e.Props.Add(p)
	}
	// 提醒（分钟）→ VALARM
	if ev.Reminder > 0 {
		alarm := ical.NewComponent(ical.CompAlarm)
		alarm.Props.SetText(ical.PropAction, "DISPLAY")
		trig := ical.NewProp(ical.PropTrigger)
		trig.Value = fmt.Sprintf("-PT%dM", ev.Reminder)
		trig.SetValueType(ical.ValueDuration)
		alarm.Props.Set(trig)
		alarm.Props.SetText(ical.PropDescription, clean(ev.Subject))
		e.Children = append(e.Children, alarm)
	}
	// 循环规则 → RRULE（必须显式 RECUR 值类型，SetText 会标成 TEXT 导致客户端解析失败）
	if ev.Recurrence != nil {
		if rs := easRecurrenceToRRULE(ev.Recurrence); rs != "" {
			p := ical.NewProp(ical.PropRecurrenceRule)
			p.Value = rs
			p.SetValueType(ical.ValueRecurrence)
			e.Props.Set(p)
		}
	}
	// 例外（删除的实例）→ EXDATE；修改的实例 v1 忽略
	for _, ex := range ev.Exceptions {
		if ex.Deleted {
			p := ical.NewProp(ical.PropExceptionDates)
			p.SetDateTime(ex.ExceptionStartTime.UTC())
			e.Props.Add(p)
		}
	}

	cal.Children = append(cal.Children, e.Component)
	return cal
}

var easDowCodes = []struct {
	bit  eas.DayOfWeek
	code string
}{
	{eas.DowSunday, "SU"}, {eas.DowMonday, "MO"}, {eas.DowTuesday, "TU"},
	{eas.DowWednesday, "WE"}, {eas.DowThursday, "TH"}, {eas.DowFriday, "FR"}, {eas.DowSaturday, "SA"},
}

// easRecurrenceToRRULE 把 EAS Recurrence 映射为 iCal RRULE 字符串。
func easRecurrenceToRRULE(r *eas.Recurrence) string {
	var parts []string
	interval := r.Interval
	if interval <= 0 {
		interval = 1
	}
	byday := func() string {
		var codes []string
		for _, d := range easDowCodes {
			if r.DayOfWeek&d.bit != 0 {
				codes = append(codes, d.code)
			}
		}
		return strings.Join(codes, ",")
	}
	// BYDAY 带周序前缀：2TU / -1MO（WeekOfMonth 5 = last）
	bydayWithPos := func() string {
		var codes []string
		wk := r.WeekOfMonth
		prefix := fmt.Sprintf("%d", wk)
		if wk == 5 {
			prefix = "-1"
		}
		for _, d := range easDowCodes {
			if r.DayOfWeek&d.bit != 0 {
				codes = append(codes, prefix+d.code)
			}
		}
		return strings.Join(codes, ",")
	}

	switch r.Type {
	case eas.RecurrenceDaily:
		parts = append(parts, "FREQ=DAILY")
	case eas.RecurrenceWeekly:
		parts = append(parts, "FREQ=WEEKLY")
		if bd := byday(); bd != "" {
			parts = append(parts, "BYDAY="+bd)
		}
	case eas.RecurrenceMonthlyDate:
		parts = append(parts, "FREQ=MONTHLY")
		if r.DayOfMonth > 0 {
			parts = append(parts, fmt.Sprintf("BYMONTHDAY=%d", r.DayOfMonth))
		}
	case eas.RecurrenceMonthlyByDay:
		parts = append(parts, "FREQ=MONTHLY")
		if bd := bydayWithPos(); bd != "" {
			parts = append(parts, "BYDAY="+bd)
		}
	case eas.RecurrenceYearlyDate:
		parts = append(parts, "FREQ=YEARLY")
		if r.MonthOfYear > 0 {
			parts = append(parts, fmt.Sprintf("BYMONTH=%d", r.MonthOfYear))
		}
		if r.DayOfMonth > 0 {
			parts = append(parts, fmt.Sprintf("BYMONTHDAY=%d", r.DayOfMonth))
		}
	case eas.RecurrenceYearlyByDay:
		parts = append(parts, "FREQ=YEARLY")
		if r.MonthOfYear > 0 {
			parts = append(parts, fmt.Sprintf("BYMONTH=%d", r.MonthOfYear))
		}
		if bd := bydayWithPos(); bd != "" {
			parts = append(parts, "BYDAY="+bd)
		}
	default:
		return ""
	}
	if interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", interval))
	}
	if r.Occurrences > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", r.Occurrences))
	} else if !r.Until.IsZero() {
		parts = append(parts, "UNTIL="+r.Until.UTC().Format("20060102T150405Z"))
	}
	return strings.Join(parts, ";")
}

// ---------- HTTP 服务 ----------

// newCalDAVServer 构建 CalDAV HTTP 服务（Basic + Digest 认证），由 main 启动/优雅关闭。
// Digest 必须存在：macOS CoreDAV 拒绝明文 HTTP 上的 Basic（见 digest.go 头部注释）。
func newCalDAVServer(engine *syncEngine, addr string) *http.Server {
	backend := &caldavBackend{engine: engine}
	handler := &caldav.Handler{Backend: backend}

	cfg := engine.cfg
	dk := newDigestKeys()
	authed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !caldavAuthorized(r, cfg.User, cfg.Password, dk) {
			w.Header().Add("WWW-Authenticate", `Basic realm="eas-bridge"`)
			w.Header().Add("WWW-Authenticate", dk.challenge())
			http.Error(w, "未授权", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})

	log.Printf("[caldav] 监听 %s", addr)
	return &http.Server{Addr: addr, Handler: authed}
}

func errCalDAVReadOnly(op string) error {
	return webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("%s 暂不支持（eas-bridge 日历为只读桥）", op))
}
