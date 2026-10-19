package esports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsrechargecampaign"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/migrate"
)

func openCampaignDB(t *testing.T) *ent.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:esports_campaign_%s?mode=memory&cache=shared&_fk=1",
		strings.ReplaceAll(t.Name(), "/", "_"))
	db := enttest.Open(t, "sqlite3", dsn,
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// createActiveCampaign 建一个活动并切到 active，供充值匹配使用。
func createActiveCampaign(t *testing.T, db *ent.Client, in campaignInput) *ent.EsportsRechargeCampaign {
	t.Helper()
	item, err := createCampaign(t.Context(), db, in)
	if err != nil {
		t.Fatalf("创建活动失败：%v", err)
	}
	item, err = setCampaignStatus(t.Context(), db, item.ID, item.Version, "active")
	if err != nil {
		t.Fatalf("启用活动失败：%v", err)
	}
	return item
}

func baseCampaignInput(code string, now time.Time) campaignInput {
	return campaignInput{
		Code: code, Name: "测试活动", ActivityType: "ladder",
		Tiers:     []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 500}},
		Audience:  "all",
		StackRule: "stack", BonusValidDays: 0,
		StartsAt: now, EndsAt: now.Add(24 * time.Hour),
	}
}

// 阶梯匹配取满足门槛的最高档。
func TestCampaignTierBonusPicksHighestTier(t *testing.T) {
	tiers := []schema.CampaignTier{
		{ThresholdCents: 5000, BonusCents: 500},
		{ThresholdCents: 10000, BonusCents: 1200},
	}
	cases := []struct {
		principal  int64
		wantThresh int64
		wantBonus  int64
	}{
		{10000, 10000, 1200},
		{12001, 10000, 1200},
		{4999, 0, 0},
	}
	for _, tc := range cases {
		threshold, bonus := campaignTierBonus(tiers, tc.principal)
		if threshold != tc.wantThresh || bonus != tc.wantBonus {
			t.Fatalf("实充 %d 匹配错误：得到 (%d,%d)，期望 (%d,%d)",
				tc.principal, threshold, bonus, tc.wantThresh, tc.wantBonus)
		}
	}
}

// 活动配置校验、code 冲突、版本冲突与状态流转。
func TestCampaignValidationAndStatusFlow(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	base := baseCampaignInput("C-VALID", now)

	invalid := []campaignInput{}
	empty := base
	empty.Code = "C-EMPTY"
	empty.Tiers = nil
	invalid = append(invalid, empty)
	zero := base
	zero.Code = "C-ZERO"
	zero.Tiers = []schema.CampaignTier{{ThresholdCents: 0, BonusCents: 100}}
	invalid = append(invalid, zero)
	negBonus := base
	negBonus.Code = "C-NEG"
	negBonus.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 0}}
	invalid = append(invalid, negBonus)
	notAsc := base
	notAsc.Code = "C-ASC"
	notAsc.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 500}, {ThresholdCents: 5000, BonusCents: 600}}
	invalid = append(invalid, notAsc)
	badWindow := base
	badWindow.Code = "C-WIN"
	badWindow.EndsAt = base.StartsAt
	invalid = append(invalid, badWindow)
	badAudience := base
	badAudience.Code = "C-AUD"
	badAudience.Audience = "specified"
	badAudience.AudienceCustomerIDs = nil
	invalid = append(invalid, badAudience)
	for _, in := range invalid {
		if _, err := createCampaign(ctx, db, in); !errors.Is(err, errInvalidInput) {
			t.Fatalf("非法配置未被拦截（%s）：%v", in.Code, err)
		}
	}
	if count := db.EsportsRechargeCampaign.Query().CountX(ctx); count != 0 {
		t.Fatalf("校验失败仍写库：%d 条", count)
	}

	created, err := createCampaign(ctx, db, base)
	if err != nil {
		t.Fatal(err)
	}
	dup := base
	dup.Name = "重复编码"
	if _, err := createCampaign(ctx, db, dup); !errors.Is(err, errConflict) {
		t.Fatalf("重复 code 未被拦截：%v", err)
	}
	renamed := base
	renamed.Name = "改名"
	if _, err := updateCampaign(ctx, db, created.ID, created.Version+5, renamed); !errors.Is(err, errConflict) {
		t.Fatalf("错误版本更新未被拦截：%v", err)
	}
	updated, err := updateCampaign(ctx, db, created.ID, created.Version, renamed)
	if err != nil || updated.Name != "改名" {
		t.Fatalf("正常更新失败：%v", err)
	}
	flow := updated
	for _, status := range []string{"active", "paused", "active"} {
		flow, err = setCampaignStatus(ctx, db, flow.ID, flow.Version, status)
		if err != nil {
			t.Fatalf("状态流转到 %s 失败：%v", status, err)
		}
	}
	flow, err = setCampaignStatus(ctx, db, flow.ID, flow.Version, "ended")
	if err != nil {
		t.Fatalf("结束活动失败：%v", err)
	}
	if _, err := setCampaignStatus(ctx, db, flow.ID, flow.Version, "active"); !errors.Is(err, errConflict) {
		t.Fatalf("ended 回退未被拦截：%v", err)
	}
}

