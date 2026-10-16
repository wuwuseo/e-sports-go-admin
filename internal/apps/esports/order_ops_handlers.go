package esports

import (
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
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := cancelOrder(c.Context(), global.ORM, id, adminID, in.Reason, time.Now())
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

// walletMutationHandler 充值 / 调整共用：区分点只在 entry_type，幂等键取自请求头。
func walletMutationHandler(adjust bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		adminID, err := requireAdmin(c)
		if err != nil {
			return businessError(c, err)
		}
		accountType := c.Query("account_type")
		ownerID, err := parsePathID(c, "钱包归属人")
		if err != nil {
			return businessError(c, err)
		}
		if strings.TrimSpace(accountType) == walletCustomerBalance {
			isSuper, _ := c.Locals("isSuperAdmin").(bool)
			if _, err := loadOwnedCustomer(c.Context(), global.ORM, adminID, isSuper, ownerID); err != nil {
				return businessError(c, err)
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
