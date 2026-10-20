package esports

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/message"
)

// 业务外盒事件投递口径（§2.1-05 ①）：
//   - 绝不写 user_uid="" 的全员广播站内信；
//   - 定向投递给事件载荷点名的管理员，载荷无管理员时投递给超管（资金/账务事件的最小可见范围）；
//   - 站内信正文不再拼接完整事件载荷（载荷只留在 esports_outbox 事件行里）；
//   - 无任何可定向收件人时明确失败，而不是降级为广播或伪成功。

func businessEventMessages(t *testing.T, db *ent.Client, eventType string) []*ent.Message {
	t.Helper()
	rows, err := db.Message.Query().
		Where(message.TypeEQ(businessEventMessageType)).
		All(t.Context())
	if err != nil {
		t.Fatalf("查询站内信失败: %v", err)
	}
	out := make([]*ent.Message, 0, len(rows))
	for _, row := range rows {
		if stored, _ := row.Extra["event_type"].(string); stored == eventType {
			out = append(out, row)
		}
	}
	return out
}

func assertNoBroadcastMessage(t *testing.T, db *ent.Client) {
	t.Helper()
	n, err := db.Message.Query().Where(message.UserUIDEQ("")).Count(t.Context())
	if err != nil {
		t.Fatalf("统计广播站内信失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("业务事件不得产生 user_uid=\"\" 的全员广播站内信，实际 %d 条", n)
	}
}

func TestBusinessEventPublisherDeliversToNamedAdminOnly(t *testing.T) {
	db := openOutboxDB(t, "esports_business_msg_named")
	defer db.Close()

	operator := seedNotificationAdmin(t, db, "uid-operator")
	_ = seedNotificationAdmin(t, db, "uid-bystander")

	publisher := &messageEventPublisher{db: db}
	payload := map[string]interface{}{
		"admin_id":          operator.ID,
		"wallet_account_id": 7,
		"note":              "SENSITIVE-PAYLOAD-SENTINEL",
	}
	if err := publisher.Publish(t.Context(), "wallet.adjust@7", "wallet.adjust", "wallet_account:7", payload); err != nil {
		t.Fatalf("业务事件定向投递失败: %v", err)
	}

	rows := businessEventMessages(t, db, "wallet.adjust")
	if len(rows) != 1 {
		t.Fatalf("定向投递应只写 1 条站内信，实际 %d 条", len(rows))
	}
	if rows[0].UserUID != operator.UID {
		t.Fatalf("站内信收件人 = %q，期望 %q", rows[0].UserUID, operator.UID)
	}
	if strings.Contains(rows[0].Content, "SENSITIVE-PAYLOAD-SENTINEL") {
		t.Fatalf("站内信正文不得拼接完整事件载荷: %q", rows[0].Content)
	}
	if _, ok := rows[0].Extra["event_key"]; !ok {
		t.Fatal("站内信缺少 event_key，重试无法幂等")
	}
	assertNoBroadcastMessage(t, db)

	// 同一 eventKey 重投不得产生重复站内信。
	if err := publisher.Publish(t.Context(), "wallet.adjust@7", "wallet.adjust", "wallet_account:7", payload); err != nil {
		t.Fatalf("重投失败: %v", err)
	}
	if again := businessEventMessages(t, db, "wallet.adjust"); len(again) != 1 {
		t.Fatalf("重复投递产生了重复站内信: %d 条", len(again))
	}
}

func TestBusinessEventPublisherFallsBackToSuperAdmins(t *testing.T) {
	db := openOutboxDB(t, "esports_business_msg_super")
	defer db.Close()

	normal := seedNotificationAdmin(t, db, "uid-normal")
	super := db.Admin.Create().SetEmail("super@example.com").SetPassword("test").
		SetUID("uid-super").SetUsername("super").SetStatus("active").SetIsSuperAdmin(true).
		SaveX(t.Context())

	publisher := &messageEventPublisher{db: db}
	payload := map[string]interface{}{"ledger_account_id": 3, "diff_cents": 1200}
	if err := publisher.Publish(t.Context(), "ledger.reconcile_anomaly@3", "ledger.reconcile_anomaly", "wallet_account:3", payload); err != nil {
		t.Fatalf("业务事件投递失败: %v", err)
	}

	rows := businessEventMessages(t, db, "ledger.reconcile_anomaly")
	if len(rows) != 1 {
		t.Fatalf("资金类业务事件应定向投递给超管 1 条，实际 %d 条", len(rows))
	}
	if rows[0].UserUID != super.UID {
		t.Fatalf("收件人 = %q，期望超管 %q", rows[0].UserUID, super.UID)
	}
	if rows[0].UserUID == normal.UID {
		t.Fatal("无归属点的资金类事件不得发给普通管理员")
	}
	assertNoBroadcastMessage(t, db)
}

