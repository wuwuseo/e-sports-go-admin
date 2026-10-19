package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswallettx"
)

// P0-6 / P0-7 / P0-8 客服提成结算回归：
//   - 草稿只挂单不置 settled，发放后才置 settled；冻结明细不进结算单、不置 settled；
//   - 登记发放校验领取主体（owner_type=cs）、资金已入钱包、发放人≠登记人、重复发放 errConflict；
//   - 状态/版本条件更新：并发抬升 version 后发放返回 errConflict；
//   - 作废（软作废 status=void）回退明细并可重开，作废后原单不可再发放。

// seedCsCommissionReady 造一条生效的客服提成规则 + 一张已结单订单，返回由此产生的待结算提成明细。
func seedCsCommissionReady(t *testing.T, db *ent.Client, tag string) (admin, other *ent.Admin,
	order *ent.EsportsOrder, entry *ent.EsportsCsCommissionEntry, now time.Time) {
	t.Helper()
	ctx := t.Context()
	admin = seedAdmin(t, db, tag+"-admin")
	other = seedAdmin(t, db, tag+"-other")
	now = time.Now()

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-" + strings.ToUpper(tag), Scope: "global", BaseMode: "order_amount",
		CalcMode: "percent", ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatalf("建提成规则失败：%v", err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatalf("生效提成规则失败：%v", err)
	}
	order = seedCloseableOrder(t, db, "CC-"+strings.ToUpper(tag)+"-1", 10000,
		[]segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("结单失败：%v", err)
	}
	entry = db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID)).OnlyX(ctx)
	return admin, other, order, entry, now
}

// seedCsSettlementFixture 在 seedCsCommissionReady 基础上生成一张草稿结算单。
func seedCsSettlementFixture(t *testing.T, db *ent.Client, tag string) (admin, other *ent.Admin,
	settlement *ent.EsportsSettlement, entry *ent.EsportsCsCommissionEntry, now time.Time) {
	t.Helper()
	admin, other, _, entry, now = seedCsCommissionReady(t, db, tag)
	settlement, err := settleCsCommission(t.Context(), db, admin.ID,
		now.UTC().Add(-time.Hour), now.UTC().Add(time.Hour), admin.ID, now)
	if err != nil {
		t.Fatalf("生成客服提成结算单失败：%v", err)
	}
	// 重新读取明细，拿到挂单后的最新快照（settlement_id / version）。
	entry = db.EsportsCsCommissionEntry.GetX(t.Context(), entry.ID)
	return admin, other, settlement, entry, now
}

// 草稿阶段明细必须保持「待结算」且挂上结算单；发放后才置 settled。
func TestCsSettlementDraftKeepsEntriesPending(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_draft")
	defer db.Close()
	ctx := t.Context()
	admin, other, settlement, entry, now := seedCsSettlementFixture(t, db, "draft")

	if settlement.Status != esportssettlement.StatusDraft {
		t.Fatalf("生成后应为草稿，got=%s", settlement.Status)
	}
	if entry.Status != esportscscommissionentry.StatusPendingSettlement {
		t.Fatalf("草稿阶段明细必须仍未结算，got=%s", entry.Status)
	}
	if entry.SettlementID == nil || *entry.SettlementID != settlement.ID {
		t.Fatalf("草稿阶段明细应挂到结算单上：%+v", entry)
	}
	// 同一周期再生成：明细已挂单，不得重复汇总。
	if _, err := settleCsCommission(ctx, db, admin.ID,
		now.UTC().Add(-time.Hour), now.UTC().Add(time.Hour), admin.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("同一批明细不得重复生成结算单，err=%v", err)
	}
	// 锁定后仍未结算（发放登记才落已结算）。
	if _, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now); err != nil {
		t.Fatalf("锁定结算单失败：%v", err)
	}
	if got := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID).Status; got != esportscscommissionentry.StatusPendingSettlement {
		t.Fatalf("锁定阶段明细仍应待结算，got=%s", got)
	}
	if _, err := markSettlementPaid(ctx, db, settlement.ID, other.ID, "WX-2026-0001", now); err != nil {
		t.Fatalf("登记发放失败：%v", err)
	}
	if got := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID).Status; got != esportscscommissionentry.StatusSettled {
		t.Fatalf("发放后明细应转已结算，got=%s", got)
	}
}