// 按活动充值：命中阶梯、写入快照与赠送有效期，本金与赠送分列。
func TestRechargeWithCampaignMatchesTierAndSnapshots(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	in := baseCampaignInput("LAD1", now)
	in.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 500}, {ThresholdCents: 10000, BonusCents: 1200}}
	in.BonusValidDays = 30
	createActiveCampaign(t, db, in)

	view, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		10000, "按活动充值", "camp-recharge-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if view.PrincipalCents != 10000 || view.BonusCents != 1200 {
		t.Fatalf("本金/赠送分列不正确：%+v", view)
	}
	tx, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.OwnerIDEQ(customer.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRecharge),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tx.PrincipalDeltaCents != 10000 || tx.BonusDeltaCents != 1200 {
		t.Fatalf("流水金额不正确：%+v", tx)
	}
	batch, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.SourceTypeEQ(campaignSourceType),
		esportsledgerentry.SourceIDEQ("camp-recharge-1"),
		esportsledgerentry.EntryRoleEQ("recharge_batch"),
	).Only(ctx)
	if err != nil {
		t.Fatalf("活动充值应写预付款批次账本: %v", err)
	}
	if batch.DeltaCents != 11200 {
		t.Fatalf("批次金额应为本金+赠送=11200，got %d", batch.DeltaCents)
	}
	if tx.RuleSnapshot == nil {
		t.Fatal("活动快照为空")
	}
	if got := snapshotTotalBonus(t, tx.RuleSnapshot); got != 1200 {
		t.Fatalf("快照赠送额不正确：%d", got)
	}
	if names := snapshotCampaignCodes(t, tx.RuleSnapshot); len(names) != 1 || names[0] != "LAD1" {
		t.Fatalf("快照活动信息不正确：%v", names)
	}
	if tx.BonusExpiresAt == nil || tx.BonusExpiresAt.Unix() != now.AddDate(0, 0, 30).Unix() {
		t.Fatalf("赠送有效期不正确：%v", tx.BonusExpiresAt)
	}
}

// 叠加规则：多个 stack 相加；存在 exclusive 时只取互斥中最大者。
func TestRechargeCampaignStackingRules(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	first := baseCampaignInput("STACK-A", now)
	first.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 500}}
	createActiveCampaign(t, db, first)
	second := baseCampaignInput("STACK-B", now)
	second.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 300}}
	createActiveCampaign(t, db, second)

	view, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		5000, "叠加充值", "stack-key-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if view.BonusCents != 800 {
		t.Fatalf("可叠加活动赠送额应相加：%d", view.BonusCents)
	}

	exclusiveA := baseCampaignInput("EXCL-A", now)
	exclusiveA.StackRule = "exclusive"
	exclusiveA.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 1000}}
	createActiveCampaign(t, db, exclusiveA)
	exclusiveB := baseCampaignInput("EXCL-B", now)
	exclusiveB.StackRule = "exclusive"
	exclusiveB.Tiers = []schema.CampaignTier{{ThresholdCents: 5000, BonusCents: 700}}
	createActiveCampaign(t, db, exclusiveB)

	view, err = rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		5000, "互斥充值", "stack-key-2", now)
	if err != nil {
		t.Fatal(err)
	}
	// view.BonusCents 是账户累计赠送余额（前一次叠加充值已注入 800），
	// 本次充值自身命中多少赠送额要看该笔流水。
	tx, err := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("stack-key-2")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tx.BonusDeltaCents != 1000 {
		t.Fatalf("互斥时应只取赠额最大的活动：%d", tx.BonusDeltaCents)
	}
	if view.BonusCents != 1800 {
		t.Fatalf("账户累计赠送余额应为 800+1000：%d", view.BonusCents)
	}
	if codes := snapshotCampaignCodes(t, tx.RuleSnapshot); len(codes) != 1 || codes[0] != "EXCL-A" {
		t.Fatalf("互斥快照应仅含赠额最大活动：%v", codes)
	}
}

