package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// apiClient 是带 admin token 的最小 HTTP 客户端。
// 后端响应体统一为 {code, message, data}，且业务失败时 HTTP 状态可能仍是 200，
// 因此所有判定都以 body.code 为准，HTTP 状态只作为兜底信息记录。
type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newAPIClient(baseURL string, timeout time.Duration) *apiClient {
	return &apiClient{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func successfulResponse(code, status int) bool {
	return (code == 1 || code == 2) && status >= 200 && status < 300
}

// call 发起请求，返回业务码 code、HTTP 状态码、响应体。
// code 优先取响应体里的 code；响应体不是 JSON 时回退为 HTTP 状态码。
func (c *apiClient) call(method, path string, body any) (code, status int, raw []byte, err error) {
	code, status, _, raw, err = c.callWithHeader(method, path, body)
	return code, status, raw, err
}

// callWithHeader 与 call 相同，额外返回响应头（登录重试需要读 Retry-After）。
func (c *apiClient) callWithHeader(method, path string, body any) (code, status int, header http.Header, raw []byte, err error) {
	var reader io.Reader
	if body != nil {
		b, mErr := json.Marshal(body)
		if mErr != nil {
			return 0, 0, nil, nil, mErr
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, resp.StatusCode, resp.Header, nil, err
	}
	var env envelope
	if json.Unmarshal(raw, &env) == nil && env.Code != 0 {
		return env.Code, resp.StatusCode, resp.Header, raw, nil
	}
	return resp.StatusCode, resp.StatusCode, resp.Header, raw, nil
}

// login 登录并保存 access_token。命中登录限流（429）时按 Retry-After 等待后重试，
// 最多 4 轮；其他失败直接返回（密码错、账号停用重试也不会成功）。
func (c *apiClient) login(username, password string) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		retryAfter, err := c.loginOnce(username, password)
		if err == nil {
			return nil
		}
		lastErr = err
		if retryAfter <= 0 {
			return err
		}
		time.Sleep(retryAfter)
	}
	return lastErr
}

// loginOnce 执行一次登录。被限流时返回需要等待的时长（>0），其余错误返回 0。
func (c *apiClient) loginOnce(username, password string) (time.Duration, error) {
	code, status, header, raw, err := c.callWithHeader(http.MethodPost, "/api/v1/admin/public/login", map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		return 0, fmt.Errorf("登录 %s 失败: %w", username, err)
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	if code != 1 && code != 2 {
		msg := strings.TrimSpace(env.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		if status == http.StatusTooManyRequests {
			return retryAfterDuration(header), fmt.Errorf("登录 %s 被限流（429）：%s", username, msg)
		}
		return 0, fmt.Errorf("登录 %s 被拒绝（code=%d, http=%d）：%s", username, code, status, msg)
	}
	var data struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || data.AccessToken == "" {
		return 0, fmt.Errorf("登录 %s 未返回 access_token", username)
	}
	c.token = data.AccessToken
	return 0, nil
}

// retryAfterDuration 解析 Retry-After（秒），缺省或非法时回退 15 秒。
func retryAfterDuration(header http.Header) time.Duration {
	if header != nil {
		if v := strings.TrimSpace(header.Get("Retry-After")); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	return 15 * time.Second
}

// menuNode 是 /admin/auth/user/menu-permissions 的树节点。
type menuNode struct {
	Code     string     `json:"code"`
	MenuPath string     `json:"menu_path"`
	Children []menuNode `json:"children"`
}

// menuPaths 返回当前登录账号可见的全部菜单：menu_path -> code。
func (c *apiClient) menuPaths() (map[string]string, error) {
	code, status, raw, err := c.call(http.MethodGet, "/api/v1/admin/auth/user/menu-permissions", nil)
	if err != nil {
		return nil, err
	}
	if code != 1 && code != 2 {
		return nil, fmt.Errorf("获取菜单权限失败（code=%d, http=%d）", code, status)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	var nodes []menuNode
	if err := json.Unmarshal(env.Data, &nodes); err != nil {
		return nil, fmt.Errorf("解析菜单树失败: %w", err)
	}
	out := make(map[string]string)
	var walk func(list []menuNode)
	walk = func(list []menuNode) {
		for _, n := range list {
			if n.MenuPath != "" {
				out[normalizeMenuPath(n.MenuPath)] = n.Code
			}
			walk(n.Children)
		}
	}
	walk(nodes)
	return out, nil
}

// normalizeMenuPath 去掉查询串与尾部斜杠，避免 /a 与 /a/ 被当成两个菜单。
func normalizeMenuPath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

// list 请求一个列表接口，兼容「data 为数组」与「data 为 {items|data, total}」两种返回。
func (c *apiClient) list(path string) (items []json.RawMessage, total int, code int, err error) {
	code, _, raw, err := c.call(http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	var env envelope
	if uErr := json.Unmarshal(raw, &env); uErr != nil {
		return nil, 0, code, uErr
	}
	items, total, err = parseList(env.Data)
	return items, total, code, err
}

func parseList(raw json.RawMessage) (items []json.RawMessage, total int, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, 0, nil
	}
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, 0, err
		}
		return items, len(items), nil
	}
	var obj struct {
		Items []json.RawMessage `json:"items"`
		Data  []json.RawMessage `json:"data"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, 0, err
	}
	items = obj.Items
	if items == nil {
		items = obj.Data
	}
	total = obj.Total
	if total == 0 && len(items) > 0 {
		total = len(items)
	}
	return items, total, nil
}

type customerRow struct {
	ID           int    `json:"id"`
	DisplayName  string `json:"display_name"`
	OwnerAdminID *int   `json:"owner_admin_id"`
}

type adminRow struct {
	ID       int    `json:"id"`
	UID      string `json:"uid"`
	Username string `json:"username"`
}
