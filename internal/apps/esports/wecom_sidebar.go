package esports

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	// FooterTip 是侧边栏「底部提示」区块文案（§8.4-11），来自系统配置键
	// app.esports.wecom_sidebar_footer_tip；空串表示前端不展示该区块。
	FooterTip string `json:"footer_tip"`
}

// wecomSidebarFooterTipKey 是侧边栏底部提示文案的系统配置键（裸键 wecom_sidebar_footer_tip）。
// 未配置 / 空值回落空串 = 不展示底部提示区块。
const wecomSidebarFooterTipKey = "app.esports.wecom_sidebar_footer_tip"

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
		AuthMode:     mode,
		JsSdkEnabled: mode == wecomAuthModeJsSDK,
		// §8.4-11：底部提示由后台配置下发；未配置即空串，前端据此隐藏该区块。
		FooterTip: wecomConfigString(c.Context(), global.ORM, wecomSidebarFooterTipKey),
	})
}

// ── 企微 JS-SDK 签名（§8.4） ──
//
// 签名算法遵循企业微信官方 JS-SDK 规则：signature = sha1(jsapi_ticket=xxx&noncestr=xxx&timestamp=xxx&url=xxx)，
// 字段按字典序排列（jsapi_ticket < noncestr < timestamp < url），值不做 URL 转义。
// 生产环境 jsapi_ticket 应由 access_token（wecom_secret 换发）定时拉取并缓存；当前暂以 systemconfig
// 预置 wecom_jsapi_ticket 直接取用，接入真实密钥后在此接入 access_token → jsapi_ticket 拉取链路。

// 企微 JS-SDK 相关系统配置键（runtime 全键由 app.esports. 前缀 + 短键组成，见 apps.Provision）。
const (
	wecomCorpIDKey      = "app.esports.wecom_corp_id"
	wecomAgentIDKey     = "app.esports.wecom_agent_id"
	wecomSecretKey      = "app.esports.wecom_secret"
	wecomJsapiTicketKey = "app.esports.wecom_jsapi_ticket"
)

// errWecomNotConfigured 企微侧边栏应用尚未配置完整凭据；映射 HTTP 503 供前端降级手动建档。
var errWecomNotConfigured = fmt.Errorf("%w：企微侧边栏应用尚未配置（缺少 corpId / agentId / 应用密钥 / jsapi_ticket）", errNotConfigured)

// wecomJsSdkCredential 企微 JS-SDK 签名所需凭据。
type wecomJsSdkCredential struct {
	CorpID  string
	AgentID string
	Secret  string
	Ticket  string
}

// wecomConfigString 读取企微相关字符串配置，缺失 / 空值回落空串。
func wecomConfigString(ctx context.Context, db *ent.Client, key string) string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(item.Value)
}

// loadWecomJsSdkCredential 读取 JS-SDK 签名凭据；任一缺失即返回 errWecomNotConfigured。
func loadWecomJsSdkCredential(ctx context.Context, db *ent.Client) (wecomJsSdkCredential, error) {
	cred := wecomJsSdkCredential{
		CorpID:  wecomConfigString(ctx, db, wecomCorpIDKey),
		AgentID: wecomConfigString(ctx, db, wecomAgentIDKey),
		Secret:  wecomConfigString(ctx, db, wecomSecretKey),
		Ticket:  wecomConfigString(ctx, db, wecomJsapiTicketKey),
	}
	if cred.CorpID == "" || cred.AgentID == "" || cred.Secret == "" || cred.Ticket == "" {
		return wecomJsSdkCredential{}, errWecomNotConfigured
	}
	return cred, nil
}

// wecomJsSignature 按企微官方规则计算 JS-SDK 签名（sha1，字段字典序固定）。
func wecomJsSignature(ticket, nonceStr, timestamp, url string) string {
	raw := "jsapi_ticket=" + ticket + "&noncestr=" + nonceStr + "&timestamp=" + timestamp + "&url=" + url
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// randomNonceStr 生成 16 位十六进制随机串作为 noncestr。
func randomNonceStr() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// wecomJsConfigInput JS-SDK 签名入参：当前页面 URL（不含 # 及其后部分）。
type wecomJsConfigInput struct {
	URL string `json:"url"`
}

// wecomJsConfigResponse JS-SDK 签名出参，供前端 wx.config 使用。
type wecomJsConfigResponse struct {
	AppID     string `json:"app_id"`
	AgentID   string `json:"agent_id"`
	Timestamp string `json:"timestamp"`
	NonceStr  string `json:"nonce_str"`
	Signature string `json:"signature"`
}

// wecomJsConfigHandler 获取企业微信 JS-SDK 签名（§8.4，AuthAdmin）。
// 配置缺失时返回 503「企微未配置」，由前端降级为手动输入 / 复制话术。
func wecomJsConfigHandler(c fiber.Ctx) error {
	var in wecomJsConfigInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	url := strings.TrimSpace(in.URL)
	if url == "" {
		return fail(c, fiber.StatusBadRequest, "缺少当前页面 URL")
	}
	cred, err := loadWecomJsSdkCredential(c.Context(), global.ORM)
	if err != nil {
		return businessError(c, err)
	}
	nonceStr := randomNonceStr()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	return reply(c, wecomJsConfigResponse{
		AppID:     cred.CorpID,
		AgentID:   cred.AgentID,
		Timestamp: timestamp,
		NonceStr:  nonceStr,
		Signature: wecomJsSignature(cred.Ticket, nonceStr, timestamp, url),
	})
}

// adminWecomPaymentMethodsHandler 侧边栏收款方式（§8.4，AuthAdmin）：
// 复用客户自助端收款方式视图 listCustomerPaymentMethods（§4.3），
// 供客服在侧边栏向客户出示启用的收款方式、财务审核通过的收款账户与二维码。
func adminWecomPaymentMethodsHandler(c fiber.Ctx) error {
	methods, err := listCustomerPaymentMethods(c.Context(), global.ORM)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, methods)
}
