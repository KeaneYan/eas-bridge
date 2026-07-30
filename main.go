package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	smtp "github.com/emersion/go-smtp"
)

func main() {
	initFlag := flag.Bool("init", false, "初始化配置文件后退出")
	flag.Parse()

	if *initFlag {
		if err := initConfig(); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("[eas-bridge] 用户=%s 服务器=%s", cfg.User, cfg.Server)

	engine, err := newSyncEngine(cfg)
	if err != nil {
		log.Fatal("初始化同步引擎失败: ", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.scheduleCachePrune(ctx)

	// 文件夹列表是服务发现所必需的，启动时同步一次。
	log.Println("[sync] 同步文件夹列表...")
	if err := engine.syncFolders(ctx); err != nil {
		log.Fatal("同步文件夹失败: ", err)
	}
	folders := engine.mailFolderIDs()

	// 启动 IMAP 服务
	imapD := newIMAPD(engine)
	go imapD.monitorConnections(ctx, 10*time.Minute)
	go func() {
		// Serve 正常被 Shutdown 时返回 nil；若信号抢在监听建立前到达会
		// 返回 go-imap 未导出的 errClosed——用 ShuttingDown 兜底区分
		if err := imapD.Serve(cfg.IMAPAddr); err != nil && !imapD.ShuttingDown() {
			log.Fatal("[imapd] ", err)
		}
	}()

	// 启动 SMTP 服务
	smtpSrv := newSMTPServer(engine, cfg.SMTPAddr, ctx)
	go func() {
		log.Printf("[smtpd] 监听 %s", cfg.SMTPAddr)
		if err := smtpSrv.ListenAndServe(); err != nil && !errors.Is(err, smtp.ErrServerClosed) {
			log.Fatal("[smtpd] ", err)
		}
	}()

	// 启动 CalDAV 服务
	calBackend := &caldavBackend{engine: engine, lifecycleCtx: ctx}
	calSrv := newCalDAVServer(calBackend, cfg.CalDAVAddr)
	go func() {
		if err := calSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("[caldav] ", err)
		}
	}()

	// 启动邮件和日历轮询。邮件变更时 fan-out 广播给所有 IDLE 会话；
	// 日历独立后台拉取，不依赖 CalDAV 客户端是否打开。
	go engine.poller(ctx, time.Duration(cfg.PollSecs)*time.Second, func(folderID string) {
		imapD.broadcast(folderID)
	})
	go calBackend.calendarPoller(ctx, time.Duration(cfg.CalendarPollSecs)*time.Second)

	log.Printf(
		"[eas-bridge] 就绪。IMAP %s  SMTP %s  CalDAV %s（邮件轮询 %ds，日历轮询 %ds；Ctrl+C 退出）",
		cfg.IMAPAddr,
		cfg.SMTPAddr,
		cfg.CalDAVAddr,
		cfg.PollSecs,
		cfg.CalendarPollSecs,
	)

	// 邮件与日历预热放到后台，避免大邮箱或多年日历阻塞三个本地服务启动。
	go func() {
		for _, fid := range folders {
			if err := engine.syncMail(ctx, fid); err != nil {
				log.Printf("[sync] 文件夹 %s 预热失败: %v", fid, err)
			}
		}
		log.Println("[sync] 邮件预热完成")
	}()
	go func() {
		if err := calBackend.syncCalendar(ctx); err != nil {
			log.Printf("[sync] 日历预热失败（查询时会重试）: %v", err)
			return
		}
		engine.st.mu.Lock()
		log.Printf("[sync] 日历预热完成，%d 个事件", len(engine.st.Events))
		engine.st.mu.Unlock()
	}()

	// 等待退出信号
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println()
	log.Println("[eas-bridge] 正在退出...")

	// 优雅退出（full-review ROI-7）：先停后台同步，再关三个服务。
	// SMTP/CalDAV 渐进退出（等在途请求，最多 10s）；go-imap 无渐进语义只能直接断。
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := calSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[caldav] 优雅关闭失败: %v", err)
	}
	if err := smtpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("[smtpd] 优雅关闭失败: %v", err)
	}
	imapD.Shutdown()
	log.Println("[eas-bridge] 已退出")
}
