// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// PingClient is a hand-written test double for [eas.PingClient].
type PingClient struct {
	PingFunc func(ctx context.Context, heartbeatSeconds int, folders []eas.PingFolder) (*eas.PingResult, error)
}

func (m *PingClient) Ping(ctx context.Context, heartbeatSeconds int, folders []eas.PingFolder) (*eas.PingResult, error) {
	if m.PingFunc != nil {
		return m.PingFunc(ctx, heartbeatSeconds, folders)
	}
	return nil, errors.New("easmock: Ping not implemented")
}

var _ eas.PingClient = (*PingClient)(nil)
