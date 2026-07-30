package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/hstern/go-activesync/eas"
)

// smtpBackend 实现 go-smtp Backend：AUTH PLAIN 鉴权 + MIME 透传到 EAS SendMail。
type smtpBackend struct {
	engine       *syncEngine
	lifecycleCtx context.Context
}

func (b *smtpBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &smtpSession{engine: b.engine, lifecycleCtx: b.lifecycleCtx}, nil
}

type smtpSession struct {
	engine       *syncEngine
	lifecycleCtx context.Context
	authed       bool
	from         string
	rcpts        []string
}

func (s *smtpSession) AuthMechanisms() []string {
	return []string{"PLAIN"}
}

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	if mech != "PLAIN" {
		return nil, fmt.Errorf("仅支持 PLAIN 认证")
	}
	cfg := s.engine.cfg
	return sasl.NewPlainServer(func(identity, username, password string) error {
		if username == cfg.User && password == cfg.Password {
			s.authed = true
			return nil
		}
		return fmt.Errorf("认证失败")
	}), nil
}

func (s *smtpSession) Reset() {
	s.from = ""
	s.rcpts = nil
}

func (s *smtpSession) Logout() error { return nil }

func (s *smtpSession) Mail(from string, opts *smtp.MailOptions) error {
	if !s.authed {
		return fmt.Errorf("请先 AUTH")
	}
	s.from = from
	s.rcpts = nil
	return nil
}

func (s *smtpSession) Rcpt(to string, opts *smtp.RcptOptions) error {
	if !s.authed {
		return fmt.Errorf("请先 AUTH")
	}
	if len(s.rcpts) >= 100 {
		return fmt.Errorf("收件人过多")
	}
	s.rcpts = append(s.rcpts, to)
	return nil
}

// Data 接收完整 MIME 报文，透传到 EAS SendMail。
func (s *smtpSession) Data(r io.Reader) error {
	if !s.authed {
		return fmt.Errorf("请先 AUTH")
	}
	if s.from == "" || len(s.rcpts) == 0 {
		return fmt.Errorf("缺少 MAIL FROM 或 RCPT TO")
	}
	mime, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if len(mime) == 0 {
		return fmt.Errorf("空报文")
	}
	log.Printf("[smtpd] 发送: from=%s rcpts=%d size=%d", s.from, len(s.rcpts), len(mime))
	parent := s.lifecycleCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, easRequestTimeout)
	defer cancel()
	err = s.engine.c.SendMail(ctx, eas.SendMailOptions{MIME: mime})
	if err != nil {
		log.Printf("[smtpd] EAS SendMail 失败: %v", err)
		return fmt.Errorf("发送失败: %v", err)
	}
	log.Printf("[smtpd] 发送成功")
	s.Reset()
	return nil
}

// ServeSMTP 启动 SMTP 监听（阻塞）。
func newSMTPServer(engine *syncEngine, addr string, lifecycleCtx context.Context) *smtp.Server {
	be := &smtpBackend{engine: engine, lifecycleCtx: lifecycleCtx}
	s := smtp.NewServer(be)
	s.Addr = addr
	s.Domain = "localhost"
	s.AllowInsecureAuth = true // 仅 localhost 监听
	s.MaxMessageBytes = 50 << 20
	s.MaxRecipients = 100
	s.ReadTimeout = 5 * time.Minute
	s.WriteTimeout = 5 * time.Minute
	return s
}
