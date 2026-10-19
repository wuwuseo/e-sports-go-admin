package esports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-admin/internal/apps/esports/schematier"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscloseformfield"
	"go-admin/internal/ent/esportscloseformsnapshot"
	"go-admin/internal/ent/esportscloseformtemplate"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
)

// poSeedSpecGroup SKU：造一个引用规格组的在售 SKU（同组两档位用同一 group）。
// §15.3-01：多人 SKU 须声明每席承诺服务量，整单服务量 = 每席量 × 席位数。
func poSeedSpecGroupSku(t *testing.T, db *ent.Client, code string, groupID int, price int64) *ent.EsportsSku {
	t.Helper()
	sku, err := saveSku(t.Context(), db, skuInput{
		Code: code, Title: code, ServiceType: "play", UnitPriceCents: price,
		MinUnitPriceCents: price / 2, ServiceUnits: 2, ServiceUnitName: "小时",
		RequiredSeats: 2, PerSeatServiceUnits: 1, QuotaTotal: -1,
		ServiceDurationMinutes: 60, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	return db.EsportsSku.UpdateOneID(sku.ID).SetSpecGroupID(groupID).SaveX(t.Context())
}

// poSeedPendingIntent 给订单造一条待支付意图（用于断言改价后旧意图被作废）。
func poSeedPendingIntent(t *testing.T, db *ent.Client, order *ent.EsportsOrder) *ent.EsportsPaymentIntent {
	t.Helper()
	return db.EsportsPaymentIntent.Create().SetIntentNo("POINT" + order.OrderNo[:8]).
		SetOrderID(order.ID).SetConfirmationID(1).SetQuoteVersion(order.QuoteVersion).
		SetAmountCents(order.PayableCents).SetChannel("wechat_work").
		SetStatus(esportspaymentintent.StatusPending).SetExpiresAt(order.PaymentDeadline).
		SaveX(t.Context())
}

// poSeedPaidOrderWithSoldQuota 造一张「已付款 + 名额已售出（quota_hold=sold）」的订单，
// 用于验证取消路径的退款单与名额退回口径。
func poSeedPaidOrderWithSoldQuota(t *testing.T, db *ent.Client, code string) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: code, Title: "取消责任方商品", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po-cancel-key-"+code, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsQuotaHold.Update().SetStatus("sold").SaveX(ctx)
	db.EsportsSku.UpdateOneID(sku.ID).AddQuotaSold(1).AddQuotaReserved(-1).SaveX(ctx)
	return db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus("paid").SetFulfillmentStatus("pending_dispatch").SetPaidAt(time.Now().UTC()).
		SaveX(ctx)
}

// ── 3.3-01 改价支持改规格 / 档位：作废旧支付意图、抬升报价版本、按新规格快照重算 ──

func TestRound4PO3301RepriceSpecSwitch(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_reprice_spec")
	defer db.Close()
	ctx := t.Context()
	poSeedBaseConfigs(t, db)
	group := db.EsportsSpecGroup.Create().SetCode("PO-RP-GRP").SetName("改规格组").
		SetPerSeatServiceUnits(1).
		SetSeats([]schematier.SeatSpec{
			{SeatIndex: 0, Role: "坦克", PromisedUnits: 1}, {SeatIndex: 1, Role: "输出", PromisedUnits: 1},
		}).SaveX(ctx)
	skuA := poSeedSpecGroupSku(t, db, "PO-RP-A", group.ID, 10000)
	skuB := poSeedSpecGroupSku(t, db, "PO-RP-B", group.ID, 16000)
	otherGroup := db.EsportsSpecGroup.Create().SetCode("PO-RP-GRP2").SetName("另一组").
		SetPerSeatServiceUnits(1).
		SetSeats([]schematier.SeatSpec{{SeatIndex: 0, Role: "教练", PromisedUnits: 1}}).SaveX(ctx)
	skuC := poSeedSpecGroupSku(t, db, "PO-RP-C", otherGroup.ID, 20000)
	customer := saveCustomerOrFail(t, db, 11)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: skuA.ID, Quantity: 1},
		"po-rp-001", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	intent := poSeedPendingIntent(t, db, order)

	// 跨规格组改档位 → 参数错误。
	if _, err = repriceOrder(ctx, db, order.ID, 11, true, repriceInput{
		ExpectedVersion: order.Version, Quantity: 1, SkuID: skuC.ID, Reason: "换档位",
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("跨规格组改档位应被拒：%v", err)
	}

	// 同组改档位：按新规格价重算、抬升报价版本、作废旧支付意图、重快照席位。
	quote, err := previewReprice(ctx, db, order.ID, repriceInput{
		ExpectedVersion: order.Version, Quantity: 1, SkuID: skuB.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !quote.SpecChanged || quote.SkuID != skuB.ID || quote.PayableCents != 16000 || quote.UnitPriceCents != 16000 {
		t.Fatalf("改档位试算不正确：%+v", quote)
	}
	updated, err := repriceOrder(ctx, db, order.ID, 11, true, repriceInput{
		ExpectedVersion: order.Version, Quantity: 1, SkuID: skuB.ID, Reason: "客户升级档位",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SkuID != skuB.ID || updated.PayableCents != 16000 || updated.QuoteVersion != order.QuoteVersion+1 {
		t.Fatalf("改档位未按新规格落单：%+v", updated)
	}
	if updated.SeatRoles == nil || len(updated.SeatRoles) != 2 {
		t.Fatalf("改档位未重快照席位：%+v", updated.SeatRoles)
	}
	reloaded, err := db.EsportsPaymentIntent.Get(ctx, intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != esportspaymentintent.StatusInvalidated {
		t.Fatalf("改档位应作废旧支付意图：%+v", reloaded.Status)
	}
	event, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.repriced"),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(event.Details["spec_changed"]) != "true" {
		t.Fatalf("改档位未留痕：%+v", event.Details)
	}
	// 旧规格名额退回、新规格占位。
	if got := db.EsportsSku.GetX(ctx, skuA.ID).QuotaReserved; got != 0 {
		t.Fatalf("旧规格名额未退回：%d", got)
	}
	if got := db.EsportsSku.GetX(ctx, skuB.ID).QuotaReserved; got != 1 {
		t.Fatalf("新规格未占位：%d", got)
	}
}

// ── 3.3-08 取消时落责任方 ──

func TestRound4PO3308CancelRecordsFaultParty(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_cancel_fault")
	defer db.Close()
	ctx := t.Context()
	poSeedConfig(t, db, cancelFreeHoursKey, "24")
	poSeedConfig(t, db, cancelFeeBpKey, "500")
	poSeedBaseConfigs(t, db)
	now := time.Now().UTC()

	withFault := poSeedPaidOrderWithSoldQuota(t, db, "PO-FAULT-1")
	_, err := cancelOrderWithFault(ctx, db, withFault.ID, 11, "陪玩临时缺席", "player", now)
	if err != nil {
		t.Fatal(err)
	}
	ticket := db.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ("order"), esportsaftersale.SourceIDEQ(withFault.ID),
	).OnlyX(ctx)
	if ticket.FaultParty != esportsaftersale.FaultPartyPlayer {
		t.Fatalf("取消未落责任方：%+v", ticket.FaultParty)
	}

	// 未指定责任方时不得停在 undetermined。
	plain := poSeedPaidOrderWithSoldQuota(t, db, "PO-FAULT-2")
	if _, err = cancelOrder(ctx, db, plain.ID, 11, "客户临时有事", now); err != nil {
		t.Fatal(err)
	}
	second := db.EsportsAfterSale.Query().Where(
		esportsaftersale.SourceTypeEQ("order"), esportsaftersale.SourceIDEQ(plain.ID),
	).OnlyX(ctx)
	if second.FaultParty == esportsaftersale.FaultPartyUndetermined {
		t.Fatalf("取消后责任方仍为 undetermined：%+v", second.FaultParty)
	}

	// 非法责任方 → 参数错误。
	third := poSeedPaidOrderWithSoldQuota(t, db, "PO-FAULT-3")
	if _, err = cancelOrderWithFault(ctx, db, third.ID, 11, "客户临时有事", "who", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法责任方应被拒：%v", err)
	}
}

// ── 3.3-11 冻结拦截统一兜底 ──

func TestRound4PO3311FrozenOrderWriteGuard(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_frozen_guard")
	defer db.Close()
	ctx := t.Context()
	order := db.EsportsOrder.Create().SetOrderNo("po-frozen-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SaveX(ctx)

	if err := ensureOrderWritable(ctx, db, order.ID); err != nil {
		t.Fatalf("未冻结订单应可写：%v", err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).SetDepositStatus("frozen").SaveX(ctx)
	// 即使调用方拿着冻结前的旧快照，兜底守卫也必须按库内最新状态拦下。
	if err := ensureOrderWritable(ctx, db, order.ID); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结订单写操作未被兜底守卫拦下：%v", err)
	}
}

// ── 3.3-12 补录单按指定陪玩生成席位 / 服务段，使陪玩分成可计提 ──

func TestRound4PO3312BackfillRegistersPlayersForCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_po_backfill_players")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "PO-BACKFILL-PLAYERS")
	sku = db.EsportsSku.UpdateOneID(sku.ID).SetRequiredSeats(2).SaveX(ctx)
	seedActiveRule(t, db, ruleInput{
		RuleNo: "CR-PO-BACKFILL", Scope: "global", CalcMode: "percent",
		ValueBp: 3000, UnitBasis: "sku_quantity",
	})
	playerOne := seedPlayer(t, db, 9701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	playerTwo := seedPlayer(t, db, 9702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 未指定陪玩 → 与既有口径一致：不建席位 / 服务段，陪玩分成为 0 条。
	plain, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2,
		SettledAt: now.AddDate(0, 0, -3).Format(time.RFC3339), Reason: "历史成交补录",
	}, "po-backfill-plain", now)
	if err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(plain.ID)).CountX(ctx); n != 0 {
		t.Fatalf("未指定陪玩时不应建服务段：%d", n)
	}

	// 指定两名陪玩 → 生成席位 / 服务段骨架，并计提 kind=service 陪玩分成。
	order, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2,
		SettledAt: now.AddDate(0, 0, -3).Format(time.RFC3339), Reason: "历史成交补录带陪玩",
		PlayerIDs: []int{playerOne.ID, playerTwo.ID},
	}, "po-backfill-players", now)
	if err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID)).CountX(ctx); n != 2 {
		t.Fatalf("补录单应生成两条服务段骨架：%d", n)
	}
	if n := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).CountX(ctx); n == 0 {
		t.Fatalf("补录单结单后应有 kind=service 的陪玩分成明细")
	}

	// 超出席位数 / 重复 / 不存在 → 参数错误。
	for _, bad := range [][]int{{playerOne.ID, playerTwo.ID, 999999}, {playerOne.ID, playerOne.ID}, {999999}} {
		if _, err = backfillOrder(ctx, db, 11, backfillOrderInput{
			CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: now.AddDate(0, 0, -3).Format(time.RFC3339), Reason: "非法陪玩登记",
			PlayerIDs: bad,
		}, fmt.Sprintf("po-backfill-bad-%d", len(bad)), now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("非法陪玩登记 %v 应被拒：%v", bad, err)
		}
	}
}

