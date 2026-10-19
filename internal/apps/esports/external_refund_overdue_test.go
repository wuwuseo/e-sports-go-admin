package esports

import (
	"bytes"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/global"
)

// 外部退款待执行超期督办（审核报告 §8.2-12）回归：
// 超期单必须产生督办事件、未超期不产生、重复扫描不重复督办、且绝不改退款单状态与金额。

// seedExternalRefundOverdueOrder 构造一张最小订单（外键在测试库中关闭，仅用于核对额度未被改动）。
func seedExternalRefundOverdueOrder(t *testing.T, db *ent.Client, orderNo string) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).SaveX(t.Context())
}

// seedExternalRefundOverdueRow 直接写入一条指定登记时间的外部退款单（绕过登记额度校验），
// 便于构造「登记很久仍未执行」的历史挂单。
func seedExternalRefundOverdueRow(t *testing.T, db *ent.Client, txNo string, orderID int,
	amount int64, adminID int, status esportsexternalrefund.Status,
	createdAt time.Time) *ent.EsportsExternalRefund {
	t.Helper()
	return db.EsportsExternalRefund.Create().
		SetRefundNo(externalRefundNo()).SetChannel("wechat_work").
		SetTransactionNo(txNo).SetAmountCents(amount).SetOrderID(orderID).
		SetStatus(status).SetReason("超期督办测试").
		SetRegisteredByAdminID(adminID).SetCreatedAt(createdAt).SaveX(t.Context())
}

// TestExternalRefundOverdueScanSupervisesOnlyOverdue 只督办「registered 且已超阈值」的单：
// 产生 external_refund.overdue 事件，且退款单状态、金额、订单额度一概不变（只督办不代办）。
func TestExternalRefundOverdueScanSupervisesOnlyOverdue(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_overdue_scan")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	order := seedExternalRefundOverdueOrder(t, db, "ER-OVERDUE-ORDER")

	overdue := seedExternalRefundOverdueRow(t, db, "ER-OVERDUE-TX", order.ID, 1200, 11,
		esportsexternalrefund.StatusRegistered, now.Add(-48*time.Hour))
	fresh := seedExternalRefundOverdueRow(t, db, "ER-FRESH-TX", order.ID, 300, 11,
		esportsexternalrefund.StatusRegistered, now.Add(-time.Hour))
	executed := seedExternalRefundOverdueRow(t, db, "ER-EXECUTED-TX", order.ID, 500, 11,
		esportsexternalrefund.StatusExecuted, now.Add(-72*time.Hour))

	result, err := expirePendingExternalRefunds(ctx, db, now, externalRefundOverdueScanCap)
	if err != nil {
		t.Fatalf("超期督办扫描失败：%v", err)
	}
	if result.Scanned != 1 || result.Overdue != 1 || result.Notified != 1 || result.Skipped != 0 {
		t.Fatalf("超期督办统计不正确：%+v", result)
	}

	events := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(externalRefundOverdueEventType)).AllX(ctx)
	if len(events) != 1 {
		t.Fatalf("应收敛为 1 条督办事件，实际 %d 条", len(events))
	}
	event := events[0]
	if event.AggregateRef != overdue.RefundNo {
		t.Fatalf("督办事件聚合键 = %q，期望退款单号 %q", event.AggregateRef, overdue.RefundNo)
	}
	if event.EventKey != externalRefundOverdueEventKey(overdue.RefundNo, now) {
		t.Fatalf("督办事件幂等键不正确：%q", event.EventKey)
	}
	if got := payloadInt(event.Payload, "refund_id"); got != overdue.ID {
		t.Fatalf("督办载荷 refund_id = %d，期望 %d", got, overdue.ID)
	}
	if got := payloadInt(event.Payload, "amount_cents"); got != 1200 {
		t.Fatalf("督办载荷 amount_cents = %d，期望 1200", got)
	}
	if overdueFlag, ok := event.Payload["overdue"].(bool); !ok || !overdueFlag {
		t.Fatalf("督办载荷缺少「已超期」标记：%+v", event.Payload)
	}

	// 未超期与已执行单都不得产生督办事件。
	for _, keep := range []*ent.EsportsExternalRefund{fresh, executed} {
		if got := db.EsportsOutbox.Query().Where(
			esportsoutbox.AggregateRefEQ(keep.RefundNo)).CountX(ctx); got != 0 {
			t.Fatalf("非超期单 %s 产生了 %d 条督办事件", keep.RefundNo, got)
		}
	}

	// 只督办不代办：退款单状态 / 执行人 / 金额不变，订单退款额度也不动。
	stored := db.EsportsExternalRefund.GetX(ctx, overdue.ID)
	if stored.Status != esportsexternalrefund.StatusRegistered ||
		stored.AmountCents != 1200 || stored.ExecutedByAdminID != nil || stored.ExecutedAt != nil {
		t.Fatalf("督办后退款单被改动（不得自动执行 / 取消）：%+v", stored)
	}
	gotOrder := db.EsportsOrder.GetX(ctx, order.ID)
	if gotOrder.RefundedCents != 0 || gotOrder.RefundReservedCents != 0 {
		t.Fatalf("督办不应改动订单退款额度：refunded=%d reserved=%d",
			gotOrder.RefundedCents, gotOrder.RefundReservedCents)
	}
}

