package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

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
	Manifest    *cmfplugin.PluginManifest `json:"manifest,omitempty" swaggerignore:"true"`
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
		if err := isPluginAddressSafe(endpoint.Address); err != nil {
			return nil, "", adminplugin.ErrInvalidPath
		}
		dialer := &net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		client := &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					host, _, err := net.SplitHostPort(addr)
					if err != nil {
						return nil, err
					}
					if err := isPluginAddressSafe(host); err != nil {
						return nil, err
					}
					return dialer.DialContext(ctx, network, addr)
				},
				MaxIdleConns:    10,
				IdleConnTimeout: 60 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("超过最大重定向次数")
				}
				if req.URL != nil {
					if err := isPluginAddressSafe(req.URL.Host); err != nil {
						return err
					}
				}
				return nil
			},
		}
		return client, "http://" + endpoint.Address, nil
	case cmfplugin.TransportUnix:
		transport := &http.Transport{DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			return net.Dial("unix", endpoint.Socket)
		}}
		return &http.Client{Transport: transport, Timeout: 30 * time.Second}, "http://unix", nil
	default:
		return nil, "", adminplugin.ErrUnsupportedTransport
	}
}

// isPluginAddressSafe 校验插件代理目标地址是否安全
// 仅允许 loopback 地址（127.0.0.0/8 和 ::1）和 localhost 域名，确保插件代理只能访问本地服务
func isPluginAddressSafe(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}

	ip := net.ParseIP(host)
	if ip == nil {
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		return fmt.Errorf("不允许的插件地址: %s", host)
	}

	if !ip.IsLoopback() {
		return fmt.Errorf("插件地址必须为本地回环地址: %s", ip.String())
	}

	return nil
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
