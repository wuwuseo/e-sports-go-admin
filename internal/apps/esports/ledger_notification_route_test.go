package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/migrate"
)

// 账本核验异常的通知投递回归：
// ledger_reconcile.go / ledger_sum_reconcile.go 已把 5 类异常统一产出为 ledger.* 事件（收件人写在
// payload.admin_id，另带 recipient_roles 标注 finance / super_admin），但通知分拣不认这个前缀时
// 事件只会写进外盒、不会真的送达。这里验证两处移交都已补齐：
//   - 渠道映射命中 wecom_app（通知消费者才会认领并调用企微通道）；
//   - 收件人按 payload.admin_id 解析到财务 / 超管本人（不广播、不空地址）。
// 同时验证未配置通道时走死信 + 失败待办，绝不「假装成功」。

// ledgerReconcileAnomaliesQueryPath 与 ledger_sum_reconcile.go 写进载荷的 query_endpoint 一致
// （账本异常复核入口），此处用字面量断言通知摘要里确实带出了复核入口。
const ledgerReconcileAnomaliesQueryPath = "/api/v1/esports/admin/ledger-reconcile-anomalies"

// openLedgerNotificationDB 打开唯一 DSN 的内存 SQLite。
func openLedgerNotificationDB(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { db.Close() })
	return db
}

// seedLedgerNotificationAdmin 造一个后台可寻址的管理员（站内信按 Admin.UID 寻址）。
func seedLedgerNotificationAdmin(t *testing.T, db *ent.Client, uid string) *ent.Admin {
	t.Helper()
	return db.Admin.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").SaveX(t.Context())
}

// ledgerAnomalyNoticePayload 复刻 publishLedgerReconcileNotices 的载荷形态。
func ledgerAnomalyNoticePayload(adminID int, roles ...string) map[string]interface{} {
	return map[string]interface{}{
		"admin_id":        adminID,
		"recipient_roles": roles,
		"account_type":    "customer_balance",
		"account_id":      "42",
		"anomaly_kinds":   []string{"wallet_batch_sum_mismatch"},
		"anomaly_count":   1,
		"query_endpoint":  ledgerReconcileAnomaliesQueryPath,
	}
}

