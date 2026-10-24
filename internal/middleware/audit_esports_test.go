package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
)

// esports 审计边界（② / ）：
//   - 价格 / 规格 / 结单要求变更路由纳入统一审计白名单；
//   - 资金与内容发布等关键变更同样留痕；
//   - 关键实体变更必须能在操作日志中查到「操作人 + 时间 + 改前改后值」。

func TestAdminAuditRulesCoverEsportsFundPriceSpecAndPublishRoutes(t *testing.T) {
	rules := AdminAuditRules()
	for _, key := range []string{
		// 价格 / 规格 / 结单要求
		"POST /api/v1/esports/admin/products",
		"PUT /api/v1/esports/admin/products/:id",
		"PUT /api/v1/esports/admin/close-rules/:id",
		"POST /api/v1/esports/admin/close-rules/:id/activate",
		"POST /api/v1/esports/admin/close-rules/:id/rollback",
		"POST /api/v1/esports/admin/products/:id/suspensions",
		"POST /api/v1/esports/admin/product-suspensions/:id/decision",
		// 订单资金
		"PUT /api/v1/esports/admin/orders/:id/quote",
		"POST /api/v1/esports/admin/price-adjustments/:id/decision",
		"POST /api/v1/esports/admin/orders/:id/pay-by-balance",
		// 钱包与提现
		"POST /api/v1/esports/admin/wallets/:id/recharge",
		"POST /api/v1/esports/admin/wallets/:id/adjust",
		"POST /api/v1/esports/admin/withdrawals/:id/review",
		"POST /api/v1/esports/admin/withdrawals/:id/pay",
		"POST /api/v1/esports/admin/withdrawals/:id/payout-request",
		"POST /api/v1/esports/admin/withdrawal-payouts/:id/paid",
		"POST /api/v1/esports/admin/withdrawal-payouts/:id/failed",
		"POST /api/v1/esports/admin/settlements/:id/pay",
		// 收款账户与到账核销
		"POST /api/v1/esports/admin/payment-accounts",
		"POST /api/v1/esports/admin/payment-accounts/:id/review",
		"POST /api/v1/esports/admin/payment-accounts/:id/disable",
		"POST /api/v1/esports/admin/payment-channels/:channel/status",
		"POST /api/v1/esports/admin/offline-payment-proofs/:id/verify",
		"POST /api/v1/esports/admin/offline-payment-proofs/:id/reject",
		// 售后赔付
		"POST /api/v1/esports/admin/after-sales/:id/approve",
		"POST /api/v1/esports/admin/after-sales/:id/execute",
		"POST /api/v1/esports/admin/after-sales/:id/refund-execute",
		"POST /api/v1/esports/admin/external-refunds",
		"POST /api/v1/esports/admin/external-refunds/:id/execute",
		// 内容发布
		"PUT /api/v1/esports/admin/pages/:channel/draft",
		"POST /api/v1/esports/admin/pages/:channel/publish",
		"POST /api/v1/esports/admin/pages/:channel/publish-review/:versionId/approve",
		"POST /api/v1/esports/admin/pages/:channel/publish-review/:versionId/reject",
	} {
		rule, ok := rules[key]
		if !ok {
			t.Errorf("审计白名单缺少 esports 关键变更路由 %s", key)
			continue
		}
		if strings.TrimSpace(rule.Module) == "" || strings.TrimSpace(rule.Action) == "" {
			t.Errorf("审计规则 %s 的模块/动作不得为空：%+v", key, rule)
		}
	}
}

