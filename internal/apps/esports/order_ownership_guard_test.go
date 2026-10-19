package esports

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/global"
)

// ── P0-12：收款发起与档期处置必须有「负责客服 / 超管」的订单归属边界 ──

// ownershipGuardApp 按 app.go 的真实装配方式构造管理端订单写路由：
// 两条路由都必须包 ownedOrderHandler，否则任一持权账号都能对其他客服的订单发起收款 / 全额退款。
func ownershipGuardApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Post("/admin/orders/:id/payment/start", ownedOrderHandler(adminStartOrderPaymentHandler))
	app.Post("/admin/orders/:id/appointment/disposal", ownedOrderHandler(disposeExpiredAppointmentHandler))
	return app
}

// ownershipGuardWalletApp 构造钱包写路由（与 app.go 同路径），用于验证猫粮账户的超管兜底。
func ownershipGuardWalletApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Post("/admin/wallets/:id/recharge", walletMutationHandler(false))
	app.Post("/admin/wallets/:id/adjust", walletMutationHandler(true))
	return app
}

type ownershipGuardResult struct {
	status  int
	message string
}

// ownershipGuardPost 发一次管理端 POST，返回状态码与响应 message（业务错误文案）。
func ownershipGuardPost(t *testing.T, app *fiber.App, path, key string, body any) ownershipGuardResult {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("构造请求体失败：%v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("请求失败：%v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return ownershipGuardResult{status: resp.StatusCode, message: out.Message}
}

// P0-12-01：非归属客服为他人订单发起收款被拒（403），归属客服与超管放行。
func TestOrderOwnershipGuardPaymentStart(t *testing.T) {
	db := openProductDB(t, "esports_ownership_payment_start")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Now().UTC()

	owner := seedAdminAccount(t, db, "own-pay-owner")
	other := seedAdminAccount(t, db, "own-pay-other")
	customer := saveCustomerOrFail(t, db, owner.ID)
	sku := po2SeedSku(t, db, "OWN-PAY", 1, 1, 0)
	order, err := placeOrder(ctx, db, owner.ID, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"own-pay-key-0001", now)
	if err != nil {
		t.Fatalf("落单失败：%v", err)
	}
	// 归属关系显式落到 responsible_admin_id：写入边界只看负责人，不看客户归属。
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(owner.ID).SaveX(ctx)

	previousORM := global.ORM
	global.ORM = db
	defer func() { global.ORM = previousORM }()

	path := "/admin/orders/" + strconv.Itoa(order.ID) + "/payment/start"
	body := map[string]any{"channel": "wechat_work"}

	foreign := ownershipGuardPost(t, ownershipGuardApp(other, false), path, "", body)
	if foreign.status != fiber.StatusForbidden || !strings.Contains(foreign.message, "无权访问该客户或订单") {
		t.Fatalf("非归属客服发起收款应返回 403 与中文提示，实际 %d %q", foreign.status, foreign.message)
	}
	// 未开通渠道时业务层明确失败（503）：说明请求已越过归属校验，即归属客服放行。
	mine := ownershipGuardPost(t, ownershipGuardApp(owner, false), path, "", body)
	if mine.status == fiber.StatusForbidden {
		t.Fatalf("归属客服被归属校验拦截：%q", mine.message)
	}
	if mine.status != fiber.StatusServiceUnavailable {
		t.Fatalf("归属客服应进入渠道校验（未开通 → 503），实际 %d %q", mine.status, mine.message)
	}
	// 超管兜底：与订单无归属关系的超管仍可发起收款。
	superResult := ownershipGuardPost(t, ownershipGuardApp(other, true), path, "", body)
	if superResult.status != fiber.StatusServiceUnavailable {
		t.Fatalf("超管应越过归属校验（未开通 → 503），实际 %d %q", superResult.status, superResult.message)
	}
}

// P0-12-02：非归属客服处置他人订单的过期档期被拒，且订单状态不变；归属客服改期放行。
func TestOrderOwnershipGuardAppointmentDisposal(t *testing.T) {
	db := openProductDB(t, "esports_ownership_appointment")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Now().UTC()

	owner := seedAdminAccount(t, db, "own-appt-owner")
	other := seedAdminAccount(t, db, "own-appt-other")
	order := po2SeedExpiredAppointmentOrder(t, db, "OWN-APPT-1")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(owner.ID).SaveX(ctx)
	if _, err := expireAppointmentConfirmations(ctx, db, now, 100); err != nil {
		t.Fatalf("档期到期扫描失败：%v", err)
	}
	suspended := db.EsportsOrder.GetX(ctx, order.ID)
	if suspended.AppointmentSuspendedAt == nil {
		t.Fatalf("订单未进入待处置：%+v", suspended)
	}

	previousORM := global.ORM
	global.ORM = db
	defer func() { global.ORM = previousORM }()

	path := "/admin/orders/" + strconv.Itoa(order.ID) + "/appointment/disposal"
	newStart := now.Add(72 * time.Hour)

	// 非归属客服尝试全额退款：被归属边界拦下，订单不得被取消 / 退款。
	refundDenied := ownershipGuardPost(t, ownershipGuardApp(other, false), path, "", map[string]any{
		"action": appointmentExpireActionRefund, "reason": "越权退款",
		"expected_version": suspended.Version,
	})
	if refundDenied.status != fiber.StatusForbidden {
		t.Fatalf("非归属客服处置档期应返回 403，实际 %d %q", refundDenied.status, refundDenied.message)
	}
	untouched := db.EsportsOrder.GetX(ctx, order.ID)
	if untouched.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled ||
		untouched.AppointmentStatus != esportsorder.AppointmentStatusPendingConfirm ||
		untouched.Version != suspended.Version {
		t.Fatalf("越权请求改动了订单：%+v", untouched)
	}

	// 非归属客服尝试改期：同样被拒。
	rescheduleDenied := ownershipGuardPost(t, ownershipGuardApp(other, false), path, "", map[string]any{
		"action": appointmentExpireActionReschedule, "reason": "越权改期",
		"start_at": newStart.Format(time.RFC3339), "expected_version": suspended.Version,
	})
	if rescheduleDenied.status != fiber.StatusForbidden {
		t.Fatalf("非归属客服改期应返回 403，实际 %d %q", rescheduleDenied.status, rescheduleDenied.message)
	}

	// 归属客服改期：放行并回到 request 状态。
	mine := ownershipGuardPost(t, ownershipGuardApp(owner, false), path, "", map[string]any{
		"action": appointmentExpireActionReschedule, "reason": "客户改期",
		"start_at": newStart.Format(time.RFC3339), "expected_version": suspended.Version,
	})
	if mine.status != fiber.StatusOK {
		t.Fatalf("归属客服改期应成功，实际 %d %q", mine.status, mine.message)
	}
	after := db.EsportsOrder.GetX(ctx, order.ID)
	if after.AppointmentStatus != esportsorder.AppointmentStatusRequest ||
		after.AppointmentSuspendedAt != nil {
		t.Fatalf("改期后档期状态不正确：%+v", after)
	}
}