// 时间窗与适用客户过滤。
func TestRechargeCampaignWindowAndAudience(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	notStarted := baseCampaignInput("WIN-LATE", now)
	notStarted.StartsAt = now.Add(time.Hour)
	notStarted.EndsAt = now.Add(2 * time.Hour)
	createActiveCampaign(t, db, notStarted)
	ended := baseCampaignInput("WIN-DONE", now)
	ended.StartsAt = now.Add(-2 * time.Hour)
	ended.EndsAt = now.Add(-time.Hour)
	createActiveCampaign(t, db, ended)

	windowCustomer := saveCustomerOrFail(t, db, 11)
	if view, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, windowCustomer.ID,
		5000, "窗口外充值", "win-key", now); err != nil || view.BonusCents != 0 {
		t.Fatalf("时间窗外应无赠送：%+v err=%v", view, err)
	}

	// 新客：已有充值记录的老客户不命中，无记录的新客户命中。
	newCustomerCampaign := baseCampaignInput("NEW-ONLY", now)
	newCustomerCampaign.Audience = "new_customer"
	newCustomerRow := createActiveCampaign(t, db, newCustomerCampaign)
	oldCustomer := saveCustomerOrFail(t, db, 12)
	if _, err := walletRecharge(ctx, db, 12, walletCustomerBalance, oldCustomer.ID,
		walletInput{PrincipalCents: 100, Note: "老客户充值"}, "old-recharge"); err != nil {
		t.Fatal(err)
	}
	if view, err := rechargeWithCampaigns(ctx, db, 12, walletCustomerBalance, oldCustomer.ID,
		5000, "老客户按活动充值", "new-key-old", now); err != nil || view.BonusCents != 0 {
		t.Fatalf("新客活动不应命中老客户：%+v err=%v", view, err)
	}
	freshCustomer := saveCustomerOrFail(t, db, 12)
	if view, err := rechargeWithCampaigns(ctx, db, 12, walletCustomerBalance, freshCustomer.ID,
		5000, "新客户按活动充值", "new-key-fresh", now); err != nil || view.BonusCents != 500 {
		t.Fatalf("新客活动应命中新客户：%+v err=%v", view, err)
	}

	// 先结束新客活动，否则它与下面的指定客户活动同时命中，无法单独验证名单过滤。
	if _, err := setCampaignStatus(ctx, db, newCustomerRow.ID, newCustomerRow.Version, "ended"); err != nil {
		t.Fatal(err)
	}
	// 指定客户：仅名单内命中。
	specified := baseCampaignInput("SPEC-ONLY", now)
	specified.Audience = "specified"
	specified.AudienceCustomerIDs = []int{freshCustomer.ID}
	createActiveCampaign(t, db, specified)
	otherCustomer := saveCustomerOrFail(t, db, 13)
	if view, err := rechargeWithCampaigns(ctx, db, 13, walletCustomerBalance, otherCustomer.ID,
		5000, "名单外充值", "spec-key-out", now); err != nil || view.BonusCents != 0 {
		t.Fatalf("指定名单外不应命中：%+v err=%v", view, err)
	}
	// freshCustomer 已在名单内；新客活动已结束，故只有指定活动命中。
	// 该客户此前已有 500 赠送，累计余额看流水自身的赠送额。
	if _, err := rechargeWithCampaigns(ctx, db, 13, walletCustomerBalance, freshCustomer.ID,
		5000, "名单内充值", "spec-key-in", now); err != nil {
		t.Fatalf("指定名单内应命中：%v", err)
	}
	inTx, err := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("spec-key-in")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inTx.BonusDeltaCents != 500 {
		t.Fatalf("指定名单内应命中：%d", inTx.BonusDeltaCents)
	}
}

