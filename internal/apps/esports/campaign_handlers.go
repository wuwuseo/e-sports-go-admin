package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// campaignView 是充值活动的对外返回结构，字段与 schema 对齐。
type campaignView struct {
	ID                  int                   `json:"id"`
	Code                string                `json:"code"`
	Name                string                `json:"name"`
	ActivityType        string                `json:"activity_type"`
	Tiers               []schema.CampaignTier `json:"tiers"`
	Audience            string                `json:"audience"`
	AudienceCustomerIDs []int                 `json:"audience_customer_ids"`
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
	tiers := item.Tiers
	if tiers == nil {
		tiers = []schema.CampaignTier{}
	}
	return campaignView{
		ID: item.ID, Code: item.Code, Name: item.Name,
		ActivityType: item.ActivityType.String(), Tiers: tiers,
		Audience: item.Audience.String(), AudienceCustomerIDs: ids,
		StackRule: item.StackRule.String(), BonusValidDays: item.BonusValidDays,
		StartsAt: item.StartsAt, EndsAt: item.EndsAt, Status: item.Status.String(),
		Version: item.Version, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
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

// rechargeWalletByCampaignHandler 按活动充值：account_type 走 query，仅接受 customer_balance；
// 幂等键取自请求头 Idempotency-Key。
func rechargeWalletByCampaignHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	if strings.TrimSpace(c.Query("account_type")) != walletCustomerBalance {
		return fail(c, fiber.StatusBadRequest, "充值活动仅支持客户余额账户")
	}
	ownerID, err := parsePathID(c, "钱包归属人")
	if err != nil {
		return businessError(c, err)
	}
	key := strings.TrimSpace(c.Get("Idempotency-Key"))
	var in struct {
		PrincipalCents int64  `json:"principal_cents"`
		Note           string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := rechargeWithCampaigns(c.Context(), global.ORM, adminID, walletCustomerBalance,
		ownerID, in.PrincipalCents, in.Note, key, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
