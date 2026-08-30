package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	adminplugin "go-admin/internal/plugin"
	"go-admin/pluginapi"

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
	ID           string                    `json:"id"`
	Name         string                    `json:"name"`
	Version      string                    `json:"version"`
	Author       string                    `json:"author"`
	Description  string                    `json:"description"`
	Status       string                    `json:"status"`
	InstallPath  string                    `json:"install_path"`
	Manifest     *cmfplugin.PluginManifest `json:"manifest,omitempty" swaggerignore:"true"`
	ErrorMessage string                    `json:"error_message,omitempty"`
	InstalledAt  time.Time                 `json:"installed_at"`
	EnabledAt    *time.Time                `json:"enabled_at,omitempty"`
	UpdatedAt    time.Time                 `json:"updated_at"`
	Restarting   bool                      `json:"restarting"`
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

const (
	maxPluginProxyBody = pluginapi.MaxMessageSize
	pluginProxyTimeout = 30 * time.Second
)

func (s *PluginService) Install(ctx context.Context, req *InstallPluginRequest) (*PluginResponse, error) {
	info, err := s.manager.Install(ctx, req.PackagePath)
	if err != nil {
		return nil, err
	}
	return s.toPluginResponse(ctx, info)
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
		item, err := s.toPluginResponse(ctx, &items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, nil
}

func (s *PluginService) Get(ctx context.Context, pluginID string) (*PluginResponse, error) {
	info, err := s.manager.Get(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	return s.toPluginResponse(ctx, info)
}

func (s *PluginService) Proxy(ctx context.Context, req *PluginProxyRequest) (*PluginProxyResponse, error) {
	if req == nil {
		return nil, adminplugin.ErrProxyFailed
	}
	if reservedPluginControlPath(req.Path) {
		return nil, adminplugin.ErrPermissionDenied
	}
	proxyCtx, cancel := context.WithTimeout(ctx, pluginProxyTimeout)
	defer cancel()
	if err := s.manager.Authorize(proxyCtx, req.PluginID, req.Method, normalizedProxyPath(req.Path)); err != nil {
		return nil, err
	}
	endpoint, err := s.manager.ResolveEndpoint(proxyCtx, req.PluginID)
	if err != nil {
		return nil, err
	}
	body, err := readPluginBody(req.Body)
	if err != nil {
		return nil, adminplugin.ErrProxyFailed
	}
	switch endpoint.Protocol {
	case cmfplugin.ProtocolHTTP:
		request := *req
		request.Body = bytes.NewReader(body)
		return s.proxyHTTP(proxyCtx, endpoint, &request)
	case cmfplugin.ProtocolGRPC, cmfplugin.ProtocolStdio:
		response, err := s.manager.Invoke(proxyCtx, req.PluginID, &pluginapi.ProxyRequest{
			Method:  req.Method,
			Path:    normalizedProxyPath(req.Path),
			Query:   req.Query,
			Headers: toPluginHeaders(req.Header),
			Body:    body,
		})
		if err != nil {
			return nil, adminplugin.ErrProxyFailed
		}
		if response.GetStatusCode() < 100 || response.GetStatusCode() > 599 || len(response.GetBody()) > maxPluginProxyBody {
			return nil, adminplugin.ErrProxyFailed
		}
		return &PluginProxyResponse{
			StatusCode: int(response.GetStatusCode()),
			Header:     fromPluginHeaders(response.GetHeaders()),
			Body:       response.GetBody(),
		}, nil
	default:
		return nil, adminplugin.ErrUnsupportedTransport
	}
}

func (s *PluginService) proxyHTTP(ctx context.Context, endpoint *adminplugin.Endpoint, req *PluginProxyRequest) (*PluginProxyResponse, error) {
	client, baseURL, err := proxyHTTPClient(endpoint)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	url := baseURL + normalizedProxyPath(req.Path)
	if req.Query != "" {
		url += "?" + req.Query
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, url, req.Body)
	if err != nil {
		return nil, err
	}
	httpReq.Header = req.Header.Clone()
	if endpoint.Token != "" {
		httpReq.Header.Set(pluginapi.TokenHeader, endpoint.Token)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, adminplugin.ErrProxyFailed
	}
	defer resp.Body.Close()
	body, err := readPluginBody(resp.Body)
	if err != nil {
		return nil, adminplugin.ErrProxyFailed
	}
	return &PluginProxyResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Body: body}, nil
}

func readPluginBody(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxPluginProxyBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPluginProxyBody {
		return nil, errors.New("plugin body is too large")
	}
	return body, nil
}

func toPluginHeaders(headers http.Header) []*pluginapi.Header {
	result := make([]*pluginapi.Header, 0, len(headers))
	for name, values := range headers {
		result = append(result, &pluginapi.Header{Name: name, Values: append([]string(nil), values...)})
	}
	return result
}

func fromPluginHeaders(headers []*pluginapi.Header) http.Header {
	result := make(http.Header, len(headers))
	for _, header := range headers {
		if header == nil || header.GetName() == "" {
			continue
		}
		for _, value := range header.GetValues() {
			result.Add(header.GetName(), value)
		}
	}
	return result
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
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		return client, "http://" + endpoint.Address, nil
	case cmfplugin.TransportUnix:
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport := &http.Transport{DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", endpoint.Socket)
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

func reservedPluginControlPath(value string) bool {
	for range 3 {
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return true
		}
		value = decoded
	}
	value = normalizedProxyPath(value)
	return pluginapi.IsControlPath(value) || value == "/_go-admin" || strings.HasPrefix(value, "/_go-admin/")
}

type pluginDiagnosticsProvider interface {
	Diagnostics(context.Context, string) (*adminplugin.Diagnostics, error)
}

func (s *PluginService) toPluginResponse(ctx context.Context, info *cmfplugin.PluginInfo) (*PluginResponse, error) {
	response := &PluginResponse{
		ID:          info.ID,
		Name:        info.Name,
		Version:     info.Version,
		Author:      info.Author,
		Description: info.Description,
		Status:      string(info.Status),
		InstallPath: info.InstallPath,
		Manifest:    info.Manifest,
	}
	if provider, ok := s.manager.(pluginDiagnosticsProvider); ok {
		diagnostics, err := provider.Diagnostics(ctx, info.ID)
		if err != nil {
			return nil, err
		}
		response.ErrorMessage = diagnostics.ErrorMessage
		response.InstalledAt = diagnostics.InstalledAt
		response.EnabledAt = diagnostics.EnabledAt
		response.UpdatedAt = diagnostics.UpdatedAt
		response.Restarting = diagnostics.Restarting
	}
	return response, nil
}
