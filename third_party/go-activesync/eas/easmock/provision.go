// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// ProvisionClient is a hand-written test double for [eas.ProvisionClient].
type ProvisionClient struct {
	ProvisionFunc             func(ctx context.Context) error
	AcknowledgeRemoteWipeFunc func(ctx context.Context, status int) error
	NegotiateVersionFunc      func(ctx context.Context) (string, error)
	OptionsFunc               func(ctx context.Context) (*eas.OptionsResult, error)
}

func (m *ProvisionClient) Provision(ctx context.Context) error {
	if m.ProvisionFunc != nil {
		return m.ProvisionFunc(ctx)
	}
	return errors.New("easmock: Provision not implemented")
}

func (m *ProvisionClient) AcknowledgeRemoteWipe(ctx context.Context, status int) error {
	if m.AcknowledgeRemoteWipeFunc != nil {
		return m.AcknowledgeRemoteWipeFunc(ctx, status)
	}
	return errors.New("easmock: AcknowledgeRemoteWipe not implemented")
}

func (m *ProvisionClient) NegotiateVersion(ctx context.Context) (string, error) {
	if m.NegotiateVersionFunc != nil {
		return m.NegotiateVersionFunc(ctx)
	}
	return "", errors.New("easmock: NegotiateVersion not implemented")
}

func (m *ProvisionClient) Options(ctx context.Context) (*eas.OptionsResult, error) {
	if m.OptionsFunc != nil {
		return m.OptionsFunc(ctx)
	}
	return nil, errors.New("easmock: Options not implemented")
}

var _ eas.ProvisionClient = (*ProvisionClient)(nil)
