package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"go-admin/internal/tasks"
)

// Executor 定义执行器接口
type Executor interface {
	Execute(ctx context.Context, command string, logger io.Writer) error
}

// CommandExecutor 基于白名单注册表的安全命令执行器
type CommandExecutor struct{}

// Execute 从注册表中查找命令并使用 exec.Command 直接执行（不经过 shell）
func (e *CommandExecutor) Execute(ctx context.Context, command string, logger io.Writer) error {
	// 解析命令名和参数：第一段为注册名，后续为参数
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return fmt.Errorf("命令不能为空")
	}

	name := parts[0]
	args := parts[1:]

	// 从注册表查找命令
	registered, ok := Get(name)
	if !ok {
		return fmt.Errorf("命令 '%s' 未注册，请在代码中通过 cron.Register() 注册后再使用", name)
	}

	// 验证参数数量
	if err := registered.ValidateArgs(args); err != nil {
		return err
	}

	// 使用 exec.Command 直接执行，不经过 shell 解释器，防止命令注入
	cmd := exec.CommandContext(ctx, registered.Path, args...)
	cmd.Stdout = logger
	cmd.Stderr = logger
	return cmd.Run()
}

// CurlExecutor 执行 HTTP 请求
type CurlExecutor struct{}

// curlCommand 定义 curl 命令的 JSON 格式
type curlCommand struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // 秒
}

// isSafeHost 判断主机（IP或域名）是否安全，阻止内网和云元数据服务访问
func isSafeHost(host string) error {
	ip := net.ParseIP(host)
	if ip == nil {
		// 不是IP地址，检查是否为本地域名
		localDomains := []string{"localhost", "localhost.localdomain", "local"}
		for _, domain := range localDomains {
			if strings.EqualFold(host, domain) {
				return fmt.Errorf("不允许访问本地域名: %s", host)
			}
		}
		return nil
	}

	// 禁止内网IP
	if ip.IsPrivate() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return fmt.Errorf("不允许访问内网IP地址: %s", ip.String())
	}

	// 禁止云厂商元数据服务
	metadataIPs := []string{
		"169.254.169.254", // AWS/GCP/Azure
		"100.100.100.200", // 阿里云
	}
	for _, metaIP := range metadataIPs {
		if ip.Equal(net.ParseIP(metaIP)) {
			return fmt.Errorf("不允许访问云元数据服务")
		}
	}

	return nil
}

// validateURL 预验证URL安全性（协议、端口、主机），作为第一道防线
func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL解析失败: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("不允许的URL协议: %s，仅支持 http/https", u.Scheme)
	}

	// 检查端口白名单
	port := u.Port()
	if port != "" && port != "80" && port != "443" {
		return fmt.Errorf("不允许的端口: %s，仅支持 80/443", port)
	}

	return isSafeHost(u.Hostname())
}

// buildSafeHTTPClient 构建防SSRF的HTTP客户端
// 包含连接层IP校验（防DNS重绑定）和重定向校验（防重定向到内网）
func buildSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// 连接时二次校验目标IP，防止DNS重绑定攻击
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				if err := isSafeHost(host); err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, addr)
			},
			MaxIdleConns:       10,
			IdleConnTimeout:    60 * time.Second,
			DisableCompression: false,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 限制最大重定向次数
			if len(via) >= 5 {
				return fmt.Errorf("超过最大重定向次数")
			}
			// 验证每个重定向目标的URL安全性
			if req.URL == nil {
				return fmt.Errorf("重定向目标URL为空")
			}
			return validateURL(req.URL.String())
		},
	}
}

// Execute 解析 JSON 配置并执行 HTTP 请求
func (e *CurlExecutor) Execute(ctx context.Context, command string, logger io.Writer) error {
	var cmd curlCommand
	if err := json.Unmarshal([]byte(command), &cmd); err != nil {
		return fmt.Errorf("解析curl命令失败: %w", err)
	}

	if cmd.URL == "" {
		return fmt.Errorf("URL不能为空")
	}

	// 第一道防线：预验证URL安全性
	if err := validateURL(cmd.URL); err != nil {
		return fmt.Errorf("URL验证失败: %w", err)
	}

	if cmd.Method == "" {
		cmd.Method = "GET"
	}
	if cmd.Timeout <= 0 {
		cmd.Timeout = 30
	}

	timeout := time.Duration(cmd.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if cmd.Body != "" {
		bodyReader = strings.NewReader(cmd.Body)
	}

	req, err := http.NewRequestWithContext(ctx, cmd.Method, cmd.URL, bodyReader)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	for k, v := range cmd.Headers {
		req.Header.Set(k, v)
	}

	fmt.Fprintf(logger, "[%s] %s %s\n", time.Now().Format(time.DateTime), cmd.Method, cmd.URL)

	// 使用防SSRF的安全HTTP客户端（含连接层校验和重定向校验）
	client := buildSafeHTTPClient(timeout)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("执行HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	fmt.Fprintf(logger, "[%s] 响应状态: %s\n", time.Now().Format(time.DateTime), resp.Status)

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		return fmt.Errorf("读取响应体失败: %w", err)
	}

	body := buf.String()
	if len(body) > 4096 {
		body = body[:4096] + "\n... (truncated)"
	}
	fmt.Fprintf(logger, "[%s] 响应内容:\n%s\n", time.Now().Format(time.DateTime), body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP请求返回错误状态码: %d", resp.StatusCode)
	}

	return nil
}

// TaskExecutor 执行注册的 Go task
type TaskExecutor struct{}

// Execute 从 tasks.Registry 查找 task 函数并执行
func (e *TaskExecutor) Execute(ctx context.Context, command string, logger io.Writer) error {
	fn, ok := tasks.Get(command)
	if !ok {
		return fmt.Errorf("Go task '%s' 未注册", command)
	}
	return fn(ctx, logger)
}