// 陪玩结算单（金额已入猫粮账户）不得再登记外部发放。
func TestMarkSettlementPaidRejectsCatfoodOwner(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_owner")
	defer db.Close()
	ctx := t.Context()
	admin := seedAdmin(t, db, "guard-owner-admin")
	other := seedAdmin(t, db, "guard-owner-other")
	now := time.Now()
	player := db.EsportsPlayer.Create().SetUserID(90001).SetDisplayName("陪玩甲").SaveX(ctx)
	settlement := db.EsportsSettlement.Create().SetSettlementNo("ST" + uuid.NewString()).
		SetOwnerType(esportssettlement.OwnerTypePlayer).SetOwnerID(player.ID).
		SetPeriodStart(now.UTC().Add(-time.Hour)).SetPeriodEnd(now.UTC()).
		SetNetCents(1000).SetEntryCount(1).SetStatus(esportssettlement.StatusLocked).
		SetLockedByAdminID(admin.ID).SetLockedAt(now.UTC()).SaveX(ctx)

	_, err := markSettlementPaid(ctx, db, settlement.ID, other.ID, "PAY-X", now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("陪玩结算单不得登记外部发放，err=%v", err)
	}
	if !strings.Contains(err.Error(), "猫粮") {
		t.Fatalf("应给中文提示（猫粮账户），got=%q", err.Error())
	}
	if reloaded := db.EsportsSettlement.GetX(ctx, settlement.ID); reloaded.Status != esportssettlement.StatusLocked {
		t.Fatalf("被拒后状态不得变化，got=%s", reloaded.Status)
	}
}

// 已存在钱包入账流水的客服结算单同样禁止外部发放。
func TestMarkSettlementPaidRejectsWalletCredited(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_wallet")
	defer db.Close()
	ctx := t.Context()
	admin, other, settlement, _, now := seedCsSettlementFixture(t, db, "wallet")
	if _, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now); err != nil {
		t.Fatalf("锁定失败：%v", err)
	}
	db.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(admin.ID).
		SetEntryType(esportswallettx.EntryTypeRecharge).SetPrincipalDeltaCents(1000).
		SetSourceType("settlement").SetSourceID(settlement.SettlementNo).
		SetOperatorID(admin.ID).SetNote("异常入账").SaveX(ctx)

	_, err := markSettlementPaid(ctx, db, settlement.ID, other.ID, "PAY-Y", now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "已入账") {
		t.Fatalf("已入账结算单不得外部发放，err=%v", err)
	}
}

// 发放人=登记人（锁定人）被拒；换人发放成功；重复发放返回 errConflict。
func TestMarkSettlementPaidSeparationAndIdempotency(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_pay")
	defer db.Close()
	ctx := t.Context()
	admin, other, settlement, _, now := seedCsSettlementFixture(t, db, "pay")
	if _, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now); err != nil {
		t.Fatalf("锁定失败：%v", err)
	}
	if _, err := markSettlementPaid(ctx, db, settlement.ID, admin.ID, "PAY-SELF", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("发放人不得等于登记人，err=%v", err)
	}
	paid, err := markSettlementPaid(ctx, db, settlement.ID, other.ID, "PAY-OK", now)
	if err != nil {
		t.Fatalf("换人发放应成功，err=%v", err)
	}
	if paid.Status != esportssettlement.StatusPaid || paid.PayoutRef != "PAY-OK" {
		t.Fatalf("发放留痕不正确：%+v", paid)
	}
	if _, err = markSettlementPaid(ctx, db, settlement.ID, admin.ID, "PAY-AGAIN", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复发放应冲突，err=%v", err)
	}
}

