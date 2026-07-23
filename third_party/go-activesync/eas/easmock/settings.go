// Copyright (C) 2026 Henry Stern
// SPDX-License-Identifier: MIT

package easmock

import (
	"context"
	"errors"

	"github.com/hstern/go-activesync/eas"
)

// SettingsClient is a hand-written test double for [eas.SettingsClient].
type SettingsClient struct {
	GetOofFunc                       func(ctx context.Context) (*eas.OofConfig, error)
	SetOofFunc                       func(ctx context.Context, cfg eas.OofConfig) error
	SetDevicePasswordFunc            func(ctx context.Context, newPassword string) error
	GetUserInformationFunc           func(ctx context.Context) (*eas.UserInformation, error)
	SettingsDeviceInformationFunc    func(ctx context.Context, info eas.DeviceInformation) error
	GetRightsManagementTemplatesFunc func(ctx context.Context) ([]eas.RightsTemplate, error)
}

func (m *SettingsClient) GetOof(ctx context.Context) (*eas.OofConfig, error) {
	if m.GetOofFunc != nil {
		return m.GetOofFunc(ctx)
	}
	return nil, errors.New("easmock: GetOof not implemented")
}

func (m *SettingsClient) SetOof(ctx context.Context, cfg eas.OofConfig) error {
	if m.SetOofFunc != nil {
		return m.SetOofFunc(ctx, cfg)
	}
	return errors.New("easmock: SetOof not implemented")
}

func (m *SettingsClient) SetDevicePassword(ctx context.Context, newPassword string) error {
	if m.SetDevicePasswordFunc != nil {
		return m.SetDevicePasswordFunc(ctx, newPassword)
	}
	return errors.New("easmock: SetDevicePassword not implemented")
}

func (m *SettingsClient) GetUserInformation(ctx context.Context) (*eas.UserInformation, error) {
	if m.GetUserInformationFunc != nil {
		return m.GetUserInformationFunc(ctx)
	}
	return nil, errors.New("easmock: GetUserInformation not implemented")
}

func (m *SettingsClient) SettingsDeviceInformation(ctx context.Context, info eas.DeviceInformation) error {
	if m.SettingsDeviceInformationFunc != nil {
		return m.SettingsDeviceInformationFunc(ctx, info)
	}
	return errors.New("easmock: SettingsDeviceInformation not implemented")
}

func (m *SettingsClient) GetRightsManagementTemplates(ctx context.Context) ([]eas.RightsTemplate, error) {
	if m.GetRightsManagementTemplatesFunc != nil {
		return m.GetRightsManagementTemplatesFunc(ctx)
	}
	return nil, errors.New("easmock: GetRightsManagementTemplates not implemented")
}

var _ eas.SettingsClient = (*SettingsClient)(nil)
