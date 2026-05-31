package service

import (
	"context"

	adminplugin "go-admin/internal/plugin"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

type PluginServiceInterface interface {
	Install(ctx context.Context, req *InstallPluginRequest) (*PluginResponse, error)
	Enable(ctx context.Context, pluginID string) error
	Disable(ctx context.Context, pluginID string) error
	Uninstall(ctx context.Context, pluginID string) error
	List(ctx context.Context) ([]PluginResponse, error)
	Get(ctx context.Context, pluginID string) (*PluginResponse, error)
}

type PluginService struct {
	manager adminplugin.Manager
}

func NewPluginService(manager adminplugin.Manager) *PluginService {
	return &PluginService{manager: manager}
}

type InstallPluginRequest struct {
	PackagePath string `json:"package_path" validate:"required"`
}

type PluginResponse struct {
	ID          string                    `json:"id"`
	Name        string                    `json:"name"`
	Version     string                    `json:"version"`
	Author      string                    `json:"author"`
	Description string                    `json:"description"`
	Status      string                    `json:"status"`
	InstallPath string                    `json:"install_path"`
	Manifest    *cmfplugin.PluginManifest `json:"manifest,omitempty"`
}

func (s *PluginService) Install(ctx context.Context, req *InstallPluginRequest) (*PluginResponse, error) {
	info, err := s.manager.Install(ctx, req.PackagePath)
	if err != nil {
		return nil, err
	}
	return toPluginResponse(info), nil
}

func (s *PluginService) Enable(ctx context.Context, pluginID string) error {
	return s.manager.Enable(ctx, pluginID)
}

func (s *PluginService) Disable(ctx context.Context, pluginID string) error {
	return s.manager.Disable(ctx, pluginID)
}

func (s *PluginService) Uninstall(ctx context.Context, pluginID string) error {
	return s.manager.Uninstall(ctx, pluginID)
}

func (s *PluginService) List(ctx context.Context) ([]PluginResponse, error) {
	items, err := s.manager.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PluginResponse, 0, len(items))
	for i := range items {
		out = append(out, *toPluginResponse(&items[i]))
	}
	return out, nil
}

func (s *PluginService) Get(ctx context.Context, pluginID string) (*PluginResponse, error) {
	info, err := s.manager.Get(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	return toPluginResponse(info), nil
}

func toPluginResponse(info *cmfplugin.PluginInfo) *PluginResponse {
	return &PluginResponse{
		ID:          info.ID,
		Name:        info.Name,
		Version:     info.Version,
		Author:      info.Author,
		Description: info.Description,
		Status:      string(info.Status),
		InstallPath: info.InstallPath,
		Manifest:    info.Manifest,
	}
}