// P0-8：冻结的待结算明细不进入结算单，解冻后才可结算；冻结金额在概览中单列。
func TestFrozenCsEntryExcludedFromSettlement(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_frozen")
	defer db.Close()
	ctx := t.Context()
	admin, _, order, entry, now := seedCsCommissionReady(t, db, "frozen")
	start, end := now.UTC().Add(-time.Hour), now.UTC().Add(time.Hour)

	frozen, err := freezeCsCommissionEntry(ctx, db, entry.ID, admin.ID, "客户争议", now)
	if err != nil {
		t.Fatalf("冻结应成功，err=%v", err)
	}
	if frozen.FrozenAt == nil || frozen.FrozenReason != "客户争议" || frozen.FrozenByAdminID == nil {
		t.Fatalf("冻结留痕不完整：%+v", frozen)
	}
	if frozen.Status != esportscscommissionentry.StatusPendingSettlement {
		t.Fatalf("冻结不应改状态（与状态正交），got=%s", frozen.Status)
	}
	view, err := csCommissionSummary(ctx, db, admin.ID, now)
	if err != nil {
		t.Fatalf("概览失败：%v", err)
	}
	if view.FrozenCents != 1000 || view.PendingSettlementCents != 0 {
		t.Fatalf("冻结金额应单列：%+v", view)
	}
	// 冻结明细不得进入结算单。
	if _, err = settleCsCommission(ctx, db, admin.ID, start, end, admin.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("冻结明细不得进入结算单，err=%v", err)
	}
	// 冻结中不得再次冻结。
	if _, err = freezeCsCommissionEntry(ctx, db, entry.ID, admin.ID, "再次冻结", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复冻结应冲突，err=%v", err)
	}
	if _, err = unfreezeCsCommissionEntry(ctx, db, entry.ID); err != nil {
		t.Fatalf("解冻应成功，err=%v", err)
	}
	if _, err = unfreezeCsCommissionEntry(ctx, db, entry.ID); !errors.Is(err, errConflict) {
		t.Fatalf("未冻结明细解冻应冲突，err=%v", err)
	}
	// 解冻后可正常结算。
	settlement, err := settleCsCommission(ctx, db, admin.ID, start, end, admin.ID, now)
	if err != nil {
		t.Fatalf("解冻后应可结算，err=%v", err)
	}
	if settlement.EntryCount != 1 || settlement.NetCents != 1000 {
		t.Fatalf("结算单应包含解冻后的明细：%+v", settlement)
	}
	// 已纳入结算单的明细不得冻结（避免与结算单口径冲突）。
	if _, err = freezeCsCommissionEntry(ctx, db, entry.ID, admin.ID, "又争议", now); !errors.Is(err, errConflict) {
		t.Fatalf("已挂结算单的明细不得冻结，err=%v", err)
	}
	// 已挂单的明细不得被整单冻结。
	if n, err := freezeOrderCsCommissionEntries(ctx, db, order.ID, admin.ID, "整单争议", now); err != nil || n != 0 {
		t.Fatalf("挂单明细不得被整单冻结：n=%d err=%v", n, err)
	}
}

// P0-8：发放按读取到的 version 做条件更新，并发抬升版本后返回 errConflict。
func TestPaySettlementVersionConflict(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_version")
	defer db.Close()
	ctx := t.Context()
	admin, other, settlement, _, now := seedCsSettlementFixture(t, db, "version")
	if _, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now); err != nil {
		t.Fatalf("锁定失败：%v", err)
	}
	stale := db.EsportsSettlement.GetX(ctx, settlement.ID)
	// 模拟并发操作抬升版本（事务外），发放时仍用旧版本快照。
	db.EsportsSettlement.UpdateOneID(stale.ID).AddVersion(1).SaveX(ctx)

	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatalf("开事务失败：%v", err)
	}
	err = payLockedCsSettlement(ctx, tx, stale, other.ID, "PAY-V", now)
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatalf("回滚失败：%v", rollbackErr)
	}
	if !errors.Is(err, errConflict) {
		t.Fatalf("版本不一致应冲突，err=%v", err)
	}
	if got := db.EsportsSettlement.GetX(ctx, settlement.ID); got.Status != esportssettlement.StatusLocked {
		t.Fatalf("版本冲突不得改状态，got=%s", got.Status)
	}
}

