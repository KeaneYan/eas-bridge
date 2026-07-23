// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/hstern/go-activesync/wbxml"
)

// SendMailOptions controls a SendMail / SmartReply / SmartForward call.
type SendMailOptions struct {
	// MIME is the full RFC 5322 message bytes. Required.
	MIME []byte
	// SaveInSent toggles whether the server stores a copy in Sent Items.
	// Default true.
	SaveInSent bool
	// ClientID is an opaque idempotency key. If empty, a random 32-hex
	// value is generated; the same ClientID may be retried safely if a
	// previous attempt's status is unknown.
	ClientID string
	// SkipSaveInSent inverts SaveInSent without forcing the caller to
	// pass false explicitly (Go zero-value is false, which we want to
	// mean "save in sent" by default).
	SkipSaveInSent bool
}

// ReplyForwardOptions controls a SmartReply or SmartForward call.
type ReplyForwardOptions struct {
	SendMailOptions
	// FolderID and ServerID identify the source message being replied
	// to or forwarded.
	FolderID string
	ServerID string
}

// SendMail sends a new RFC 5322 message via the EAS SendMail command.
// Returns nil on success.
func (c *httpClient) SendMail(ctx context.Context, opts SendMailOptions) error {
	if len(opts.MIME) == 0 {
		return errors.New("eas: SendMail: MIME is required")
	}
	clientID := orRandomID(opts.ClientID)
	doc := &wbxml.Document{
		Root: wbxml.E(wbxml.PageComposeMail, "SendMail",
			wbxml.E(wbxml.PageComposeMail, "ClientId", wbxml.Text(clientID)),
		),
	}
	if !opts.SkipSaveInSent {
		doc.Root.Children = append(doc.Root.Children,
			wbxml.E(wbxml.PageComposeMail, "SaveInSentItems"),
		)
	}
	doc.Root.Children = append(doc.Root.Children,
		wbxml.E(wbxml.PageComposeMail, "Mime", wbxml.Opaque(opts.MIME)),
	)
	return c.sendMailLike(ctx, "SendMail", doc)
}

// SmartReply sends a reply that the server merges with the original
// message (preserving threading and headers it already has).
func (c *httpClient) SmartReply(ctx context.Context, opts ReplyForwardOptions) error {
	return c.smartReplyOrForward(ctx, "SmartReply", opts)
}

// SmartForward forwards a message; like SmartReply, the server expands
// the original headers/body so the client only sends the new content.
func (c *httpClient) SmartForward(ctx context.Context, opts ReplyForwardOptions) error {
	return c.smartReplyOrForward(ctx, "SmartForward", opts)
}

func (c *httpClient) smartReplyOrForward(ctx context.Context, cmd string, opts ReplyForwardOptions) error {
	if len(opts.MIME) == 0 {
		return fmt.Errorf("eas: %s: MIME is required", cmd)
	}
	if opts.FolderID == "" || opts.ServerID == "" {
		return fmt.Errorf("eas: %s: FolderID and ServerID are required", cmd)
	}
	clientID := orRandomID(opts.ClientID)
	root := wbxml.E(wbxml.PageComposeMail, cmd,
		wbxml.E(wbxml.PageComposeMail, "ClientId", wbxml.Text(clientID)),
	)
	if !opts.SkipSaveInSent {
		root.Children = append(root.Children,
			wbxml.E(wbxml.PageComposeMail, "SaveInSentItems"),
		)
	}
	root.Children = append(root.Children,
		wbxml.E(wbxml.PageComposeMail, "Source",
			wbxml.E(wbxml.PageComposeMail, "FolderId", wbxml.Text(opts.FolderID)),
			wbxml.E(wbxml.PageComposeMail, "ItemId", wbxml.Text(opts.ServerID)),
		),
		wbxml.E(wbxml.PageComposeMail, "Mime", wbxml.Opaque(opts.MIME)),
	)
	return c.sendMailLike(ctx, cmd, &wbxml.Document{Root: root})
}

// sendMailLike POSTs a SendMail-shaped command. EAS reports success
// either as an empty 200 OK body or as a Status=1 element. Anything
// else is surfaced.
func (c *httpClient) sendMailLike(ctx context.Context, cmd string, doc *wbxml.Document) error {
	resp, err := c.post(ctx, cmd, doc)
	if err != nil {
		return err
	}
	if resp == nil || resp.Root == nil {
		// Empty body == success per MS-ASCMD §2.2.2.16
		return nil
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return &StatusError{Command: cmd, Code: st}
	}
	return nil
}

// orRandomID returns id if non-empty, otherwise generates a 32-hex
// random identifier suitable for a SendMail ClientId.
func orRandomID(id string) string {
	if id != "" {
		return id
	}
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