// 幂等：同一 Idempotency-Key 重复充值返回 errConflict 且余额不变。
func TestRechargeCampaignIdempotency(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	createActiveCampaign(t, db, baseCampaignInput("IDEM", now))
	customer := saveCustomerOrFail(t, db, 11)

	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		5000, "幂等充值", "idem-key", now); err != nil {
		t.Fatal(err)
	}
	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		5000, "幂等充值", "idem-key", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复幂等键未被拦截：%v", err)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 5000 || balance.BonusCents != 500 {
		t.Fatalf("重复提交后余额被改动：%+v", balance)
	}
}

// §11.2-01 口径更新：陪玩猫粮**可以**接活动奖励（目标账户=player_catfood + 陪玩受众），
// 但「目标账户=customer_balance」的客户余额活动不得命中猫粮账户。
// 规格依据：台账 11.2-01「充值活动需支持奖励入账到陪玩猫粮（新增目标账户/陪玩受众配置）」，
// 原用例固化的「猫粮一律拒绝活动」已被规格推翻，故按新口径重写（未放宽断言：仍要求客户余额活动不得误发）。
func TestRechargeCampaignCatfoodIgnoresCustomerBalanceCampaign(t *testing.T) {
	db := openCampaignDB(t)
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	player := seedPlayer(t, db, 202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	balanceCampaign := baseCampaignInput("CATFOOD-CB", now)
	balanceCampaign.TargetAccountType = "customer_balance"
	createActiveCampaign(t, db, balanceCampaign)

	if _, err := rechargeWithCampaigns(ctx, db, 11, walletPlayerCatfood, player.ID,
		5000, "猫粮充值", "catfood-camp", now); err != nil {
		t.Fatalf("陪玩猫粮充值不应报错：%v", err)
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 5000 || balance.BonusCents != 0 {
		t.Fatalf("客户余额活动被误用到猫粮账户：%+v", balance)
	}
}

// 赠送到期作废：FIFO 只作废已到期批次，重复运行幂等，余额不为负。
func TestExpireWalletBonusFIFOAndIdempotent(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)

	short := baseCampaignInput("EXPIRE-SHORT", now)
	short.Tiers = []schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 1000}}
	short.BonusValidDays = 1
	shortItem := createActiveCampaign(t, db, short)
	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		1000, "短效赠送", "expire-key-short", now); err != nil {
		t.Fatal(err)
	}
	if _, err := setCampaignStatus(ctx, db, shortItem.ID, shortItem.Version, "ended"); err != nil {
		t.Fatal(err)
	}
	long := baseCampaignInput("EXPIRE-LONG", now)
	long.Tiers = []schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 2000}}
	long.BonusValidDays = 30
	createActiveCampaign(t, db, long)
	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		1000, "长效赠送", "expire-key-long", now); err != nil {
		t.Fatal(err)
	}
	shortTx, err := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("expire-key-short")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 直接插入一笔消费：先扣赠送 500。
	db.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(0).SetBonusDeltaCents(-500).
		SetSourceType("test").SetSourceID("consume-1").SetNote("测试消费").SaveX(ctx)

	// 短效批次已到期（now+1d），长效未到期（now+30d）。
	expireAt := now.Add(48 * time.Hour)
	count, err := expireWalletBonus(ctx, db, expireAt, 200)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应只作废短效批次 1 笔，实际 %d", count)
	}
	expireRows := db.EsportsWalletTx.Query().Where(
		esportswallettx.OwnerIDEQ(customer.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeExpire),
	).AllX(ctx)
	if len(expireRows) != 1 {
		t.Fatalf("作废流水条数不正确：%d", len(expireRows))
	}
	if expireRows[0].BonusDeltaCents != -500 || expireRows[0].SourceID != shortTx.TxNo {
		t.Fatalf("作废流水内容不正确：%+v（短效 tx_no=%s）", expireRows[0], shortTx.TxNo)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.BonusCents != 2000 || balance.BonusCents < 0 {
		t.Fatalf("作废后赠送余额不正确：%+v", balance)
	}
	// 重复运行不产生第二条作废流水。
	if again, err := expireWalletBonus(ctx, db, expireAt, 200); err != nil || again != 0 {
		t.Fatalf("重复运行应作废 0 笔：count=%d err=%v", again, err)
	}
	if total := db.EsportsWalletTx.Query().Where(
		esportswallettx.OwnerIDEQ(customer.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeExpire),
	).CountX(ctx); total != 1 {
		t.Fatalf("重复运行后作废流水应仍为 1 条：%d", total)
	}
}

