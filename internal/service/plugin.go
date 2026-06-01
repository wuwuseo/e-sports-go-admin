package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"

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
	Proxy(ctx context.Context, req *PluginProxyRequest) (*PluginProxyResponse, error)
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

type PluginProxyRequest struct {
	PluginID string
	Method   string
	Path     string
	Query    string
	Header   http.Header
	Body     io.Reader
}

type PluginProxyResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
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

func (s *PluginService) Proxy(ctx context.Context, req *PluginProxyRequest) (*PluginProxyResponse, error) {
	endpoint, err := s.manager.ResolveEndpoint(ctx, req.PluginID)
	if err != nil {
		return nil, err
	}
	if endpoint.Protocol != cmfplugin.ProtocolHTTP {
		return nil, adminplugin.ErrUnsupportedTransport
	}
	client, baseURL, err := proxyHTTPClient(endpoint)
	if err != nil {
		return nil, err
	}
	url := baseURL + normalizedProxyPath(req.Path)
	if req.Query != "" {
		url += "?" + req.Query
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, req.Body)
	if err != nil {
		return nil, err
	}
	httpReq.Header = req.Header.Clone()
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, adminplugin.ErrProxyFailed
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, adminplugin.ErrProxyFailed
	}
	return &PluginProxyResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: body}, nil
}

func proxyHTTPClient(endpoint *adminplugin.Endpoint) (*http.Client, string, error) {
	switch endpoint.Transport {
	case cmfplugin.TransportTCP:
		return http.DefaultClient, "http://" + endpoint.Address, nil
	case cmfplugin.TransportUnix:
		transport := &http.Transport{DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			return net.Dial("unix", endpoint.Socket)
		}}
		return &http.Client{Transport: transport}, "http://unix", nil
	default:
		return nil, "", adminplugin.ErrUnsupportedTransport
	}
}

func normalizedProxyPath(path string) string {
	if path == "" {
		return "/"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
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