// 账本告警事件经分拣后：渠道命中 wecom_app、收件人解析到载荷点名的财务本人、
// 端到端真的推送企微通道并写站内信；汇总审计事件仍留在业务事件口径。
func TestLedgerNotificationEventRoutesToWecomAppAndFinanceAdmin(t *testing.T) {
	db := openLedgerNotificationDB(t, "esports_ledger_notify_route")
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

	finance := seedLedgerNotificationAdmin(t, db, "uid-ledger-finance")
	payload := ledgerAnomalyNoticePayload(finance.ID, "finance")

	// 1) 渠道映射：审核报告举例的异常种类与生产事件类型都必须命中 wecom_app。
	for _, eventType := range []string{"ledger.wallet_batch_sum_mismatch", "ledger.reconcile_anomaly_notice"} {
		channels := eventNotifyChannels(eventType)
		if len(channels) != 1 || channels[0] != notifyChannelWecomApp {
			t.Fatalf("%s 渠道映射 = %v, want [%s]", eventType, channels, notifyChannelWecomApp)
		}
	}
	// 汇总审计事件（整轮核验一条、载荷无收件人）不属于通知事件，否则每轮核验都会重试成死信。
	if channels := eventNotifyChannels(ledgerAuditAggregateEventType); channels != nil {
		t.Fatalf("%s 不应归入通知通道: %v", ledgerAuditAggregateEventType, channels)
	}

	// 2) 收件人：按 payload.admin_id 解析到财务本人，唯一且地址非空（绝不写 user_uid="" 广播）。
	recipients, err := resolveNotificationRecipients(ctx, db, "ledger.wallet_batch_sum_mismatch", "ledger", payload)
	if err != nil {
		t.Fatalf("账本告警收件人解析失败: %v", err)
	}
	if len(recipients) != 1 {
		t.Fatalf("账本告警收件人 = %+v, want 恰好 %s 一人", recipients, finance.UID)
	}
	if recipients[0].Address != finance.UID || recipients[0].Role != notificationRoleAdmin {
		t.Fatalf("账本告警收件人 = %+v, want address=%s role=%s",
			recipients[0], finance.UID, notificationRoleAdmin)
	}

	// 3) 文案：财务 / 超管不必回查载荷即可知道账户、异常类型与复核入口。
	title := notificationEventTitle("ledger.wallet_batch_sum_mismatch")
	summary := notificationEventSummary("ledger.wallet_batch_sum_mismatch", "ledger", payload)
	if !strings.Contains(title, "账本") {
		t.Fatalf("账本告警标题 = %q", title)
	}
	for _, want := range []string{"customer_balance", "42", "wallet_batch_sum_mismatch", ledgerReconcileAnomaliesQueryPath} {
		if !strings.Contains(summary, want) {
			t.Fatalf("账本告警摘要缺 %q：%q", want, summary)
		}
	}

	// 4) 端到端：通道已启用 + 网关已配置 → 真的经 wecom_app 推送，并给财务本人写站内信。
	db.SystemConfig.Create().SetKey(notificationChannelsConfigKey).SetName("通知通道").
		SetValue(notifyChannelWecomApp).SaveX(ctx)
	hits := make(chan notificationWebhookRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body notificationWebhookRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	webhookCfg, _ := json.Marshal(map[string]interface{}{
		"url": server.URL, "secret": "s", "timeout_seconds": 2,
	})
	db.SystemConfig.Create().SetKey(notificationPushWebhookConfigKey).SetName("推送网关").
		SetValue(string(webhookCfg)).SaveX(ctx)

	event := db.EsportsOutbox.Create().
		SetEventKey("ledger.reconcile_anomaly_notice:2026101910:11:customer_balance:42:wallet_batch_sum_mismatch").
		SetEventType("ledger.wallet_batch_sum_mismatch").SetAggregateRef("ledger").
		SetPayload(payload).SetNextAttemptAt(now).SaveX(ctx)

	// 通知类账本告警不得被业务消费者抢走（否则只落事件行、不会真的送达）。
	if count, err := deliverOutboxBatch(ctx, db, &recordingPublisher{}, now, 10); err != nil || count != 0 {
		t.Fatalf("业务消费者抢占了账本告警通知事件: count=%d err=%v", count, err)
	}
	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 1 {
		t.Fatalf("账本告警通知投递: count=%d err=%v", count, err)
	}
	select {
	case body := <-hits:
		if body.Channel != notifyChannelWecomApp || body.EventType != "ledger.wallet_batch_sum_mismatch" {
			t.Fatalf("企微推送通道/事件类型不正确: %+v", body)
		}
		if len(body.Recipients) != 1 || body.Recipients[0].Address != finance.UID {
			t.Fatalf("企微推送收件人 = %+v, want 仅 %s", body.Recipients, finance.UID)
		}
	default:
		t.Fatal("账本告警未调用 wecom_app 推送网关")
	}
	messages := db.Message.Query().AllX(ctx)
	if len(messages) != 1 || messages[0].UserUID != finance.UID {
		t.Fatalf("账本告警站内信应收件人为财务本人：%+v", messages)
	}
	if !strings.Contains(messages[0].Title, "账本") {
		t.Fatalf("账本告警站内信标题 = %q", messages[0].Title)
	}
	if stored := db.EsportsOutbox.GetX(ctx, event.ID); stored.Status != esportsoutbox.StatusDelivered {
		t.Fatalf("账本告警投递成功后外盒应为 delivered：%+v", stored)
	}
}

// 外部退款待执行超期督办：external_refund.overdue 必须被通知分拣认领，
// 渠道命中 wecom_app、收件人按财务优先 / 回落超管解析、中文标题与退款单号 / 超期时长进正文。
func TestExternalRefundOverdueNotificationRoutesToFinanceAndSuperAdmin(t *testing.T) {
	db := openLedgerNotificationDB(t, "esports_external_refund_notify_route")
	ctx := t.Context()

	// 1) 渠道映射与中文文案。
	channels := eventNotifyChannels(externalRefundOverdueEventType)
	if len(channels) != 1 || channels[0] != notifyChannelWecomApp {
		t.Fatalf("%s 渠道映射 = %v, want [%s]", externalRefundOverdueEventType, channels, notifyChannelWecomApp)
	}
	title := notificationEventTitle(externalRefundOverdueEventType)
	if title != "外部退款待执行已超期" {
		t.Fatalf("%s 中文标题 = %q", externalRefundOverdueEventType, title)
	}
	payload := map[string]interface{}{
		"refund_no":     "ER-NOTIFY-0001",
		"refund_id":     7,
		"order_id":      3,
		"amount_cents":  1200,
		"channel":       "wechat_work",
		"overdue":       true,
		"overdue_hours": externalRefundOverdueHours,
	}
	summary := notificationEventSummary(externalRefundOverdueEventType, "ER-NOTIFY-0001", payload)
	for _, want := range []string{"外部退款待执行已超期", "ER-NOTIFY-0001", "12.00 元", "已超期 24 小时"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("督办摘要缺 %q：%q", want, summary)
		}
	}

	// 2) 收件人：没有财务角色时回落启用中的超管；有财务成员时优先财务。
	super := db.Admin.Create().SetEmail("er-super@example.com").SetPassword("test").
		SetUID("uid-er-super").SetUsername("er-super").SetStatus("active").
		SetIsSuperAdmin(true).SaveX(ctx)
	recipients, err := resolveNotificationRecipients(ctx, db, externalRefundOverdueEventType,
		"ER-NOTIFY-0001", payload)
	if err != nil {
		t.Fatalf("督办事件收件人解析失败：%v", err)
	}
	if len(recipients) != 1 || recipients[0].Address != super.UID || recipients[0].Role != notificationRoleAdmin {
		t.Fatalf("督办事件收件人 = %+v, want 超管 %s", recipients, super.UID)
	}

	// 3) 配置财务角色后，同一事件优先发给财务成员（与账本告警同一收口口径）。
	role := db.Role.Create().SetName("finance-er").SetDisplayName("财务督办").
		SetStatus("active").SaveX(ctx)
	finance := db.Admin.Create().SetEmail("er-finance@example.com").SetPassword("test").
		SetUID("uid-er-finance").SetUsername("er-finance").SetStatus("active").SaveX(ctx)
	db.AdminRole.Create().SetAdminID(finance.ID).SetRoleID(role.ID).SaveX(ctx)
	db.SystemConfig.Create().SetKey(financeReportRoleNamesKey).SetName("财务角色名").
		SetValue("finance-er").SaveX(ctx)

	recipients, err = resolveNotificationRecipients(ctx, db, externalRefundOverdueEventType,
		"ER-NOTIFY-0001", payload)
	if err != nil {
		t.Fatalf("配置财务角色后收件人解析失败：%v", err)
	}
	if len(recipients) != 1 || recipients[0].Address != finance.UID {
		t.Fatalf("财务成员存在时督办应收件人为财务：%+v", recipients)
	}
}