// 作废（软作废）：明细回退待结算、原单转 void 不可再发放、留痕，重开可重新生成草稿。
func TestVoidCsSettlementReleasesAndReopens(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_void")
	defer db.Close()
	ctx := t.Context()
	admin, other, settlement, entry, now := seedCsSettlementFixture(t, db, "void")
	if _, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now); err != nil {
		t.Fatalf("锁定失败：%v", err)
	}
	if _, err := voidCsCommissionSettlement(ctx, db, settlement.ID, other.ID, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("作废必须填原因，err=%v", err)
	}
	// 作废人不得等于登记人（锁定人）。
	if _, err := voidCsCommissionSettlement(ctx, db, settlement.ID, admin.ID, "重复周期", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("作废人不得等于登记人，err=%v", err)
	}
	result, err := voidCsCommissionSettlement(ctx, db, settlement.ID, other.ID, "重复周期，重新出单", now)
	if err != nil {
		t.Fatalf("作废应成功，err=%v", err)
	}
	// 版本：settle(v1) → lock(+1=2) → void(+1=3)，与锁定也抬版本的口径一致。
	if result.ReleasedCount != 1 || result.SettlementNo != settlement.SettlementNo || result.Version != 3 {
		t.Fatalf("作废结果不正确：%+v", result)
	}
	released := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID)
	if released.Status != esportscscommissionentry.StatusPendingSettlement || released.SettlementID != nil {
		t.Fatalf("作废后明细应回退待结算且解绑，got=%+v", released)
	}
	if !strings.Contains(released.Note, "作废回退") || !strings.Contains(released.Note, "重新出单") {
		t.Fatalf("明细应留作废痕迹，got=%q", released.Note)
	}
	// 软作废：单据保留，状态 void 且留作废人 / 时间 / 原因。
	voided := db.EsportsSettlement.GetX(ctx, settlement.ID)
	if voided.Status != esportssettlement.StatusVoid || voided.VoidedByAdminID == nil ||
		*voided.VoidedByAdminID != other.ID || voided.VoidedAt == nil ||
		voided.VoidReason != "重复周期，重新出单" {
		t.Fatalf("软作废留痕不正确：%+v", voided)
	}
	// 原单不可再发放。
	if _, err := markSettlementPaid(ctx, db, settlement.ID, admin.ID, "PAY-VOIDED", now); !errors.Is(err, errConflict) {
		t.Fatalf("作废后原单不可再发放，err=%v", err)
	}
	// 操作痕迹：外盒事件含操作人与原因。
	events, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("settlement.voided"),
	).All(ctx)
	if err != nil || len(events) != 1 {
		t.Fatalf("应写一条 settlement.voided 事件，got=%d err=%v", len(events), err)
	}
	payload := events[0].Payload
	if payload["reason"] != "重复周期，重新出单" || payload["operator_id"] != float64(other.ID) {
		t.Fatalf("作废事件应含操作人与原因：%+v", payload)
	}
	// 重复作废不会二次回退。
	if _, err = voidCsCommissionSettlement(ctx, db, settlement.ID, other.ID, "再次作废", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复作废应冲突，err=%v", err)
	}
	if got := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID).Version; got != released.Version {
		t.Fatalf("重复作废不得再次回退：version %d → %d", released.Version, got)
	}

	// 重开：按同一客服同一周期重新出草稿单，明细重新挂单但仍是待结算。
	reopened, err := reopenCsCommissionSettlement(ctx, db, admin.ID,
		result.PeriodStart, result.PeriodEnd, other.ID, "重新出单", now)
	if err != nil {
		t.Fatalf("重开应成功，err=%v", err)
	}
	if reopened.Status != esportssettlement.StatusDraft || reopened.NetCents != settlement.NetCents {
		t.Fatalf("重开应出等额草稿单：%+v", reopened)
	}
	again := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID)
	if again.SettlementID == nil || *again.SettlementID != reopened.ID ||
		again.Status != esportscscommissionentry.StatusPendingSettlement {
		t.Fatalf("重开后明细应挂新单且仍待结算：%+v", again)
	}
	if _, err = reopenCsCommissionSettlement(ctx, db, admin.ID,
		result.PeriodStart, result.PeriodEnd, other.ID, "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("重开必须填原因，err=%v", err)
	}
}