// TestExternalRefundOverdueScanIsHourlyIdempotent 同一单同一小时桶只督办一次：
// 重复扫描不重复写事件，进入下一个小时桶才允许再督办一条。
func TestExternalRefundOverdueScanIsHourlyIdempotent(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_overdue_dedupe")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	order := seedExternalRefundOverdueOrder(t, db, "ER-OVERDUE-DEDUPE")
	overdue := seedExternalRefundOverdueRow(t, db, "ER-DEDUPE-TX", order.ID, 800, 11,
		esportsexternalrefund.StatusRegistered, now.Add(-30*time.Hour))

	first, err := expirePendingExternalRefunds(ctx, db, now, externalRefundOverdueScanCap)
	if err != nil || first.Notified != 1 {
		t.Fatalf("首轮应督办 1 单：%+v %v", first, err)
	}
	second, err := expirePendingExternalRefunds(ctx, db, now, externalRefundOverdueScanCap)
	if err != nil {
		t.Fatalf("重复扫描失败：%v", err)
	}
	if second.Notified != 0 || second.Skipped != 1 || second.Overdue != 1 {
		t.Fatalf("同一小时桶重复扫描不应重复督办：%+v", second)
	}
	if total := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(externalRefundOverdueEventType)).CountX(ctx); total != 1 {
		t.Fatalf("同一小时桶产生了 %d 条督办事件，期望 1 条", total)
	}

	// 进入下一个小时桶：允许再督办一次（小时级去重，不是永久去重）。
	next := now.Add(time.Hour)
	third, err := expirePendingExternalRefunds(ctx, db, next, externalRefundOverdueScanCap)
	if err != nil || third.Notified != 1 {
		t.Fatalf("下一小时桶应重新督办：%+v %v", third, err)
	}
	if total := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(externalRefundOverdueEventType)).CountX(ctx); total != 2 {
		t.Fatalf("跨小时后应累计 2 条督办事件，实际 %d 条", total)
	}
	// 退款单仍停在 registered：督办不改变待执行状态。
	if got := db.EsportsExternalRefund.GetX(ctx, overdue.ID); got.Status != esportsexternalrefund.StatusRegistered {
		t.Fatalf("督办改变了退款单状态：%+v", got)
	}
}

