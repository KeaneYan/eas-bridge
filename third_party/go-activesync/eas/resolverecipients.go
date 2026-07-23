// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"
	"time"

	"github.com/hstern/go-activesync/wbxml"
)

// ResolveOptions controls a ResolveRecipients request.
type ResolveOptions struct {
	// CertificateRetrieval enables S/MIME cert lookup for each recipient.
	// 1=NoCertificate (default), 2=Full, 3=Mini.
	CertificateRetrieval int
	// MaxCertificates caps the cert count per recipient (default 99999).
	MaxCertificates int
	// MaxAmbiguousRecipients caps how many candidates an ambiguous name
	// returns. Default 100.
	MaxAmbiguousRecipients int
	// Availability requests free/busy data for each resolved recipient
	// in the given window. Both fields must be set to enable.
	AvailabilityStart time.Time
	AvailabilityEnd   time.Time
	// PictureMaxBytes requests a contact picture if non-zero.
	PictureMaxBytes int
}

// ResolvedRecipient is one entry in the ResolveRecipients response.
type ResolvedRecipient struct {
	// Type is 1=GAL, 2=ContactsFolder.
	Type         int
	DisplayName  string
	EmailAddress string
	// Certificates lists raw S/MIME certificate bytes (one per cert).
	Certificates [][]byte
	// MergedFreeBusy is the EAS free/busy string when Availability was
	// requested: each character is a 30-minute slot, "0"=free, "1"=tentative,
	// "2"=busy, "3"=OOF, "4"=workingElsewhere.
	MergedFreeBusy string
	// Picture, if requested and present.
	Picture []byte
}

// ResolveResponse is the per-input-recipient envelope.
type ResolveResponse struct {
	To         string
	Status     int
	Recipients []ResolvedRecipient
}

// ResolveRecipients asks the server to resolve and disambiguate one or
// more email addresses or display-name strings against the GAL and
// the user's contacts. Returns one ResolveResponse per input string.
func (c *httpClient) ResolveRecipients(ctx context.Context, recipients []string, opts ResolveOptions) ([]ResolveResponse, error) {
	if len(recipients) == 0 {
		return nil, errors.New("eas: ResolveRecipients: at least one recipient required")
	}
	root := wbxml.E(wbxml.PageResolveRecipients, "ResolveRecipients")
	for _, r := range recipients {
		root.Children = append(root.Children, wbxml.E(wbxml.PageResolveRecipients, "To", wbxml.Text(r)))
	}
	options := wbxml.E(wbxml.PageResolveRecipients, "Options")
	if opts.CertificateRetrieval > 0 {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageResolveRecipients, "CertificateRetrieval", wbxml.Text(itoa(opts.CertificateRetrieval))))
	}
	if opts.MaxCertificates > 0 {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageResolveRecipients, "MaxCertificates", wbxml.Text(itoa(opts.MaxCertificates))))
	}
	if opts.MaxAmbiguousRecipients > 0 {
		options.Children = append(options.Children,
			wbxml.E(wbxml.PageResolveRecipients, "MaxAmbiguousRecipients", wbxml.Text(itoa(opts.MaxAmbiguousRecipients))))
	}
	if !opts.AvailabilityStart.IsZero() && !opts.AvailabilityEnd.IsZero() {
		options.Children = append(options.Children, wbxml.E(wbxml.PageResolveRecipients, "Availability",
			wbxml.E(wbxml.PageResolveRecipients, "StartTime", wbxml.Text(formatEASTime(opts.AvailabilityStart))),
			wbxml.E(wbxml.PageResolveRecipients, "EndTime", wbxml.Text(formatEASTime(opts.AvailabilityEnd))),
		))
	}
	if opts.PictureMaxBytes > 0 {
		options.Children = append(options.Children, wbxml.E(wbxml.PageResolveRecipients, "Picture",
			wbxml.E(wbxml.PageResolveRecipients, "MaxSize", wbxml.Text(itoa(opts.PictureMaxBytes))),
		))
	}
	if len(options.Children) > 0 {
		root.Children = append(root.Children, options)
	}

	resp, err := c.post(ctx, "ResolveRecipients", &wbxml.Document{Root: root})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: ResolveRecipients: empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "ResolveRecipients", Code: st}
	}
	var out []ResolveResponse
	for _, c := range resp.Root.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Name != "Response" {
			continue
		}
		r := ResolveResponse{}
		if to := el.Find("To"); to != nil {
			r.To = to.TextContent()
		}
		if st := el.Find("Status"); st != nil {
			r.Status = atoi(st.TextContent())
		}
		for _, rc := range el.FindAll("Recipient") {
			parsed := ResolvedRecipient{}
			if t := rc.Find("Type"); t != nil {
				parsed.Type = atoi(t.TextContent())
			}
			if dn := rc.Find("DisplayName"); dn != nil {
				parsed.DisplayName = dn.TextContent()
			}
			if ea := rc.Find("EmailAddress"); ea != nil {
				parsed.EmailAddress = ea.TextContent()
			}
			if certs := rc.Find("Certificates"); certs != nil {
				for _, cc := range certs.Children {
					ce, ok := cc.(*wbxml.Element)
					if !ok || ce.Name != "Certificate" {
						continue
					}
					if op := firstOpaque(ce); op != nil {
						parsed.Certificates = append(parsed.Certificates, op)
					}
				}
			}
			if av := rc.Find("Availability"); av != nil {
				if mfb := av.Find("MergedFreeBusy"); mfb != nil {
					parsed.MergedFreeBusy = mfb.TextContent()
				}
			}
			if pic := rc.Find("Picture"); pic != nil {
				if op := firstOpaque(pic); op != nil {
					parsed.Picture = op
				}
			}
			r.Recipients = append(r.Recipients, parsed)
		}
		out = append(out, r)
	}
	return out, nil
}