// 兼容升级前的脏数据：旧逻辑在草稿阶段就把明细置 settled，作废时同样要安全回退到待结算。
func TestVoidLegacySettledDraftReleasesEntries(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_void_legacy")
	defer db.Close()
	ctx := t.Context()
	admin, _, settlement, entry, now := seedCsSettlementFixture(t, db, "legacy")
	// 模拟旧版：草稿即有明细被判为已结算。
	db.EsportsCsCommissionEntry.UpdateOneID(entry.ID).
		SetStatus(esportscscommissionentry.StatusSettled).SaveX(ctx)

	result, err := voidCsCommissionSettlement(ctx, db, settlement.ID, admin.ID, "旧数据回退", now)
	if err != nil {
		t.Fatalf("旧版草稿作废应成功，err=%v", err)
	}
	if result.ReleasedCount != 1 {
		t.Fatalf("应回退 1 条明细，got=%d", result.ReleasedCount)
	}
	released := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID)
	if released.Status != esportscscommissionentry.StatusPendingSettlement || released.SettlementID != nil {
		t.Fatalf("旧版已结算明细应回退待结算，got=%+v", released)
	}
}

// 已发放的结算单不可作废。
func TestVoidPaidSettlementRejected(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_void_paid")
	defer db.Close()
	ctx := t.Context()
	admin, other, settlement, _, now := seedCsSettlementFixture(t, db, "voidpaid")
	if _, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now); err != nil {
		t.Fatalf("锁定失败：%v", err)
	}
	if _, err := markSettlementPaid(ctx, db, settlement.ID, other.ID, "PAY-PAID", now); err != nil {
		t.Fatalf("发放失败：%v", err)
	}
	if _, err := voidCsCommissionSettlement(ctx, db, settlement.ID, admin.ID, "误操作", now); !errors.Is(err, errConflict) {
		t.Fatalf("已发放不得作废，err=%v", err)
	}
	if got := db.EsportsSettlement.GetX(ctx, settlement.ID).Status; got != esportssettlement.StatusPaid {
		t.Fatalf("作废被拒后状态不得变化，got=%s", got)
	}
}

// P0-8：锁定结算单也要抬版本，与发放 / 作废的版本守卫同口径。
func TestLockSettlementBumpsVersion(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_lock_version")
	defer db.Close()
	ctx := t.Context()
	admin, _, settlement, _, now := seedCsSettlementFixture(t, db, "lockver")
	if settlement.Version != 1 {
		t.Fatalf("草稿版本应为 1，got=%d", settlement.Version)
	}
	locked, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now)
	if err != nil {
		t.Fatalf("锁定失败：%v", err)
	}
	if locked.Version != 2 {
		t.Fatalf("锁定应抬版本到 2，got=%d", locked.Version)
	}
}

// P0-8：结算单列表默认不带作废单，显式 status=void 才返回作废单，status=all 返回全部。
func TestListSettlementsStatusFilter(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_list")
	defer db.Close()
	ctx := t.Context()
	admin, other, settlement, _, now := seedCsSettlementFixture(t, db, "list")
	items, err := listSettlements(ctx, db, 0, 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("默认列表应返回 1 张草稿单：len=%d err=%v", len(items), err)
	}
	if _, err = voidCsCommissionSettlement(ctx, db, settlement.ID, other.ID, "重出单", now); err != nil {
		t.Fatalf("作废失败：%v", err)
	}
	if items, err = listSettlements(ctx, db, 0, 100); err != nil || len(items) != 0 {
		t.Fatalf("默认列表不得含作废单：len=%d err=%v", len(items), err)
	}
	if items, err = listSettlements(ctx, db, 0, 100, "void"); err != nil || len(items) != 1 ||
		items[0].Status != esportssettlement.StatusVoid {
		t.Fatalf("status=void 应返回作废单：%+v err=%v", items, err)
	}
	if items, err = listSettlements(ctx, db, 0, 100, "all"); err != nil || len(items) != 1 {
		t.Fatalf("status=all 应返回全部：len=%d err=%v", len(items), err)
	}
	if _, err = listSettlements(ctx, db, 0, 100, "bogus"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态应被拒，err=%v", err)
	}
	// 重开后再看默认列表：新草稿可见，作废单仍只在 status=void 下可见。
	reopened, err := reopenCsCommissionSettlement(ctx, db, admin.ID,
		settlement.PeriodStart, settlement.PeriodEnd, other.ID, "重出单", now)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	if items, err = listSettlements(ctx, db, admin.ID, 100); err != nil || len(items) != 1 ||
		items[0].ID != reopened.ID {
		t.Fatalf("默认列表应只含新草稿：%+v err=%v", items, err)
	}
	if items, err = listSettlements(ctx, db, 0, 100, "void"); err != nil || len(items) != 1 {
		t.Fatalf("作废单仍应可查：len=%d err=%v", len(items), err)
	}
}

