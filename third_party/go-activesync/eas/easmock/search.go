// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// SearchClient is a hand-written test double for [eas.SearchClient].
type SearchClient struct {
	GALSearchFunc         func(ctx context.Context, query string, limit int) (*eas.GALSearchResult, error)
	ResolveRecipientsFunc func(ctx context.Context, recipients []string, opts eas.ResolveOptions) ([]eas.ResolveResponse, error)
	ValidateCertFunc      func(ctx context.Context, certs, chain [][]byte, checkCRL bool) ([]eas.CertValidation, error)
}

func (m *SearchClient) GALSearch(ctx context.Context, query string, limit int) (*eas.GALSearchResult, error) {
	if m.GALSearchFunc != nil {
		return m.GALSearchFunc(ctx, query, limit)
	}
	return nil, errors.New("easmock: GALSearch not implemented")
}

func (m *SearchClient) ResolveRecipients(ctx context.Context, recipients []string, opts eas.ResolveOptions) ([]eas.ResolveResponse, error) {
	if m.ResolveRecipientsFunc != nil {
		return m.ResolveRecipientsFunc(ctx, recipients, opts)
	}
	return nil, errors.New("easmock: ResolveRecipients not implemented")
}

func (m *SearchClient) ValidateCert(ctx context.Context, certs, chain [][]byte, checkCRL bool) ([]eas.CertValidation, error) {
	if m.ValidateCertFunc != nil {
		return m.ValidateCertFunc(ctx, certs, chain, checkCRL)
	}
	return nil, errors.New("easmock: ValidateCert not implemented")
}

var _ eas.SearchClient = (*SearchClient)(nil)
