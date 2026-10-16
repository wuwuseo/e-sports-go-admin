package esports

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// 企微侧边栏接入模式配置（§8.4）。
//
// auth_mode 决定侧边栏 H5 如何识别当前客户：
//   - js_sdk（默认）：启用企微 JS-SDK，宿主注入 adapter 后自动识别当前外部联系人；
//   - account：关闭 JS-SDK，客服用账号登录后手动输入 / 选择客户。
//
// 两种模式最终都要求客服 admin 登录态（/admin/* 接口均需 esports_admin_token）；
// JS-SDK 仅影响「客户识别」环节，不替代登录鉴权。
const wecomSidebarAuthModeKey = "app.esports.wecom_sidebar_auth_mode"

// 支持的接入模式常量。
const (
	wecomAuthModeJsSDK   = "js_sdk"
	wecomAuthModeAccount = "account"
)

// wecomSidebarConfig 是公开给侧边栏 H5 的配置（AuthPublic，不含个人数据）。
type wecomSidebarConfig struct {
	AuthMode string `json:"auth_mode"`
	// JsSdkEnabled 是 auth_mode 的布尔化表达，便于前端直接判断开关。
	JsSdkEnabled bool `json:"js_sdk_enabled"`
}

// wecomSidebarAuthMode 读取侧边栏接入模式配置，非法 / 缺失时回落 account（默认账号登录模式）。
func wecomSidebarAuthMode(ctx context.Context, db *ent.Client) string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(wecomSidebarAuthModeKey)).Only(ctx)
	if err != nil {
		return wecomAuthModeAccount
	}
	mode := strings.TrimSpace(item.Value)
	switch mode {
	case wecomAuthModeJsSDK:
		return wecomAuthModeJsSDK
	case wecomAuthModeAccount:
		return wecomAuthModeAccount
	default:
		return wecomAuthModeAccount
	}
}

// publicWecomSidebarConfig 侧边栏 H5 读取接入模式（AuthPublic）。
// 需在 app.go 接线：{Method: "GET", Path: "/wecom/sidebar-config", Auth: cmfapp.AuthPublic, Handler: publicWecomSidebarConfig}
func publicWecomSidebarConfig(c fiber.Ctx) error {
	mode := wecomSidebarAuthMode(c.Context(), global.ORM)
	return reply(c, wecomSidebarConfig{
		AuthMode:      mode,
		JsSdkEnabled:  mode == wecomAuthModeJsSDK,
	})
}