// TestAuditRecordsSkuPriceChangeWithOperatorAndBeforeAfter 断言：
// 管理员把 SKU 单价从 1000 改为 2500 后，操作日志中可查到该管理员、时间与 1000→2500。
func TestAuditRecordsSkuPriceChangeWithOperatorAndBeforeAfter(t *testing.T) {
	client, svc, cleanup := setupAuditTest(t)
	defer cleanup()
	ctx := t.Context()

	sku := client.EsportsSku.Create().
		SetCode("SKU-PRICE-1").
		SetTitle("上分服务").
		SetServiceType("play").
		SetUnitPriceCents(1000).
		SetServiceUnits(1).
		SetServiceUnitName("局").
		SetStatus("on_sale").
		SaveX(ctx)

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(auditTestSecret))
	app.Use(Audit(AdminAuditRules(), svc))
	app.Put("/api/v1/esports/admin/products/:id", func(c fiber.Ctx) error {
		// 模拟真实改价 handler：只改价，不写任何手工审计埋点。
		if err := client.EsportsSku.UpdateOneID(sku.ID).SetUnitPriceCents(2500).Exec(c.Context()); err != nil {
			return err
		}
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/esports/admin/products/"+strconv.Itoa(sku.ID), strings.NewReader(`{"unit_price_cents":2500}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+makeAuditTestToken(t, "31", "operator"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	waitForOperationLogCount(t, client, 1)
	row := client.OperationLog.Query().OnlyX(ctx)
	if row.UserID != 31 || row.Username != "operator" {
		t.Fatalf("操作人未留痕: user_id=%d username=%s", row.UserID, row.Username)
	}
	if row.CreatedAt.IsZero() {
		t.Fatal("操作时间未留痕")
	}
	if strings.TrimSpace(row.Module) == "" || strings.TrimSpace(row.Action) == "" {
		t.Fatalf("模块/动作未留痕: %+v", row)
	}
	detail := row.Description + "\n" + row.RequestBody
	if !strings.Contains(detail, "1000") || !strings.Contains(detail, "2500") {
		t.Fatalf("操作日志未记录改前改后值（期望同时含 1000 与 2500）: %q", detail)
	}
	if !strings.Contains(detail, "unit_price_cents") {
		t.Fatalf("操作日志未标明变更字段: %q", detail)
	}
}

// TestAuditPrefixMountCoversFeatureSetRoutes 复刻 router.go 的实际挂载方式：
// 在 /api/v1 组上按前缀先挂审计中间件，再由功能集在之后注册路由（cmfapp.Mount）。
// 断言前缀挂载确实覆盖后注册的 esports 管理端路由，且改前改后值仍可查。
func TestAuditPrefixMountCoversFeatureSetRoutes(t *testing.T) {
	client, svc, cleanup := setupAuditTest(t)
	defer cleanup()
	ctx := t.Context()

	sku := client.EsportsSku.Create().
		SetCode("SKU-PRICE-2").
		SetTitle("陪练服务").
		SetServiceType("play").
		SetUnitPriceCents(3000).
		SetServiceUnits(2).
		SetServiceUnitName("局").
		SetStatus("on_sale").
		SaveX(ctx)

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(auditTestSecret))
	v1 := app.Group("/api/v1")
	// 与 router.go 中的挂载点保持一致：必须早于功能集路由注册。
	v1.Use("/esports/admin", Audit(AdminAuditRules(), svc))
	// 模拟 cmfapp.Mount 的注册方式：在 v1 的子分组上追加路由（功能集挂在 /esports 下）。
	group := v1.Group("/esports")
	group.Add([]string{http.MethodPut}, "/admin/products/:id", func(c fiber.Ctx) error {
		if err := client.EsportsSku.UpdateOneID(sku.ID).SetMinUnitPriceCents(2900).Exec(c.Context()); err != nil {
			return err
		}
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/esports/admin/products/"+strconv.Itoa(sku.ID), nil)
	req.Header.Set("Authorization", "Bearer "+makeAuditTestToken(t, "44", "operator2"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	waitForOperationLogCount(t, client, 1)
	row := client.OperationLog.Query().OnlyX(ctx)
	if row.UserID != 44 {
		t.Fatalf("操作人未留痕: %+v", row)
	}
	if !strings.Contains(row.Description, "min_unit_price_cents") ||
		!strings.Contains(row.Description, "0→2900") {
		t.Fatalf("前缀挂载下未记录改前改后值: %q", row.Description)
	}
}

// TestAuditSkipsSnapshotWhenEntityMissing 保证快照抓取失败不会影响请求与日志本身。
func TestAuditSkipsSnapshotWhenEntityMissing(t *testing.T) {
	client, svc, cleanup := setupAuditTest(t)
	defer cleanup()
	ctx := t.Context()

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(auditTestSecret))
	app.Use(Audit(AdminAuditRules(), svc))
	app.Put("/api/v1/esports/admin/products/:id", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/esports/admin/products/999", nil)
	req.Header.Set("Authorization", "Bearer "+makeAuditTestToken(t, "31", "operator"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	waitForOperationLogCount(t, client, 1)
	if n, err := client.OperationLog.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("实体不存在时仍应留痕一条操作日志: n=%d err=%v", n, err)
	}
}
