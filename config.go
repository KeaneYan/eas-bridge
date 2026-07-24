package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type config struct {
	Server   string `json:"server"`
	User     string `json:"user"`
	Password string `json:"password"`
	IMAPAddr  string `json:"imap_addr"`   // 监听地址，默认 "127.0.0.1:1143"
	SMTPAddr  string `json:"smtp_addr"`   // 监听地址，默认 "127.0.0.1:1025"
	CalDAVAddr string `json:"caldav_addr"` // 监听地址，默认 "127.0.0.1:8008"
	PollSecs  int    `json:"poll_seconds"` // 邮件同步间隔，默认 60
}

func configDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "eas-bridge")
}

func configPath() string { return filepath.Join(configDir(), "config.json") }
func statePath() string  { return filepath.Join(configDir(), "state.json") }
func mimeCacheDir() string { return filepath.Join(configDir(), "mimecache") }

func loadConfig() (*config, error) {
	cfg := &config{}
	b, err := os.ReadFile(configPath())
	if err != nil {
		return nil, fmt.Errorf("未找到配置（请先运行: eas-bridge --init）: %w", err)
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("config.json 损坏: %w", err)
	}
	if cfg.Server == "" || cfg.User == "" || cfg.Password == "" {
		return nil, fmt.Errorf("config.json 缺少 server/user/password")
	}
	if cfg.IMAPAddr == "" {
		cfg.IMAPAddr = "127.0.0.1:1143"
	}
	if cfg.SMTPAddr == "" {
		cfg.SMTPAddr = "127.0.0.1:1025"
	}
	if cfg.CalDAVAddr == "" {
		cfg.CalDAVAddr = "127.0.0.1:8008"
	}
	// H4 防护：明文认证服务只应绑回环——非回环地址拒绝启动
	for _, a := range []string{cfg.IMAPAddr, cfg.SMTPAddr, cfg.CalDAVAddr} {
		if !isLoopbackAddr(a) {
			return nil, fmt.Errorf("监听地址 %s 非回环：eas-bridge 使用明文认证，只允许 127.0.0.1/localhost（确需对外请自行加 TLS 后再改）", a)
		}
	}
	if cfg.PollSecs <= 0 {
		cfg.PollSecs = 60
	}
	return cfg, nil
}

// isLoopbackAddr 判断 host:port 是否绑定回环地址（含省略 host 的形式 ":1143" 一律视为非回环）。
func isLoopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func initConfig() error {
	os.MkdirAll(configDir(), 0700)
	cfg := &config{
		IMAPAddr: "127.0.0.1:1143",
		SMTPAddr: "127.0.0.1:1025",
		PollSecs: 60,
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(configPath(), b, 0600); err != nil {
		return err
	}
	fmt.Println("已创建配置文件:", configPath())
	fmt.Println("请填写 server / user / password 后重新运行（不带 --init）")
	return nil
}
