package esports

import (
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspaymentaccount"
)

// 上线自检：功能开关与支付时限属于硬阻断；收款/推送/企微缺失只降级、不阻断上线。
func TestSystemReadinessBlocksWithoutGateAndDeadline(t *testing.T) {
	db := openAfterSaleDB(t, "esports_readiness_blocked")
	defer db.Close()
	ctx := t.Context()

	// 三个收款方式全部停用，验证「没有任何启用收款方式」也会被报出。
	for _, channel := range paymentChannelValues {
		db.SystemConfig.Create().SetKey(paymentChannelConfigKey(channel)).SetName(channel).
			SetValue("disabled").SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}

	report := systemReadiness(ctx, db, func(string) string { return "" })
	if report.Ready || report.Blockers != 2 {
		t.Fatalf("未配置总开关与支付时限时应有两个阻断项：%+v", report)
	}
	gate, ok := readinessOf(report, "feature_gate")
	if !ok || gate.Status != readinessMissing || !gate.Blocking {
		t.Fatalf("功能总开关应为阻断缺失项：%+v", gate)
	}
	deadline, ok := readinessOf(report, "payment_deadline")
	if !ok || deadline.Status != readinessMissing || !deadline.Blocking {
		t.Fatalf("支付时限应为阻断缺失项：%+v", deadline)
	}
	channels, ok := readinessOf(report, "payment_channels")
	if !ok || channels.Status != readinessMissing {
		t.Fatalf("全部收款方式停用时应报「无收款方式」：%+v", channels)
	}
	// 站内通知与钱包展示有内置能力，任何配置下都应 ready。
	if inapp, _ := readinessOf(report, "notification_inapp"); inapp.Status != readinessReady {
		t.Fatalf("站内通知应始终可用：%+v", inapp)
	}
	if display, _ := readinessOf(report, "wallet_display"); display.Status != readinessReady {
		t.Fatalf("钱包展示应始终可用：%+v", display)
	}
}

// 自检口径：ready 只代表配置齐备，报告必须显式声明覆盖范围与「不代表已验收」，
// 避免把配置自检当成 P0 交付依据。
func TestSystemReadinessDeclaresConfigOnlyScope(t *testing.T) {
	db := openAfterSaleDB(t, "esports_readiness_scope")
	defer db.Close()
	ctx := t.Context()

	report := systemReadiness(ctx, db, func(string) string { return "1" })
	if report.Scope == "" || report.Caveat == "" {
		t.Fatalf("自检报告必须声明 scope 与 caveat：%+v", report)
	}
	if !strings.Contains(report.Caveat, "不代表") {
		t.Fatalf("caveat 必须说明 ready 不代表功能验收：%q", report.Caveat)
	}
}

// 配置齐全时：报告 Ready、无阻断，在线收款 / 推送 / 企微均为 ready。
func TestSystemReadinessReadyWhenFullyConfigured(t *testing.T) {
	db := openAfterSaleDB(t, "esports_readiness_ready")
	defer db.Close()
	ctx := t.Context()
	seedReadinessConfig(t, db, true)

	report := systemReadiness(ctx, db, func(key string) string {
		if key == "ESPORTS_EXPERIMENTAL" {
			return "1"
		}
		return ""
	})
	if !report.Ready || report.Blockers != 0 {
		t.Fatalf("配置齐全时应可上线：%+v", report)
	}
	for _, key := range []string{
		"payment_channel:wechat_work", "notification_push", "wecom_app",
	} {
		check, ok := readinessOf(report, key)
		if !ok || check.Status != readinessReady {
			t.Fatalf("%s 应为 ready：%+v", key, check)
		}
	}
}

// 渠道已启用但没有通道参数：只降级（人工核验仍可跑），且给出可执行的动作。
func TestSystemReadinessDegradesWithoutGatewayConfig(t *testing.T) {
	db := openAfterSaleDB(t, "esports_readiness_degraded")
	defer db.Close()
	ctx := t.Context()
	seedReadinessConfig(t, db, false)

	report := systemReadiness(ctx, db, func(key string) string {
		if key == "ESPORTS_EXPERIMENTAL" {
			return "1"
		}
		return ""
	})
	if !report.Ready || report.Degraded == 0 {
		t.Fatalf("缺通道参数不应阻断上线，但应计入降级：%+v", report)
	}
	check, ok := readinessOf(report, "payment_channel:wechat_work")
	if !ok || check.Status != readinessDegraded || check.Action == "" {
		t.Fatalf("缺通道参数应降级并给出动作：%+v", check)
	}
	if push, _ := readinessOf(report, "notification_push"); push.Status != readinessReady {
		t.Fatalf("推送配置正确时不应降级：%+v", push)
	}
	if wecom, _ := readinessOf(report, "wecom_app"); wecom.Status != readinessReady {
		t.Fatalf("企微配置完整时不应降级：%+v", wecom)
	}
}

// readinessOf 按 key 取自检项。
func readinessOf(report *readinessReport, key string) (readinessCheck, bool) {
	for _, check := range report.Checks {
		if check.Key == key {
			return check, true
		}
	}
	return readinessCheck{}, false
}

// seedReadinessConfig 写入自检所需配置；withGateway=false 时刻意不写 payment_gateways。
func seedReadinessConfig(t *testing.T, db *ent.Client, withGateway bool) {
	t.Helper()
	ctx := t.Context()
	set := func(key, value string) {
		db.SystemConfig.Create().SetKey(key).SetName(key).
			SetValue(value).SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
	set("app.esports.payment_deadline_minutes", "30")
	set("app.esports.notification_push_webhook", `{"url":"https://push.example.com/hook","secret":"sign-secret"}`)
	set("app.esports.wecom_corp_id", "corp-001")
	set("app.esports.wecom_agent_id", "100001")
	set("app.esports.wecom_secret", "wecom-secret")
	if withGateway {
		set("app.esports.payment_gateways",
			`{"wechat_work":{"kind":"http_hmac","base_url":"https://pay.example.com","app_id":"studio-001","app_secret":"pay-secret","notify_url":"https://studio.example.com/api/v1/esports/payment-notify/wechat_work","enabled":true}}`)
		db.EsportsPaymentAccount.Create().SetChannel("wechat_work").
			SetSubjectName("工作室收款").SetAccountNo("6222000011112222").
			SetStatus(esportspaymentaccount.StatusActive).SaveX(ctx)
	}
}
