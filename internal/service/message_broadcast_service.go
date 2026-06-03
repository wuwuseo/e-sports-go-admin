package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/global"
)

// 广播相关领域错误
var (
	ErrBroadcastInvalidScope     = errors.New("无效的目标范围 scope")
	ErrBroadcastUsersRequired    = errors.New("scope=users 时必须提供 user_uids")
	ErrBroadcastRolesRequired    = errors.New("scope=roles 时必须提供 role_codes")
	ErrBroadcastNoTargetResolved = errors.New("未解析到任何目标用户")
)

// 广播目标范围常量
const (
	BroadcastScopeAll   = "all"
	BroadcastScopeUsers = "users"
	BroadcastScopeRoles = "roles"
)

// BroadcastTarget 广播目标
type BroadcastTarget struct {
	Scope     string   `json:"scope"      validate:"required,oneof=all users roles"`
	UserUIDs  []string `json:"user_uids,omitempty"`
	RoleCodes []string `json:"role_codes,omitempty"`
}

// BroadcastRequest 管理员广播请求
type BroadcastRequest struct {
	Type        string          `json:"type"         validate:"required,oneof=system business task"`
	Level       string          `json:"level"        validate:"required,oneof=info warning error"`
	Title       string          `json:"title"        validate:"required,max=200"`
	Content     string          `json:"content"      validate:"required"`
	ContentHTML string          `json:"content_html,omitempty"`
	Extra       map[string]any  `json:"extra,omitempty"`
	Target      BroadcastTarget `json:"target"      validate:"required"`
}

// BroadcastResponse 广播响应
type BroadcastResponse struct {
	Delivered int `json:"delivered"`
}

// MessageBroadcastServiceInterface 广播服务接口
type MessageBroadcastServiceInterface interface {
	Broadcast(ctx context.Context, domain string, req *BroadcastRequest) (*BroadcastResponse, error)
}

// MessageBroadcastService 广播服务实现
type MessageBroadcastService struct {
	Client     *ent.Client
	MessageSvc MessageServiceInterface
}

// NewMessageBroadcastService 构造
func NewMessageBroadcastService(messageSvc MessageServiceInterface) *MessageBroadcastService {
	return &MessageBroadcastService{
		Client:     global.ORM,
		MessageSvc: messageSvc,
	}
}

var _ MessageBroadcastServiceInterface = (*MessageBroadcastService)(nil)

// Broadcast 解析 target → 入库 + 按偏好过滤推送
func (s *MessageBroadcastService) Broadcast(ctx context.Context, domain string, req *BroadcastRequest) (*BroadcastResponse, error) {
	if req == nil {
		return nil, ErrMessageInvalidReq
	}
	if domain == "" {
		domain = "default"
	}

	uids, err := s.resolveTargetUIDs(ctx, domain, &req.Target)
	if err != nil {
		return nil, err
	}
	if len(uids) == 0 {
		return nil, ErrBroadcastNoTargetResolved
	}

	publish := PublishMessage{
		Type:        req.Type,
		Level:       req.Level,
		Title:       req.Title,
		Content:     req.Content,
		ContentHTML: req.ContentHTML,
		Extra:       req.Extra,
	}

	delivered, err := s.MessageSvc.BroadcastToUsers(ctx, uids, publish)
	if err != nil {
		return nil, err
	}
	return &BroadcastResponse{Delivered: delivered}, nil
}

// resolveTargetUIDs 解析广播目标 → 实际 UID 列表（去重）
func (s *MessageBroadcastService) resolveTargetUIDs(ctx context.Context, domain string, target *BroadcastTarget) ([]string, error) {
	if target == nil {
		return nil, ErrBroadcastInvalidScope
	}
	switch target.Scope {
	case BroadcastScopeAll:
		return s.resolveAllUIDs(ctx)
	case BroadcastScopeUsers:
		filtered := dedupNonEmpty(target.UserUIDs)
		if len(filtered) == 0 {
			return nil, ErrBroadcastUsersRequired
		}
		return filtered, nil
	case BroadcastScopeRoles:
		roleCodes := dedupNonEmpty(target.RoleCodes)
		if len(roleCodes) == 0 {
			return nil, ErrBroadcastRolesRequired
		}
		return s.resolveRoleUIDs(ctx, domain, roleCodes)
	default:
		return nil, ErrBroadcastInvalidScope
	}
}

// resolveAllUIDs 查询全部 admin + user 表的 UID（去重）
func (s *MessageBroadcastService) resolveAllUIDs(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	out := make([]string, 0, 128)

	admins, err := s.Client.Admin.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询管理员列表失败: %w", err)
	}
	for _, a := range admins {
		if a.UID == "" {
			continue
		}
		if _, ok := seen[a.UID]; ok {
			continue
		}
		seen[a.UID] = struct{}{}
		out = append(out, a.UID)
	}

	users, err := s.Client.User.Query().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询用户列表失败: %w", err)
	}
	for _, u := range users {
		if u.UID == "" {
			continue
		}
		if _, ok := seen[u.UID]; ok {
			continue
		}
		seen[u.UID] = struct{}{}
		out = append(out, u.UID)
	}
	return out, nil
}

// resolveRoleUIDs 通过 Casbin g 规则反查指定角色下的所有用户 UID
// g 规则格式：(Ptype="g", V0=userUID, V1=roleName, V2=domain)
func (s *MessageBroadcastService) resolveRoleUIDs(ctx context.Context, domain string, roleCodes []string) ([]string, error) {
	rules, err := s.Client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("g"),
			casbinrule.V1In(roleCodes...),
			casbinrule.V2EQ(domain),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询角色用户失败: %w", err)
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		uid := strings.TrimSpace(r.V0)
		if uid == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, uid)
	}
	return out, nil
}

// dedupNonEmpty 去重 + 过滤空字符串
func dedupNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		v := strings.TrimSpace(s)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