// P0-6：同周期结算单被作废后，定时任务应能重新生成（作废不算「已生成」）。
func TestGenerateDueCsSettlementsAfterVoid(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_settle_guard_auto_void")
	defer db.Close()
	ctx := t.Context()
	admin := seedAdmin(t, db, "auto-void-admin")
	operator := seedAdmin(t, db, "auto-void-operator")
	now := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-AUTO-VOID", Scope: "global", BaseMode: "order_amount",
		CalcMode: "percent", ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatalf("建提成规则失败：%v", err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatalf("生效提成规则失败：%v", err)
	}
	db.EsportsCsCommissionEntry.Create().SetIdemKey("cc-auto-void-sep").
		SetOrderID(1).SetAdminID(admin.ID).
		SetKind(esportscscommissionentry.KindService).
		SetCalcMode(esportscscommissionentry.CalcModePercent).
		SetBaseMode(esportscscommissionentry.BaseModeOrderAmount).
		SetBaseCents(10000).SetAmountCents(1000).
		SetStatus(esportscscommissionentry.StatusPendingSettlement).
		SetCreatedAt(time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)).SaveX(ctx)

	created, err := generateDueCsSettlements(ctx, db, now, 200)
	if err != nil || created != 1 {
		t.Fatalf("首轮应生成 1 张结算单：%d err=%v", created, err)
	}
	first := db.EsportsSettlement.Query().Where(
		esportssettlement.OwnerTypeEQ(esportssettlement.OwnerTypeCs),
		esportssettlement.StatusEQ(esportssettlement.StatusDraft),
	).OnlyX(ctx)
	if _, err = voidCsCommissionSettlement(ctx, db, first.ID, operator.ID, "周期重出", now); err != nil {
		t.Fatalf("作废失败：%v", err)
	}
	// 作废后同周期自动任务应重新生成（作废单不算已生成）。
	created, err = generateDueCsSettlements(ctx, db, now, 200)
	if err != nil || created != 1 {
		t.Fatalf("作废后应能重新生成：%d err=%v", created, err)
	}
	total := db.EsportsSettlement.Query().Where(
		esportssettlement.OwnerTypeEQ(esportssettlement.OwnerTypeCs),
	).CountX(ctx)
	if total != 2 {
		t.Fatalf("应保留作废单 + 新草稿共 2 张：%d", total)
	}
	entry := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.AdminIDEQ(admin.ID)).OnlyX(ctx)
	regenerated := db.EsportsSettlement.Query().Where(
		esportssettlement.OwnerTypeEQ(esportssettlement.OwnerTypeCs),
		esportssettlement.StatusEQ(esportssettlement.StatusDraft),
	).OnlyX(ctx)
	if regenerated.ID == first.ID || entry.SettlementID == nil || *entry.SettlementID != regenerated.ID {
		t.Fatalf("新草稿应重新挂单：settlement=%d entry=%+v", regenerated.ID, entry)
	}
	if again, err := generateDueCsSettlements(ctx, db, now, 200); err != nil || again != 0 {
		t.Fatalf("重新生成后应幂等：%d err=%v", again, err)
	}
}