// TestExternalRefundOverdueEventReachesSuperAdmin 督办事件走**通知消费链路**落到超管站内信
// （财务 / 超管可见），正文带退款单号，且不是 user_uid="" 的全员广播。
//
// 口径变更说明：external_refund.overdue 已归入通知类事件（渠道映射 wecom_app + 财务/超管收件人），
// 而业务消费与通知消费按事件类型**互斥**（businessEventPredicate = Not(notificationEventPredicate())），
// 因此该事件只由 deliverNotificationOutboxBatch 认领，业务消费者不得抢走它。
func TestExternalRefundOverdueEventReachesSuperAdmin(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_overdue_deliver")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	order := seedExternalRefundOverdueOrder(t, db, "ER-OVERDUE-NOTIFY")
	overdue := seedExternalRefundOverdueRow(t, db, "ER-NOTIFY-TX", order.ID, 600, 11,
		esportsexternalrefund.StatusRegistered, now.Add(-26*time.Hour))
	super := db.Admin.Create().SetEmail("overdue-super@example.com").SetPassword("test").
		SetUID("uid-overdue-super").SetUsername("overdue-super").SetStatus("active").
		SetIsSuperAdmin(true).SaveX(ctx)
	db.Admin.Create().SetEmail("overdue-normal@example.com").SetPassword("test").
		SetUID("uid-overdue-normal").SetUsername("overdue-normal").SetStatus("active").
		SetIsSuperAdmin(false).SaveX(ctx)

	if _, err := expirePendingExternalRefunds(ctx, db, now, externalRefundOverdueScanCap); err != nil {
		t.Fatalf("超期督办扫描失败：%v", err)
	}

	// 两类消费互斥：督办事件属于通知类，业务消费者不得认领（否则只落事件行、财务永远收不到）。
	if count, err := deliverOutboxBatch(ctx, db, &messageEventPublisher{db: db},
		time.Now().UTC().Add(time.Minute), 100); err != nil || count != 0 {
		t.Fatalf("业务消费者抢占了督办通知事件：count=%d err=%v", count, err)
	}
	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db},
		time.Now().UTC().Add(time.Minute), 100); err != nil || count != 1 {
		t.Fatalf("督办通知投递：count=%d err=%v", count, err)
	}

	messages := db.Message.Query().AllX(ctx)
	event := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ(externalRefundOverdueEventType)).OnlyX(ctx)
	if len(messages) != 1 {
		t.Fatalf("督办事件应只落 1 条站内信，实际 %d 条（事件状态=%s，last_error=%q）",
			len(messages), event.Status, event.LastError)
	}
	message := messages[0]
	if message.UserUID != super.UID {
		t.Fatalf("督办收件人 = %q，期望超管 %q", message.UserUID, super.UID)
	}
	if message.UserUID == "" {
		t.Fatal("督办事件降级成了 user_uid 为空的全员广播")
	}
	if !strings.Contains(message.Title, "超期") {
		t.Fatalf("督办站内信标题应为中文督办文案：%q", message.Title)
	}
	if !strings.Contains(message.Content, overdue.RefundNo) {
		t.Fatalf("督办站内信正文未带退款单号：%q", message.Content)
	}
	if event.Status != esportsoutbox.StatusDelivered {
		t.Fatalf("督办事件投递后状态 = %q，期望 delivered", event.Status)
	}
}

// TestExpirePendingExternalRefundTaskLogsCount 任务入口签名与既有任务一致（ctx, logger），
// 并按既有任务风格输出可读的处理数量文案。
func TestExpirePendingExternalRefundTaskLogsCount(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_overdue_task")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	order := seedExternalRefundOverdueOrder(t, db, "ER-OVERDUE-TASK")
	seedExternalRefundOverdueRow(t, db, "ER-TASK-TX", order.ID, 400, 11,
		esportsexternalrefund.StatusRegistered, time.Now().UTC().Add(-40*time.Hour))

	var log bytes.Buffer
	if err := expirePendingExternalRefundTask(t.Context(), &log); err != nil {
		t.Fatalf("督办任务执行失败：%v", err)
	}
	if !strings.Contains(log.String(), "超期 1 单") || !strings.Contains(log.String(), "本轮督办 1 单") {
		t.Fatalf("任务日志未输出可读的处理数量文案：%q", log.String())
	}
}

// TestExpirePendingExternalRefundsRejectsInvalidLimit 非法限流参数必须明确拒绝。
func TestExpirePendingExternalRefundsRejectsInvalidLimit(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_overdue_limit")
	defer db.Close()
	now := time.Now().UTC()
	for _, limit := range []int{0, -1, 1001} {
		if _, err := expirePendingExternalRefunds(t.Context(), db, now, limit); err == nil {
			t.Fatalf("非法限流参数 %d 未被拦截", limit)
		}
	}
}
