package esports

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

func requireAdmin(c fiber.Ctx) (int, error) {
	operator, err := requestAdmin(c)
	if err != nil {
		return 0, err
	}
	return operator.ID, nil
}

func parsePathID(c fiber.Ctx, field string) (int, error) {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return 0, errInvalidInput
	}
	return id, nil
}

// confirmOrderAppointmentHandler 确认预约档期（§3.2 / §5.5）：档期提案 request → confirmed，
// 同一事务内做资源核验；未确认档期的预约单不允许派单。
func confirmOrderAppointmentHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := confirmOrderAppointment(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func repriceOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in repriceInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	order, err := repriceOrder(c.Context(), global.ORM, id, adminID, isSuper, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func previewRepriceHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in repriceInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	quote, err := previewReprice(c.Context(), global.ORM, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, quote)
}

func requestPriceAdjustmentHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in priceAdjustmentRequestInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := requestPriceAdjustment(c.Context(), global.ORM, orderID, adminID, isSuper, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func adminPriceAdjustments(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listPriceAdjustments(c.Context(), global.ORM, adminID, isSuper, c.Query("status"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func decidePriceAdjustmentHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "改价审批")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Approve *bool  `json:"approve"`
		Note    string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Approve == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := decidePriceAdjustment(c.Context(), global.ORM, id, adminID, isSuper, *in.Approve, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func cancelOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
		// FaultParty 取消责任方（§3.3-08）：customer / player / admin / platform / none，留空按客户方责任。
		FaultParty string `json:"fault_party"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := cancelOrderWithFault(c.Context(), global.ORM, id, adminID, in.Reason, in.FaultParty, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// previewCancelOrderHandler 已付款订单取消试算（§3.3 公示取消政策）：返回免费窗口、
// 手续费与应退金额，供客服在取消前向客户公示。
func previewCancelOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	quote, err := previewCancelOrder(c.Context(), global.ORM, id, adminID, isSuper, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, quote)
}

// rescheduleOrderHandler 未付款订单的直接改期（PUT /admin/orders/:id/appointment，§3.2）。
// 已付款订单在此被拦截并指向限时改期提案入口（§3.3）：付费档期已被派单席位占用，
// 须经客户与已接单成员逐人确认后由提案生效时原子切换。
func rescheduleOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in rescheduleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := rescheduleOrder(c.Context(), global.ORM, id, adminID, in, time.Now())
	if errors.Is(err, errRescheduleProposalRequired) {
		return fail(c, fiber.StatusConflict, err.Error()+
			"：请改用 POST /api/v1/esports/admin/orders/{id}/reschedule-proposals 发起提案，"+
			"经客户与已接单成员确认后生效")
	}
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// setOrderChannelCostHandler 登记订单渠道成本（§12.1）。
func setOrderChannelCostHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ChannelCostCents int64 `json:"channel_cost_cents"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := setOrderChannelCost(c.Context(), global.ORM, id, in.ChannelCostCents, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// depositOrderHandler 存单：把已付款订单标记为冻结。
func depositOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := depositOrder(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// restoreOrderHandler 恢复：清除订单的存单冻结标记。
func restoreOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := restoreOrder(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func payOrderWithBalanceHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := payOrderWithBalance(c.Context(), global.ORM, adminID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func adminWallets(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	views, err := listWalletAccounts(c.Context(), global.ORM, adminID, isSuper,
		c.Query("account_type"), c.Query("keyword"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func adminWalletTransactions(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "钱包归属人")
	if err != nil {
		return businessError(c, err)
	}
	if strings.TrimSpace(c.Query("account_type")) == walletCustomerBalance {
		isSuper, _ := c.Locals("isSuperAdmin").(bool)
		if _, err := loadOwnedCustomer(c.Context(), global.ORM, adminID, isSuper, id); err != nil {
			return businessError(c, err)
		}
	}
	items, err := listWalletTransactions(c.Context(), global.ORM,
		c.Query("account_type"), id, queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func queryLimit(c fiber.Ctx, fallback int) int {
	value, err := strconv.Atoi(c.Query("limit"))
	if err != nil || value < 1 || value > 500 {
		return fallback
	}
	return value
}

// errCatfoodSuperOnly 陪玩猫粮写入口的拒绝原因。
// 陪玩没有「归属客服」关系（EsportsPlayer 上不存在 owner_admin_id），无法按客户余额那种
// 「归属客服本人或超管」的方式收窄，因此猫粮充值 / 调整统一由超级管理员执行。
var errCatfoodSuperOnly = errors.New("陪玩猫粮的充值 / 调整仅限超级管理员操作")

// ensureCatfoodMutationAllowed 校验猫粮写入口的授权边界（P1）：猫粮是陪玩的结算账户，
// 任意持权客服都能写入会让「先给陪玩充猫粮、再走提现」成为一条无痕迹的套现路径。
// isSuper 取自 PermissionMiddleware 注入的 isSuperAdmin（与 loadOwnedCustomer 同源）。
func ensureCatfoodMutationAllowed(isSuper bool) error {
	if !isSuper {
		return errCatfoodSuperOnly
	}
	return nil
}

// walletMutationHandler 充值 / 调整共用：区分点只在 entry_type，幂等键取自请求头。
// 账户边界：customer_balance 按客户归属（归属客服本人或超管）收窄；
// player_catfood 没有归属关系，改为超管兜底（P1）。
func walletMutationHandler(adjust bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		adminID, err := requireAdmin(c)
		if err != nil {
			return businessError(c, err)
		}
		accountType := strings.TrimSpace(c.Query("account_type"))
		ownerID, err := parsePathID(c, "钱包归属人")
		if err != nil {
			return businessError(c, err)
		}
		isSuper, _ := c.Locals("isSuperAdmin").(bool)
		switch accountType {
		case walletCustomerBalance:
			if _, err := loadOwnedCustomer(c.Context(), global.ORM, adminID, isSuper, ownerID); err != nil {
				return businessError(c, err)
			}
		case walletPlayerCatfood:
			// 猫粮写入口的拒绝文案要能直接给运营看，故不走 businessError 的通用 403 文案。
			if err := ensureCatfoodMutationAllowed(isSuper); err != nil {
				return fail(c, fiber.StatusForbidden, err.Error())
			}
		}
		key := strings.TrimSpace(c.Get("Idempotency-Key"))
		var in walletInput
		if err := c.Bind().Body(&in); err != nil {
			return fail(c, fiber.StatusBadRequest, "请求正文无效")
		}
		var view *walletAccountView
		if adjust {
			view, err = walletAdjust(c.Context(), global.ORM, adminID, accountType, ownerID, in, key)
		} else {
			view, err = walletRecharge(c.Context(), global.ORM, adminID, accountType, ownerID, in, key)
		}
		if err != nil {
			return businessError(c, err)
		}
		return reply(c, view)
	}
}

// disposeExpiredAppointmentHandler 处置「已付款待确认档期超时」订单（§5.5-06）：
// 请求体 {"action":"refund|reschedule","reason":"...","start_at":"RFC3339","expected_version":N}。
// 路由由 ownedOrderHandler 保护（仅负责客服或超管，P0-12），权限码 esports:orders:appointment_dispose；
// action=refund 会触发全额退款，故归属边界必须在进入本 handler 之前判完。
func disposeExpiredAppointmentHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in appointmentDisposalInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := disposeExpiredAppointment(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// appointmentDisposalOptionsHandler 读取档期超时处置的可用动作（只读，无副作用）：
// 返回 {allowed_actions, current_action, expire_at}，前端据此渲染「全额退还 / 改期」按钮，
// 不再硬编码处置方式；判定与 disposeExpiredAppointmentHandler 共用同一份配置口径。
// 路由由 readableOrderHandler 保护（只读可见：负责客服 / 协作客服 / 超管），不放开写边界。
func appointmentDisposalOptionsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := global.ORM.EsportsOrder.Get(c.Context(), id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, appointmentDisposalOptions(c.Context(), global.ORM, order))
}

// adminServiceTimingHandler 读取某订单某陪玩服务段的当前计时（只读，无副作用）：
// 返回 {effective_seconds, wall_clock_seconds, upper_bound_seconds, paused_seconds, status}，
// 供「时长更正」弹窗预填当前有效时长与可填上限。上界与更正校验共用
// serviceTimingCorrectionBound，因此接口给的上限恒等于更正时被接受的边界。
// 路由由 readableOrderHandler 保护（只读可见），更正本身仍是负责客服 / 超管的写入口。
func adminServiceTimingHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePlayerPathID(c)
	if err != nil {
		return businessError(c, err)
	}
	readout, err := serviceTimingForAdmin(c.Context(), global.ORM, orderID, playerID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, readout)
}
