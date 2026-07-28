package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hstern/go-activesync/eas"
	"github.com/hstern/go-activesync/eas/easmock"
)

func TestSMTPSessionDataUsesLifecycleContextAndResets(t *testing.T) {
	var got eas.SendMailOptions
	engine := &syncEngine{
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				SendMailFunc: func(_ context.Context, opts eas.SendMailOptions) error {
					got = opts
					return nil
				},
			},
		},
	}
	sess := &smtpSession{
		engine:       engine,
		lifecycleCtx: context.Background(),
		authed:       true,
		from:         "sender@example.com",
		rcpts:        []string{"recipient@example.com"},
	}
	message := []byte("From: sender@example.com\r\nTo: recipient@example.com\r\n\r\nhello")
	if err := sess.Data(bytes.NewReader(message)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.MIME, message) {
		t.Fatalf("SendMail MIME = %q, want %q", got.MIME, message)
	}
	if sess.from != "" || len(sess.rcpts) != 0 {
		t.Fatalf("successful send did not reset envelope: from=%q rcpts=%v", sess.from, sess.rcpts)
	}
}

func TestSMTPSessionDataStopsWhenDaemonShutsDown(t *testing.T) {
	lifecycleCtx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := &syncEngine{
		c: &easmock.Client{
			EmailClient: easmock.EmailClient{
				SendMailFunc: func(ctx context.Context, _ eas.SendMailOptions) error {
					<-ctx.Done()
					return ctx.Err()
				},
			},
		},
	}
	sess := &smtpSession{
		engine:       engine,
		lifecycleCtx: lifecycleCtx,
		authed:       true,
		from:         "sender@example.com",
		rcpts:        []string{"recipient@example.com"},
	}
	err := sess.Data(strings.NewReader("Subject: test\r\n\r\nbody"))
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("Data error = %v, want lifecycle cancellation", err)
	}
}

func TestSMTPServerHasConnectionTimeouts(t *testing.T) {
	srv := newSMTPServer(&syncEngine{}, "127.0.0.1:0", context.Background())
	if srv.ReadTimeout != 5*time.Minute {
		t.Fatalf("ReadTimeout = %v, want 5m", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 5*time.Minute {
		t.Fatalf("WriteTimeout = %v, want 5m", srv.WriteTimeout)
	}
	if !srv.AllowInsecureAuth {
		t.Fatal("localhost SMTP must retain insecure AUTH support")
	}
}