// 通道未配置（notification_channels 为空）：账本告警不得被当成已投递（旧行为是分拣不认前缀、
// 直接返回成功），必须退避重试到死信并计入客服「通知发送失败」待办。
func TestLedgerNotificationEventFailsClosedWhenChannelNotConfigured(t *testing.T) {
	db := openLedgerNotificationDB(t, "esports_ledger_notify_no_channel")
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

	finance := seedLedgerNotificationAdmin(t, db, "uid-ledger-noconfig")
	payload := ledgerAnomalyNoticePayload(finance.ID, "finance")

	// 载荷没有 admin_id、也没有可用的财务 / 超管收件人时明确报收件人缺失，不降级为广播。
	if recipients, err := resolveNotificationRecipients(ctx, db, "ledger.wallet_batch_sum_mismatch", "ledger",
		map[string]interface{}{"account_type": "customer_balance", "account_id": "42"}); !errors.Is(err, errNotificationRecipientMissing) {
		t.Fatalf("无可用收件人的账本告警应报收件人缺失，err=%v recipients=%+v", err, recipients)
	}

	event := db.EsportsOutbox.Create().
		SetEventKey("ledger.reconcile_anomaly_notice:2026101910:12:customer_balance:42:wallet_batch_sum_mismatch").
		SetEventType("ledger.wallet_batch_sum_mismatch").SetAggregateRef("ledger").
		SetPayload(payload).SetNextAttemptAt(now).SaveX(ctx)

	sender := &recordingNotificationSender{}
	when := now
	for attempt := 1; attempt <= outboxMaxAttempts; attempt++ {
		count, err := deliverNotificationOutboxBatch(ctx, db, sender, when, 10)
		if err != nil || count != 1 {
			t.Fatalf("第 %d 次投递: count=%d err=%v", attempt, count, err)
		}
		when = db.EsportsOutbox.GetX(ctx, event.ID).NextAttemptAt
	}
	dead := db.EsportsOutbox.GetX(ctx, event.ID)
	if dead.Status != esportsoutbox.StatusDead || dead.Attempts != outboxMaxAttempts {
		t.Fatalf("通道未配置时账本告警必须落死信而不是假装成功：%+v", dead)
	}
	if !strings.Contains(dead.LastError, "未配置") {
		t.Fatalf("死信原因应说明通道未配置：%q", dead.LastError)
	}
	if len(sender.sends) != 0 {
		t.Fatalf("通道未配置时不应调用任何发送：%+v", sender.sends)
	}
	if failed, err := countFailedNotifications(ctx, db); err != nil || failed != 1 {
		t.Fatalf("失败待办计数 = %d err=%v", failed, err)
	}
	todos, err := csWorkbenchTodos(ctx, db, 0, true, now)
	if err != nil {
		t.Fatalf("客服待办查询失败：%v", err)
	}
	found := false
	for _, item := range todos {
		if item.Key == "notification_failed" && item.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("账本告警死信应进入 notification_failed 待办：%+v", todos)
	}
}
