package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// memberLevelView 是等级配置的对外返回结构。
type memberLevelView struct {
	ID               int       `json:"id"`
	Code             string    `json:"code"`
	Name             string    `json:"name"`
	GrowthThreshold  int64     `json:"growth_threshold"`
	DiscountBps      int       `json:"discount_bps"`
	PriorityDispatch bool      `json:"priority_dispatch"`
	PrepayBonusBps   int       `json:"prepay_bonus_bps"`
	SortOrder        int       `json:"sort_order"`
	Status           string    `json:"status"`
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func toMemberLevelView(item *ent.EsportsMemberLevelRule) memberLevelView {
	return memberLevelView{
		ID: item.ID, Code: item.Code, Name: item.Name,
		GrowthThreshold: item.GrowthThreshold, DiscountBps: item.DiscountBps,
		PriorityDispatch: item.PriorityDispatch, PrepayBonusBps: item.PrepayBonusBps,
		SortOrder: item.SortOrder, Status: item.Status.String(), Version: item.Version,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func adminMemberLevels(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listMemberLevels(c.Context(), global.ORM, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views := make([]memberLevelView, 0, len(items))
	for _, item := range items {
		views = append(views, toMemberLevelView(item))
	}
	return reply(c, views)
}

func createMemberLevelHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in memberLevelInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createMemberLevel(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toMemberLevelView(item))
}

func updateMemberLevelHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "会员等级")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		memberLevelInput
		ExpectedVersion int `json:"expected_version"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateMemberLevel(c.Context(), global.ORM, id, in.ExpectedVersion, in.memberLevelInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toMemberLevelView(item))
}

func setMemberLevelStatusHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "会员等级")
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
	item, err := setMemberLevelStatus(c.Context(), global.ORM, id, in.ExpectedVersion, in.Status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toMemberLevelView(item))
}

// adminCustomerMember 客户会员详情：当前等级、折扣、累计成长值、待降级信息与流水/覆盖记录。
func adminCustomerMember(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	customerID, err := parsePathID(c, "客户")
	if err != nil {
		return businessError(c, err)
	}
	view, err := buildMemberView(c.Context(), global.ORM, customerID, queryLimit(c, 50))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// setCustomerMemberLevelHandler 管理员手动调级（§10.3）：记录操作人、原因与时间。
func setCustomerMemberLevelHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := parsePathID(c, "客户")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		LevelCode string `json:"level_code"`
		Reason    string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	customer, err := setMemberLevelByAdmin(c.Context(), global.ORM, customerID, in.LevelCode, in.Reason, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	view, err := buildMemberView(c.Context(), global.ORM, customer.ID, queryLimit(c, 50))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
