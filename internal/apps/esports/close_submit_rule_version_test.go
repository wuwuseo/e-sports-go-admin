package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsservicesegment"
)

// seedCloseReadyOrderWithVersionEvent 复刻线上真实状态：订单一路 bump 到待结单，
// 当前 version 上已经有一条事件（唯一索引 (order_id, order_version) 已被占用）。
// 只有从这种状态出发才复现「写旧版本号撞唯一索引」的缺陷——直接新建、version=1 且无事件的
// 测试数据会因为唯一索引恰好空着而假通过。
func seedCloseReadyOrderWithVersionEvent(t *testing.T, db *ent.Client, orderNo string, payable int64,
	adminID int, submitted []bool) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	const currentVersion = 5
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(payable).SetPayableCents(payable).
		SetPromisedServiceUnits(1).SetRequiredSeats(len(submitted)).SetResponsibleAdminID(adminID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).
		SetVersion(currentVersion).SaveX(ctx)
	db.EsportsOrderEvent.Create().SetOrderID(order.ID).SetOrderVersion(currentVersion).
		SetEventType("order.service_finished").SetActorType("system").SetActorID("system").
		SaveX(ctx)
	for i, ok := range submitted {
		create := db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(100 + i).
			SetSeatIndex(i + 1).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).SetSystemSeconds(600)
		if ok {
			create.SetReportedUnits(1).SetEvidenceRef("shot")
		}
		create.SaveX(ctx)
	}
	return order
}

// eventVersions 返回指定类型事件写入的订单版本号（升序由 ent 默认主键序保证）。
func eventVersions(t *testing.T, db *ent.Client, orderID int, eventType string) []int {
	t.Helper()
	rows := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID), esportsorderevent.EventTypeEQ(eventType),
	).AllX(t.Context())
	versions := make([]int, 0, len(rows))
	for _, row := range rows {
		versions = append(versions, row.OrderVersion)
	}
	return versions
}

// P0-2 回归：代提交留痕必须写在 bump 之后的新版本上。
func TestCloseSubstituteReportWritesBumpedVersion(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_substitute_version")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-VER-SUB", Name: "代提交",
		Scope: "global", Mode: "multi", AllowSubstitute: true, OnMissing: "block"})
	order := seedCloseReadyOrderWithVersionEvent(t, db, "sr-ver-sub", 2000, 11, []bool{true, false})

	closed, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "代提交后结单",
		SubstituteReports: []closeSubstituteReport{{SeatIndex: 2, ReportedUnits: 1, EvidenceRef: "admin-shot"}}},
		now)
	if err != nil {
		t.Fatalf("代提交路径应能结单（旧实现写旧版本号会撞唯一索引）：%v", err)
	}
	if closed.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("代提交后订单应已完成：%+v", closed.FulfillmentStatus)
	}
	versions := eventVersions(t, db, order.ID, "order.service_submitted_by_admin")
	if len(versions) != 1 {
		t.Fatalf("应有且仅有一条代提交留痕：%v", versions)
	}
	if versions[0] <= 5 {
		t.Fatalf("代提交事件须写在 bump 后的新版本上，实际 order_version=%d", versions[0])
	}
	// 事件版本必须与该订单当前的事件版本一致（先 bump 再写）。
	if versions[0] != closed.Version-1 {
		t.Fatalf("代提交事件版本应为结单前一版：事件 %d，订单 %d", versions[0], closed.Version)
	}
}

// P0-2 回归：视为已提交留痕同样必须写在 bump 之后的新版本上。
func TestCloseAutoAcceptWritesBumpedVersion(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_auto_accept_version")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-VER-AUTO", Name: "视为已提交",
		Scope: "global", Mode: "multi", OnMissing: "auto_accept"})
	order := seedCloseReadyOrderWithVersionEvent(t, db, "sr-ver-auto", 2000, 11, []bool{true, false})

	closed, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "未上报放行"}, now)
	if err != nil {
		t.Fatalf("视为已提交路径应能结单（旧实现写旧版本号会撞唯一索引）：%v", err)
	}
	if closed.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("应已完成：%+v", closed.FulfillmentStatus)
	}
	versions := eventVersions(t, db, order.ID, "order.close_submit_auto_accepted")
	if len(versions) != 1 || versions[0] <= 5 {
		t.Fatalf("视为已提交事件须写在 bump 后的新版本上：%v", versions)
	}
}

// seedSubmitOrderWithEvidence 建一张待结单订单，按 refs 逐席位建服务段：
// 空串表示该席未形成上报（无服务量、无凭证）；非空按该凭证与 1 个服务量上报。
func seedSubmitOrderWithEvidence(t *testing.T, db *ent.Client, orderNo string, refs []string, adminID int) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(2000).SetPayableCents(2000).
		SetPromisedServiceUnits(1).SetRequiredSeats(len(refs)).SetResponsibleAdminID(adminID).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	for i, ref := range refs {
		create := db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(100 + i).
			SetSeatIndex(i + 1).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).SetSystemSeconds(600)
		if ref != "" {
			create.SetReportedUnits(1).SetEvidenceRef(ref)
		}
		create.SaveX(ctx)
	}
	return order
}

