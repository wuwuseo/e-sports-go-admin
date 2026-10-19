package esports

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// campaignView 是充值活动的对外返回结构，字段与 schema 对齐。
// P1-19：补上陪玩受众与奖励目标账户，管理端表单需要回显这两项才能编辑猫粮奖励活动。
type campaignView struct {
	ID                  int                   `json:"id"`
	Code                string                `json:"code"`
	Name                string                `json:"name"`
	ActivityType        string                `json:"activity_type"`
	Tiers               []schema.CampaignTier `json:"tiers"`
	Audience            string                `json:"audience"`
	AudienceCustomerIDs []int                 `json:"audience_customer_ids"`
	AudiencePlayerIDs   []int                 `json:"audience_player_ids"`
	TargetAccountType   string                `json:"target_account_type"`
	StackRule           string                `json:"stack_rule"`
	BonusValidDays      int                   `json:"bonus_valid_days"`
	StartsAt            time.Time             `json:"starts_at"`
	EndsAt              time.Time             `json:"ends_at"`
	Status              string                `json:"status"`
	Version             int                   `json:"version"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

func toCampaignView(item *ent.EsportsRechargeCampaign) campaignView {
	ids := item.AudienceCustomerIds
	if ids == nil {
		ids = []int{}
	}
	playerIDs := item.AudiencePlayerIds
	if playerIDs == nil {
		playerIDs = []int{}
	}
	tiers := item.Tiers
	if tiers == nil {
		tiers = []schema.CampaignTier{}
	}
	targetAccount := string(item.TargetAccountType)
	if targetAccount == "" {
		targetAccount = normalizeCampaignTargetAccount("")
	}
	return campaignView{
		ID: item.ID, Code: item.Code, Name: item.Name,
		ActivityType: item.ActivityType.String(), Tiers: tiers,
		Audience: item.Audience.String(), AudienceCustomerIDs: ids, AudiencePlayerIDs: playerIDs,
		TargetAccountType: targetAccount,
		StackRule:         item.StackRule.String(), BonusValidDays: item.BonusValidDays,
		StartsAt: item.StartsAt, EndsAt: item.EndsAt, Status: item.Status.String(),
		Version: item.Version, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

// campaignRechargeAccountType 归一化「按活动充值」的账户类型（P1-19）：
// 活动奖励支持客户余额与陪玩猫粮两类账户，两个值都必须显式指定（不传不猜），
// 未知类型直接拒绝并指出支持值，避免把猫粮奖励误发到客户余额。
func campaignRechargeAccountType(raw string) (string, error) {
	switch value := strings.TrimSpace(raw); value {
	case walletCustomerBalance:
		return walletCustomerBalance, nil
	case walletPlayerCatfood:
		return walletPlayerCatfood, nil
	case "":
		return "", fmt.Errorf(
			"%w：请通过 account_type 指定充值账户类型（%s 客户余额 / %s 陪玩猫粮）",
			errInvalidInput, walletCustomerBalance, walletPlayerCatfood)
	default:
		return "", fmt.Errorf(
			"%w：不支持的账户类型 %q（仅支持 %s 客户余额 / %s 陪玩猫粮）",
			errInvalidInput, value, walletCustomerBalance, walletPlayerCatfood)
	}
}

func adminCampaigns(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCampaigns(c.Context(), global.ORM, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views := make([]campaignView, 0, len(items))
	for _, item := range items {
		views = append(views, toCampaignView(item))
	}
	return reply(c, views)
}

// publicCampaigns 客户端已生效充值赠送活动（AuthPublic）：仅 status=active 且当前在活动期内。
func publicCampaigns(c fiber.Ctx) error {
	items, err := listActiveCampaigns(c.Context(), global.ORM, time.Now(), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views := make([]campaignView, 0, len(items))
	for _, item := range items {
		views = append(views, toCampaignView(item))
	}
	return reply(c, views)
}

func createCampaignHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in campaignInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCampaign(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toCampaignView(item))
}

func updateCampaignHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "活动")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		campaignInput
		ExpectedVersion int `json:"expected_version"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateCampaign(c.Context(), global.ORM, id, in.ExpectedVersion, in.campaignInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toCampaignView(item))
}

func setCampaignStatusHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "活动")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int    `json:"expected_version"`
		Status          string `json:"status"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := setCampaignStatus(c.Context(), global.ORM, id, in.ExpectedVersion, in.Status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toCampaignView(item))
}

// rechargeWalletByCampaignHandler 按活动充值（P1-19）：account_type 走 query，接受客户余额与陪玩猫粮
// 两类账户，未知类型显式 400；客户余额校验客户归属，猫粮只校验陪玩档案存在（陪玩无归属客服模型）。
// 幂等键取自请求头 Idempotency-Key。
func rechargeWalletByCampaignHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	accountType, err := campaignRechargeAccountType(c.Query("account_type"))
	if err != nil {
		return businessError(c, err)
	}
	ownerID, err := parsePathID(c, "钱包归属人")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	switch accountType {
	case walletCustomerBalance:
		if _, err = loadOwnedCustomer(c.Context(), global.ORM, adminID, isSuper, ownerID); err != nil {
			return businessError(c, err)
		}
	case walletPlayerCatfood:
		// 陪玩档案没有「归属客服」关系：与 /admin/wallets 的猫粮口径一致，只校验档案存在。
		if _, err = global.ORM.EsportsPlayer.Get(c.Context(), ownerID); err != nil {
			return businessError(c, err)
		}
	}
	key := strings.TrimSpace(c.Get("Idempotency-Key"))
	var in struct {
		PrincipalCents int64  `json:"principal_cents"`
		Note           string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := rechargeWithCampaigns(c.Context(), global.ORM, adminID, accountType,
		ownerID, in.PrincipalCents, in.Note, key, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