// 永久赠送不作废；叠加限时赠送时只作废到期的那一档。
func TestRechargeCampaignPermanentBonusNeverExpires(t *testing.T) {
	db := openCampaignDB(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	permanent := baseCampaignInput("PERMANENT", now)
	permanent.BonusValidDays = 0
	createActiveCampaign(t, db, permanent)
	customer := saveCustomerOrFail(t, db, 11)
	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		5000, "永久赠送", "permanent-key", now); err != nil {
		t.Fatal(err)
	}
	tx, err := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("permanent-key")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tx.BonusExpiresAt != nil {
		t.Fatalf("永久赠送不应有到期时间：%v", tx.BonusExpiresAt)
	}
	if count, err := expireWalletBonus(ctx, db, now.AddDate(1, 0, 0), 200); err != nil || count != 0 {
		t.Fatalf("永久赠送不应被作废：count=%d err=%v", count, err)
	}

	// 永久活动与限时活动同时命中时，两个赠送批次保留各自期限。
	limited := baseCampaignInput("LIMITED-7D", now)
	limited.BonusValidDays = 7
	createActiveCampaign(t, db, limited)
	other := saveCustomerOrFail(t, db, 12)
	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, other.ID,
		5000, "永久加限时", "mix-key", now); err != nil {
		t.Fatal(err)
	}
	mixTx, err := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("mix-key")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mixTx.BonusExpiresAt != nil {
		t.Fatalf("聚合流水包含永久赠送时不应设置统一到期时间：%v", mixTx.BonusExpiresAt)
	}
	if count, err := expireWalletBonus(ctx, db, now.AddDate(0, 0, 8), 200); err != nil || count != 1 {
		t.Fatalf("应只作废限时赠送批次：count=%d err=%v", count, err)
	}
	remaining, err := walletBalanceOf(ctx, db, walletCustomerBalance, other.ID)
	if err != nil || remaining.BonusCents != 500 {
		t.Fatalf("永久赠送应保留 500 分：balance=%+v err=%v", remaining, err)
	}
	if count, err := expireWalletBonus(ctx, db, now.AddDate(1, 0, 0), 200); err != nil || count != 0 {
		t.Fatalf("重复运行不应再次作废：count=%d err=%v", count, err)
	}
}

func snapshotTotalBonus(t *testing.T, snapshot map[string]any) int64 {
	t.Helper()
	value, ok := snapshot["total_bonus_cents"].(float64)
	if !ok {
		t.Fatalf("快照缺少 total_bonus_cents：%v", snapshot)
	}
	return int64(value)
}

func snapshotCampaignCodes(t *testing.T, snapshot map[string]any) []string {
	t.Helper()
	if snapshot == nil {
		t.Fatal("快照为空")
	}
	raw, ok := snapshot["campaigns"].([]any)
	if !ok {
		t.Fatalf("快照缺少 campaigns：%v", snapshot)
	}
	codes := make([]string, 0, len(raw))
	for _, item := range raw {
		campaign, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("快照活动项格式不正确：%v", item)
		}
		code, _ := campaign["code"].(string)
		codes = append(codes, code)
	}
	return codes
}

func mustFindCampaignID(t *testing.T, db *ent.Client, code string) int {
	t.Helper()
	item, err := db.EsportsRechargeCampaign.Query().Where(
		esportsrechargecampaign.CodeEQ(code),
	).Only(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return item.ID
}