func TestBusinessEventPublisherFailsWithoutDirectedRecipient(t *testing.T) {
	db := openOutboxDB(t, "esports_business_msg_none")
	defer db.Close()

	publisher := &messageEventPublisher{db: db}
	err := publisher.Publish(t.Context(), "ledger.reconcile_anomaly@9", "ledger.reconcile_anomaly", "wallet_account:9",
		map[string]interface{}{"diff_cents": 1})
	if err == nil {
		t.Fatal("无可定向收件人时必须明确失败，而不是伪成功/降级广播")
	}
	assertNoBroadcastMessage(t, db)
	if n, countErr := db.Message.Query().Count(t.Context()); countErr != nil || n != 0 {
		t.Fatalf("明确失败时不得写任何站内信: n=%d err=%v", n, countErr)
	}
}

func TestDeliverOutboxBatchBusinessEventsNeverBroadcast(t *testing.T) {
	db := openOutboxDB(t, "esports_business_msg_batch")
	defer db.Close()

	operator := seedNotificationAdmin(t, db, "uid-batch-operator")
	now := time.Now().UTC()

	// 只取「按当前通道映射仍算业务事件」的样本：settlement./order. 等前缀会随通知口径调整
	// 划入通知类事件（由通知投递器消费），这里动态过滤，避免与通知口径变更互相干扰。
	candidates := []struct {
		eventType string
		ref       string
		payload   map[string]interface{}
	}{
		{"wallet.recharge", "wallet_account:1", map[string]interface{}{"admin_id": operator.ID, "amount_cents": 500}},
		{"ledger.reconcile_anomaly", "wallet_account:1", map[string]interface{}{"admin_id": operator.ID}},
		{"commission.generated", "commission:1", map[string]interface{}{"admin_id": operator.ID}},
		{"gift.intent_confirmed", "gift_intent:1", map[string]interface{}{"admin_id": operator.ID}},
		{"sku.suspension_requested", "sku:1", map[string]interface{}{"admin_id": operator.ID}},
	}
	seeded := 0
	for i, event := range candidates {
		if eventNotifyChannels(event.eventType) != nil {
			continue // 已划入通知类事件，不在业务投递器范围内。
		}
		if _, err := db.EsportsOutbox.Create().
			SetEventKey(event.eventType + "@" + strconv.Itoa(i)).SetEventType(event.eventType).SetAggregateRef(event.ref).
			SetPayload(event.payload).SetStatus(esportsoutbox.StatusPending).SetNextAttemptAt(now).
			Save(t.Context()); err != nil {
			t.Fatalf("第 %d 个事件建行失败: %v", i, err)
		}
		seeded++
	}
	if seeded < 2 {
		t.Fatalf("当前通道映射下业务事件样本不足（%d），请补充样本", seeded)
	}

	count, err := deliverOutboxBatch(t.Context(), db, &messageEventPublisher{db: db}, now, 10)
	if err != nil {
		t.Fatalf("deliverOutboxBatch 失败: %v", err)
	}
	if count != seeded {
		t.Fatalf("投递条数 = %d，期望 %d", count, seeded)
	}
	assertNoBroadcastMessage(t, db)

	delivered, err := db.EsportsOutbox.Query().Where(esportsoutbox.StatusEQ(esportsoutbox.StatusDelivered)).Count(t.Context())
	if err != nil || delivered != seeded {
		t.Fatalf("已投递事件数 = %d（err=%v），期望 %d", delivered, err, seeded)
	}
}
