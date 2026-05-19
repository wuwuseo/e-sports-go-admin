package service

import "time"

// ========== Swagger 文档专用类型定义 ==========
// 由于 swag 工具不支持泛型，为各 ListResponse 创建具体类型别名

// AdminListResponse 管理员列表响应（Swagger文档专用）
type AdminListResponse = ListResponse[*AdminListItem]

// UserListResponse 用户列表响应（Swagger文档专用）
type UserListResponse = ListResponse[*UserListItem]

// ThirdPartyUserListResponse 第三方用户列表响应（Swagger文档专用）
type ThirdPartyUserListResponse = ListResponse[*ThirdPartyUserItem]

// ThirdPartyUserItem 第三方用户项（Swagger文档专用）
type ThirdPartyUserItem struct {
	ID           int       `json:"id"`
	UserID       int       `json:"user_id"`
	Username     string    `json:"username"`
	Platform     string    `json:"platform"`
	PlatformName string    `json:"platform_name"`
	Openid       string    `json:"openid"`
	Nickname     string    `json:"nickname"`
	AvatarURL    string    `json:"avatar_url"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// BlacklistItem 黑名单项
type BlacklistItem struct {
	ID         int       `json:"id"`
	UserID     int       `json:"user_id"`
	Username   string    `json:"username"`
	Reason     string    `json:"reason"`
	OperatorID string    `json:"operator_id"`
	Operator   string    `json:"operator"`
	CreatedAt  time.Time `json:"created_at"`
}

// BlacklistResponse 黑名单列表响应（Swagger文档专用）
type BlacklistResponse = ListResponse[*BlacklistItem]

// ThirdPartyUserResponse 第三方用户响应（Swagger文档专用）
type ThirdPartyUserResponse = ThirdPartyUserItem
