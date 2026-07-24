package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
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

	// 文件夹列表是服务发现所必需的，启动时同步一次。
	log.Println("[sync] 同步文件夹列表...")
	if err := engine.syncFolders(ctx); err != nil {
		log.Fatal("同步文件夹失败: ", err)
	}
	folders := engine.mailFolderIDs()

	// 启动 IMAP 服务
	imapd := newIMAPD(engine)
	go func() {
		if err := imapd.Serve(cfg.IMAPAddr); err != nil {
			log.Fatal("[imapd] ", err)
		}
	}()

	// 启动 SMTP 服务
	go func() {
		if err := serveSMTP(engine, cfg.SMTPAddr); err != nil {
			log.Fatal("[smtpd] ", err)
		}
	}()

	// 启动 CalDAV 服务
	go func() {
		if err := serveCalDAV(engine, cfg.CalDAVAddr); err != nil {
			log.Fatal("[caldav] ", err)
		}
	}()

	// 启动轮询（变更时 fan-out 广播给所有 IDLE 会话）
	go engine.poller(ctx, time.Duration(cfg.PollSecs)*time.Second, func(folderID string) {
		imapd.broadcast(folderID)
	})

	log.Printf("[eas-bridge] 就绪。IMAP %s  SMTP %s  CalDAV %s（Ctrl+C 退出）", cfg.IMAPAddr, cfg.SMTPAddr, cfg.CalDAVAddr)

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
		if err := engine.syncCalendar(ctx); err != nil {
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
}