// ── 3.4-01 内置结单模板 + 强制「服务结果」+ 未配置提交规则时多人单逐人上报 ──

func TestRound4PO3401BuiltinCloseFormAndSubmitRule(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_close_form_builtin")
	defer db.Close()
	ctx := t.Context()
	poSeedBaseConfigs(t, db)
	// 默认口径：内置模板开关关闭，「服务结果」的必要性完全由适用模板的绑定决定。
	if closeFormRequireServiceResult(ctx, db) {
		t.Fatalf("内置结单模板开关默认应为关闭（否则会改变存量结单口径）")
	}

	// ① 列表即兜底生成内置模板，并且内置模板带「服务结果」字段。
	templates, err := listCloseFormTemplates(ctx, db, 50)
	if err != nil {
		t.Fatal(err)
	}
	builtin := (*ent.EsportsCloseFormTemplate)(nil)
	for _, item := range templates {
		if item.Code == "builtin_default" && item.Status == esportscloseformtemplate.StatusActive {
			builtin = item
		}
	}
	if builtin == nil {
		t.Fatalf("初始化后应存在 active 的内置结单模板：%+v", templates)
	}
	if len(builtin.Fields) == 0 || builtin.Fields[0].FieldKey != "service_result" {
		t.Fatalf("内置模板未采集服务结果：%+v", builtin.Fields)
	}
	if n := db.EsportsCloseFormField.Query().Where(
		esportscloseformfield.KeyEQ("service_result")).CountX(ctx); n != 1 {
		t.Fatalf("内置服务结果字段应唯一存在：%d", n)
	}

	customer := saveCustomerOrFail(t, db, 11)
	// 启用内置结单模板 + 强制采集服务结果（§3.4-01 ①②，默认关闭，需显式开启）。
	poSeedConfig(t, db, "app.esports.close_form_require_service_result", "true")
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-CLOSE-1", Title: "多人结单商品", ServiceType: "play", UnitPriceCents: 9000,
		ServiceUnits: 2, ServiceUnitName: "小时", RequiredSeats: 2, PerSeatServiceUnits: 1,
		QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po-close-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.CloseFormTemplateID == nil {
		t.Fatalf("新单应锁定结单模板（含内置）：%+v", order)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus("pending_close").SaveX(ctx)
	// 两条服务段：仅第 1 席上报。
	seat := func(index int, reported int64, evidence string) {
		db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(9800 + index).
			SetSeatIndex(index).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(now.Add(-time.Hour)).SetEndedAt(now).
			SetReportedUnits(reported).SetSystemSeconds(1800).
			SetEvidenceRef(evidence).SaveX(ctx)
	}
	// 第 2 席既无上报量也无凭证 → 不构成有效个人上报。
	seat(1, 1, "po-close-1.png")
	seat(2, 0, "")

	// ③ 未配置提交规则：多人单未逐人上报前不得结单（先填服务结果，隔离提交规则这一项）。
	closed := closeOrderInput{
		Note:       "试图结单",
		FormValues: map[string]any{"service_result": "第 1 席已完成，第 2 席未上报"},
	}
	_, err = closeOrder(ctx, db, order.ID, 11, closed, now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "逐人上报") {
		t.Fatalf("多人单未逐人上报应被拒：%v", err)
	}

	// 补上第 2 席上报后，缺「服务结果」仍被拒。
	db.EsportsServiceSegment.Update().Where(
		esportsservicesegment.OrderIDEQ(order.ID), esportsservicesegment.SeatIndexEQ(2),
	).SetReportedUnits(1).SaveX(ctx)
	_, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "试图结单"}, now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "服务结果") {
		t.Fatalf("缺服务结果应被拒：%v", err)
	}

	// ② 填写服务结果 → 结单成功，并写入结单表单快照。
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{
		Note:       "服务完成",
		FormValues: map[string]any{"service_result": "两局均按约定完成，客户确认无异议"},
	}, now); err != nil {
		t.Fatalf("填写服务结果后应可结单：%v", err)
	}
	snapshot, err := db.EsportsCloseFormSnapshot.Query().Where(
		esportscloseformsnapshot.OrderIDEQ(order.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(snapshot.Values["service_result"]) != "两局均按约定完成，客户确认无异议" {
		t.Fatalf("结单快照未落服务结果：%+v", snapshot.Values)
	}
}
