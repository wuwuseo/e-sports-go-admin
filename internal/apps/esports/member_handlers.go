package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// memberLevelView 是等级配置的对外返回结构。
type memberLevelView struct {
	ID               int    `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	GrowthThreshold  int64  `json:"growth_threshold"`
	DiscountBps      int    `json:"discount_bps"`
	PriorityDispatch bool   `json:"priority_dispatch"`
	PrepayBonusBps   int    `json:"prepay_bonus_bps"`
	DedicatedCs      bool   `json:"dedicated_cs"`
	PriorityBooking  bool   `json:"priority_booking"`
	// 专属通道 / 线下活动优先（§10.1-02）。
	DedicatedChannel     bool      `json:"dedicated_channel"`
	OfflineEventPriority bool      `json:"offline_event_priority"`
	SortOrder            int       `json:"sort_order"`
	Status               string    `json:"status"`
	Version              int       `json:"version"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func toMemberLevelView(item *ent.EsportsMemberLevelRule) memberLevelView {
	return memberLevelView{
		ID: item.ID, Code: item.Code, Name: item.Name,
		GrowthThreshold: item.GrowthThreshold, DiscountBps: item.DiscountBps,
		PriorityDispatch: item.PriorityDispatch, PrepayBonusBps: item.PrepayBonusBps,
		DedicatedCs: item.DedicatedCs, PriorityBooking: item.PriorityBooking,
		DedicatedChannel: item.DedicatedChannel, OfflineEventPriority: item.OfflineEventPriority,
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
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := parsePathID(c, "客户")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err := loadOwnedCustomer(c.Context(), global.ORM, adminID, isSuper, customerID); err != nil {
		return businessError(c, err)
	}
	view, err := buildMemberView(c.Context(), global.ORM, customerID, queryLimit(c, 50))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// setCustomerMemberLevelHandler 管理员手动调级（§10.3）：记录操作人、原因与时间。
// 覆盖期可选：override_days（天数）与 override_until（RFC3339 到期时刻）二选一，
// 都不传时沿用系统配置 app.esports.member_override_guard_days；同时给出或越界返回参数错误。
func setCustomerMemberLevelHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := parsePathID(c, "客户")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err := loadOwnedCustomer(c.Context(), global.ORM, adminID, isSuper, customerID); err != nil {
		return businessError(c, err)
	}
	var in struct {
		LevelCode string `json:"level_code"`
		Reason    string `json:"reason"`
		// 覆盖期（逐次指定）：天数或到期时刻，二选一；都不传沿用系统配置。
		OverrideDays  int    `json:"override_days"`
		OverrideUntil string `json:"override_until"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	window := memberOverrideWindow{Days: in.OverrideDays}
	if raw := strings.TrimSpace(in.OverrideUntil); raw != "" {
		until, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return fail(c, fiber.StatusBadRequest, "override_until 须为 RFC3339 时间格式")
		}
		window.Until = until
	}
	customer, err := setMemberLevelByAdmin(c.Context(), global.ORM, customerID, in.LevelCode, in.Reason,
		adminID, time.Now(), window)
	if err != nil {
		return businessError(c, err)
	}
	view, err := buildMemberView(c.Context(), global.ORM, customer.ID, queryLimit(c, 50))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