// seedActiveCloseRule 建一条要求最少图片张数的生效结单规则。
func seedActiveCloseRule(t *testing.T, db *ent.Client, code string, minEvidence int) {
	t.Helper()
	ctx := t.Context()
	rule, err := createCloseRule(ctx, db, closeRuleInput{Code: code, Name: "图片底线", Scope: "global",
		MinEvidence: minEvidence, RequireDuration: true, RequireNoPendingAddon: true,
		RequireNoOpenAftersale: true}, 11)
	if err != nil {
		t.Fatalf("创建结单规则失败：%v", err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, time.Now()); err != nil {
		t.Fatalf("生效结单规则失败：%v", err)
	}
}

// 审核报告 ：规则要求 2 张图片时，代提交只给自由文本必须被拒；给足 2 张图片 ID 才通过。
func TestCloseSubstituteRequiresRuleImageEvidence(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_substitute_images")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveCloseRule(t, db, "CLOSE-IMG-2", 2)
	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-IMG-SUB", Name: "代提交带图",
		Scope: "global", Mode: "multi", AllowSubstitute: true, OnMissing: "block"})
	order := seedSubmitOrderWithEvidence(t, db, "sr-img-sub", []string{"[201,202]", ""}, 11)

	// 文本凭证不满足「至少 2 张图片」。
	_, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "代提交只给文本",
		ViewedEvidenceAttachmentIDs: []int{201, 202},
		SubstituteReports:           []closeSubstituteReport{{SeatIndex: 2, ReportedUnits: 1, EvidenceRef: "admin-shot"}}},
		now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("代提交只给文本应被拒：%v", err)
	}
	if !strings.Contains(err.Error(), "还差 2 张") {
		t.Fatalf("提示应说明还差几张图片：%v", err)
	}
	// 给足 2 张图片 ID 后可结单。
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "代提交带图",
		ViewedEvidenceAttachmentIDs: []int{201, 202},
		SubstituteReports: []closeSubstituteReport{{SeatIndex: 2, ReportedUnits: 1,
			EvidenceRef: "[301,302]"}}}, now); err != nil {
		t.Fatalf("代提交带足图片应结单成功：%v", err)
	}
}

// 审核报告 ：自由文本把「凭证张数」凑够后，「视为已提交」也不能放行缺图片的订单。
func TestCloseAutoAcceptRequiresRuleImageEvidence(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_auto_accept_images")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveCloseRule(t, db, "CLOSE-IMG-2B", 2)
	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-IMG-AUTO", Name: "视为已提交带图",
		Scope: "global", Mode: "multi", OnMissing: "auto_accept"})
	// 第 2 席用文本凭证凑够结单条件的「凭证张数」（1 图 + 1 文本 = 2），但图片只有 1 张。
	order := seedSubmitOrderWithEvidence(t, db, "sr-img-auto", []string{"[201]", "shot", ""}, 11)

	_, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "文本凑数",
		ViewedEvidenceAttachmentIDs: []int{201}}, now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("图片不足时「视为已提交」应被拒：%v", err)
	}
	if !strings.Contains(err.Error(), "还差 1 张") {
		t.Fatalf("提示应说明还差几张图片：%v", err)
	}
	// 第 2 席换成图片凭证后图片达 2 张，放行。
	db.EsportsServiceSegment.Update().Where(
		esportsservicesegment.OrderIDEQ(order.ID), esportsservicesegment.SeatIndexEQ(2),
	).SetEvidenceRef("[202]").SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "图片达标",
		ViewedEvidenceAttachmentIDs: []int{201, 202}}, now); err != nil {
		t.Fatalf("图片达标后应可结单：%v", err)
	}
}

// P1 回归：mode=single 不能把多人单放宽成「任一人提交即可」（功能列表 ）。
func TestCloseSingleModeRequiresEverySeat(t *testing.T) {
	db := openCloseFormDB(t, "esports_close_single_mode")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveSubmitRule(t, db, closeSubmitRuleInput{Code: "SR-VER-SINGLE", Name: "单人提交",
		Scope: "global", Mode: "single", AllowSubstitute: true, OnMissing: "block"})
	order := seedCloseReadyOrderWithVersionEvent(t, db, "sr-ver-single", 2000, 11, []bool{true, false})

	_, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("mode=single 下多席订单只上报 1 人也应被拒：%v", err)
	}
	if !strings.Contains(err.Error(), "共 2") {
		t.Fatalf("提示应说明需逐人提交（共 2 人）：%v", err)
	}
	// 补齐第 2 席后逐人达标，方可结单。
	db.EsportsServiceSegment.Update().Where(
		esportsservicesegment.OrderIDEQ(order.ID), esportsservicesegment.SeatIndexEQ(2),
	).SetReportedUnits(1).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("逐人上报达标后应可结单：%v", err)
	}
}
