// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package eas

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/wbxml"
)

// CertValidation is the per-certificate result of ValidateCert.
type CertValidation struct {
	// Status is 1 (Success) when the certificate validates against the
	// server's trust chain. Other values per MS-ASCERT §2.2.4.74.
	Status int
}

// ValidateCert asks the server to validate one or more S/MIME
// certificates against its trust chain. Useful when composing encrypted
// or signed mail to recipients whose certificates were retrieved via
// ResolveRecipients.
//
// chain is an optional list of intermediate CAs (DER-encoded). checkCRL
// requests an explicit revocation check.
func (c *httpClient) ValidateCert(ctx context.Context, certs [][]byte, chain [][]byte, checkCRL bool) ([]CertValidation, error) {
	if len(certs) == 0 {
		return nil, errors.New("eas: ValidateCert: at least one certificate required")
	}
	root := wbxml.E(wbxml.PageValidateCert, "ValidateCert")
	if checkCRL {
		root.Children = append(root.Children, wbxml.E(wbxml.PageValidateCert, "CheckCRL", wbxml.Text("1")))
	}
	if len(chain) > 0 {
		chainEl := wbxml.E(wbxml.PageValidateCert, "CertificateChain")
		for _, ca := range chain {
			chainEl.Children = append(chainEl.Children,
				wbxml.E(wbxml.PageValidateCert, "Certificate", wbxml.Opaque(ca)))
		}
		root.Children = append(root.Children, chainEl)
	}
	for _, der := range certs {
		root.Children = append(root.Children,
			wbxml.E(wbxml.PageValidateCert, "Certificate", wbxml.Opaque(der)))
	}

	resp, err := c.post(ctx, "ValidateCert", &wbxml.Document{Root: root})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Root == nil {
		return nil, errors.New("eas: ValidateCert: empty response")
	}
	if st := topStatus(resp.Root); st != 0 && st != StatusOK {
		return nil, &StatusError{Command: "ValidateCert", Code: st}
	}
	var out []CertValidation
	for _, c := range resp.Root.Children {
		el, ok := c.(*wbxml.Element)
		if !ok || el.Name != "Certificate" {
			continue
		}
		v := CertValidation{}
		if st := el.Find("Status"); st != nil {
			v.Status = atoi(st.TextContent())
		}
		out = append(out, v)
	}
	return out, nil
}
