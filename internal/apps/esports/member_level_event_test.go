package esports

import (
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsoutbox"
)

// 等级变更逐次留事件（§10.5-03）：事件键含「客户 + 等级 + 业务日期」而非按秒，
// 同日同等级重复变更各留一条；保护期到期的降级执行也必须留事件。
func TestMemberLevelChangesLeaveEventPerChangeWithDayKey(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_member_level_event")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	seedMemberLevel(t, db, memberLevelInput{Code: "silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000})
	seedMemberLevel(t, db, memberLevelInput{Code: "gold", Name: "金卡", GrowthThreshold: 10000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)

	// 同一天同一秒内两次手动改为同一等级：事件键若按秒会被唯一索引吞掉，必须各留一条。
	for i, reason := range []string{"客诉补偿", "活动补偿"} {
		if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "gold", reason, 11, now); err != nil {
			t.Fatalf("第 %d 次手动调级失败：%v", i+1, err)
		}
	}
	events := db.EsportsOutbox.Query().
		Where(esportsoutbox.EventTypeEQ("member.level_manual")).AllX(ctx)
	if len(events) != 2 {
		t.Fatalf("同日两次等级变更应各留一条事件：得到 %d 条", len(events))
	}
	seen := map[string]bool{}
	for _, event := range events {
		if seen[event.EventKey] {
			t.Fatalf("等级变更事件键重复：%q", event.EventKey)
		}
		seen[event.EventKey] = true
		if !strings.Contains(event.EventKey, "20261011") || !strings.Contains(event.EventKey, "gold") {
			t.Fatalf("事件键应含客户+等级+业务日期（不按秒）：%q", event.EventKey)
		}
	}

	// 保护期到期的降级执行同样留事件（另一客户，避免与被覆盖的手动调级互相干扰）。
	other := saveCustomerOrFail(t, db, 12)
	db.EsportsGrowthLedger.Create().SetCustomerID(other.ID).SetDelta(100000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("r4wm-event-seed-up").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, other.ID, now); err != nil {
		t.Fatalf("定级失败：%v", err)
	}
	if got := db.EsportsCustomer.GetX(ctx, other.ID); got.MemberLevelCode != "gold" {
		t.Fatalf("未定级到金卡：%+v", got)
	}
	db.EsportsGrowthLedger.Create().SetCustomerID(other.ID).SetDelta(-99000).
		SetSourceType(esportsgrowthledger.SourceTypeManual).SetSourceID("r4wm-event-seed-down").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, other.ID, now); err != nil {
		t.Fatalf("重算失败：%v", err)
	}
	downgradeAt := now.Add(31 * 24 * time.Hour)
	if count, err := applyMemberDowngrades(ctx, db, downgradeAt, 100); err != nil || count != 1 {
		t.Fatalf("保护期到期未降级：%d %v", count, err)
	}
	downgraded := db.EsportsOutbox.Query().
		Where(esportsoutbox.EventTypeEQ("member.level_downgraded")).AllX(ctx)
	if len(downgraded) != 1 {
		t.Fatalf("降级执行应留一条事件：得到 %d 条", len(downgraded))
	}
	expectedDay := downgradeAt.In(cstZone).Format("20060102")
	if key := downgraded[0].EventKey; !strings.Contains(key, expectedDay) || !strings.Contains(key, "silver") {
		t.Fatalf("降级事件键应含客户+等级+业务日期：%q（期望含 %s）", key, expectedDay)
	}
}
