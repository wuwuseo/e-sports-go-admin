package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	esportscsentry "go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreview"
	"go-admin/internal/ent/esportsplayerreviewappeal"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/global"
)

// ── 用户侧读取端点补齐（本轮 w4）：我的评价列表、我的改期提案列表、客服提成结算单明细、手动调级覆盖期 ──

// w4UserReadDB 是本轮用例的独立内存库（每个用例唯一 DSN，避免共享内存库互相污染）。
func w4UserReadDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return openDispatchPriorityDB(t, name)
}

// w4VerifiedCustomer 建一个已绑定登录身份的客户（active 用户 + 客户 + active 身份绑定）。
func w4VerifiedCustomer(t *testing.T, db *ent.Client, uid string) (*ent.User, *ent.EsportsCustomer) {
	t.Helper()
	ctx := t.Context()
	account := db.User.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").SaveX(ctx)
	customer := db.EsportsCustomer.Create().SetUserID(account.ID).SetDisplayName("测试客户").
		SetAgeStatus("adult").SetSelfOrderEnabled(true).SaveX(ctx)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").
		SetSubjectScope("").SetExternalSubject(uid).SetVerificationMethod("verified_account_link").
		SetVerifiedAt(time.Now().UTC()).SaveX(ctx)
	return account, customer
}

// w4VerifiedPlayer 建一个已绑定登录身份的陪玩档案。
func w4VerifiedPlayer(t *testing.T, db *ent.Client, uid string,
	qualification esportsplayer.Qualification, presence esportsplayer.Presence) (*ent.User, *ent.EsportsPlayer) {
	t.Helper()
	account := db.User.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").SaveX(t.Context())
	player := seedPlayer(t, db, account.ID, qualification, presence)
	return account, player
}

// w4SeedReview 直接落一条评价（verified 为 true 表示客户本人提交、计入综合评分）。
func w4SeedReview(t *testing.T, db *ent.Client, orderID, customerID, playerID, rating int, comment string) *ent.EsportsPlayerReview {
	t.Helper()
	return db.EsportsPlayerReview.Create().SetOrderID(orderID).SetCustomerID(customerID).
		SetPlayerID(playerID).SetRating(rating).SetComment(comment).SetVerified(true).
		SetStatus(esportsplayerreview.StatusNormal).SaveX(t.Context())
}

// w4SeedReviewAppeal 直接落一条申诉流水（status 取 pending / approved / rejected）。
func w4SeedReviewAppeal(t *testing.T, db *ent.Client, review *ent.EsportsPlayerReview,
	status esportsplayerreviewappeal.Status) *ent.EsportsPlayerReviewAppeal {
	t.Helper()
	return db.EsportsPlayerReviewAppeal.Create().SetReviewID(review.ID).
		SetAppellantType(esportsplayerreviewappeal.AppellantTypeCustomer).
		SetAppellantID(review.CustomerID).SetOrderID(review.OrderID).
		SetReason("对刷评判定有异议").SetStatus(status).SaveX(t.Context())
}

// getUserJSON 构造带登录态（Locals.uid）的最小 Fiber 应用并调用一次 GET handler，
// 返回状态码与解析后的响应体；运行环境与 PermissionMiddleware 之后的 handler 一致。
func getUserJSON(t *testing.T, uid, route, path string, handler fiber.Handler) (int, map[string]any) {
	t.Helper()
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", uid)
		return c.Next()
	})
	app.Get(route, handler)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("请求 %s 失败：%v", path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	return resp.StatusCode, decoded
}

// 用户侧评价列表只返回本人评价，并给出 appeal_status / verified 等申诉 UI 必需字段；
// 跨客户不可见（含按订单过滤时越权返回 403）。
func TestUserReviewsListScopedToCustomer(t *testing.T) {
	db := w4UserReadDB(t, "w4_user_reviews_list")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	accountA, customerA := w4VerifiedCustomer(t, db, "w4-review-a")
	_, customerB := w4VerifiedCustomer(t, db, "w4-review-b")
	player := seedPlayer(t, db, 88101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	orderA := seedRescheduleOrder(t, db, "w4-review-order-a", customerA.ID, now.Add(24*time.Hour), 60)
	orderB := seedRescheduleOrder(t, db, "w4-review-order-b", customerB.ID, now.Add(24*time.Hour), 60)

	plain := w4SeedReview(t, db, orderA.ID, customerA.ID, player.ID, 5, "服务很好")
	pendingReview := w4SeedReview(t, db, orderA.ID, customerA.ID, player.ID+1000, 2, "一般")
	w4SeedReviewAppeal(t, db, pendingReview, esportsplayerreviewappeal.StatusPending)
	approvedReview := w4SeedReview(t, db, orderA.ID, customerA.ID, player.ID+2000, 1, "不满意")
	w4SeedReviewAppeal(t, db, approvedReview, esportsplayerreviewappeal.StatusApproved)
	rejectedReview := w4SeedReview(t, db, orderA.ID, customerA.ID, player.ID+3000, 3, "中评")
	w4SeedReviewAppeal(t, db, rejectedReview, esportsplayerreviewappeal.StatusRejected)
	otherCustomerReview := w4SeedReview(t, db, orderB.ID, customerB.ID, player.ID, 1, "别人的评价")

	views, err := listCustomerReviews(ctx, db, customerA.ID, 0, 50, now)
	if err != nil {
		t.Fatalf("读取本人评价失败：%v", err)
	}
	if len(views) != 4 {
		t.Fatalf("应只返回本人 4 条评价，got=%d", len(views))
	}
	byID := map[int]userReviewView{}
	for _, view := range views {
		byID[view.ReviewID] = view
		if view.ReviewID == otherCustomerReview.ID {
			t.Fatal("跨客户评价泄露：不应返回他人评价")
		}
		if view.OrderNo != orderA.OrderNo {
			t.Fatalf("订单号应回填本人订单号，got=%q", view.OrderNo)
		}
	}
	if got := byID[plain.ID]; got.AppealStatus != userReviewAppealNone || !got.Verified ||
		got.Rating != 5 || got.Content != "服务很好" || !got.CanAppeal || got.CreatedAt.IsZero() {
		t.Fatalf("未申诉评价字段不正确：%+v", got)
	}
	if got := byID[pendingReview.ID]; got.AppealStatus != userReviewAppealPending || got.CanAppeal {
		t.Fatalf("申诉待复核的评价应为 pending 且不可再申诉：%+v", got)
	}
	if got := byID[approvedReview.ID]; got.AppealStatus != userReviewAppealApproved || got.CanAppeal {
		t.Fatalf("申诉成立的评价应为 approved 且不可再申诉：%+v", got)
	}
	if got := byID[rejectedReview.ID]; got.AppealStatus != userReviewAppealRejected || got.CanAppeal {
		t.Fatalf("申诉驳回的评价应为 rejected 且不可再申诉：%+v", got)
	}

	// 另一个客户读自己的评价：只能看到自己那一条。
	otherViews, err := listCustomerReviews(ctx, db, customerB.ID, 0, 50, now)
	if err != nil {
		t.Fatalf("读取他人本人评价失败：%v", err)
	}
	if len(otherViews) != 1 || otherViews[0].ReviewID != otherCustomerReview.ID {
		t.Fatalf("客户 B 只应看到自己的评价：%+v", otherViews)
	}

	// 按订单过滤：本人订单可过滤，他人订单返回 403（不泄露订单是否存在）。
	filtered, err := listCustomerReviews(ctx, db, customerA.ID, orderA.ID, 50, now)
	if err != nil || len(filtered) != 4 {
		t.Fatalf("按本人订单过滤失败：len=%d err=%v", len(filtered), err)
	}
	if _, err := listCustomerReviews(ctx, db, customerA.ID, orderB.ID, 50, now); !errors.Is(err, errForbidden) {
		t.Fatalf("按他人订单过滤应返回 403：%v", err)
	}

	// handler 维度：登录身份解析出客户，响应与 service 同口径。
	status, body := getUserJSON(t, accountA.UID, "/user/reviews", "/user/reviews", userReviewsHandler)
	if status != http.StatusOK {
		t.Fatalf("我的评价列表应返回 200，got=%d body=%+v", status, body)
	}
	items, _ := body["data"].([]any)
	if len(items) != 4 {
		t.Fatalf("handler 应返回 4 条本人评价，got=%d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["review_id"] == nil || first["appeal_status"] == nil || first["order_no"] == nil {
		t.Fatalf("响应应含 review_id / appeal_status / order_no：%+v", first)
	}
}

// 用户侧改期提案读取：客户与陪玩各只看到本人相关提案，进度摘要与本人表态正确，跨客户不可见。
func TestUserRescheduleProposalsScopedToCustomerAndPlayer(t *testing.T) {
	db := w4UserReadDB(t, "w4_user_reschedule_list")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	accountA, customerA := w4VerifiedCustomer(t, db, "w4-resched-a")
	_, customerB := w4VerifiedCustomer(t, db, "w4-resched-b")
	playerAccount, memberA := w4VerifiedPlayer(t, db, "w4-resched-player-a",
		esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	memberB := seedPlayer(t, db, 88202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 88203, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	base := now.Add(48 * time.Hour).Truncate(time.Hour)
	orderA := seedRescheduleOrder(t, db, "w4-resched-order-a", customerA.ID, base, 60)
	seedRescheduleSeat(t, db, orderA.ID, 1, memberA)
	seedRescheduleSeat(t, db, orderA.ID, 2, memberB)
	target := base.Add(3 * time.Hour)
	proposal, err := proposeReschedule(ctx, db, orderA.ID, 11, rescheduleProposalInput{
		ExpectedVersion: orderA.Version, StartAt: target, Reason: "客户要求改到当晚",
	}, now)
	if err != nil {
		t.Fatalf("发起改期提案失败：%v", err)
	}

	// 另一客户的订单也有一张提案：不得出现在客户 A / 陪玩 A 的列表里。
	orderB := seedRescheduleOrder(t, db, "w4-resched-order-b", customerB.ID, base, 60)
	otherProposal, err := proposeReschedule(ctx, db, orderB.ID, 11, rescheduleProposalInput{
		ExpectedVersion: orderB.Version, StartAt: target, Reason: "别人的改期",
	}, now)
	if err != nil {
		t.Fatalf("为他人订单发起提案失败：%v", err)
	}

	customerViews, err := listUserRescheduleProposals(ctx, db,
		rescheduleProposalScope{CustomerID: customerA.ID}, 0, 20)
	if err != nil {
		t.Fatalf("客户读取改期提案失败：%v", err)
	}
	if len(customerViews) != 1 || customerViews[0].ProposalID != proposal.ID {
		t.Fatalf("客户应只看到本人订单的 1 张提案：%+v", customerViews)
	}
	view := customerViews[0]
	if view.OrderNo != orderA.OrderNo || view.Status != "pending" ||
		view.FromStartAt == nil || !view.FromStartAt.Equal(base) ||
		!view.ToStartAt.Equal(target) || view.ExpiresAt.IsZero() ||
		view.MyDecision != "pending" {
		t.Fatalf("客户侧提案字段不正确：%+v", view)
	}
	if view.Progress.RequiredCount != 3 || view.Progress.ConfirmedCount != 0 ||
		!view.Progress.CustomerRequired || view.Progress.CustomerConfirmed ||
		view.Progress.PlayerRequiredCount != 2 || len(view.Progress.PendingConfirmers) != 3 {
		t.Fatalf("客户侧确认进度摘要不正确：%+v", view.Progress)
	}
	for _, item := range customerViews {
		if item.ProposalID == otherProposal.ID {
			t.Fatal("跨客户提案泄露：不应返回他人订单的提案")
		}
	}

	// 陪玩侧：只有持席位的成员能看到；无席位陪玩看不到任何提案。
	playerViews, err := listUserRescheduleProposals(ctx, db,
		rescheduleProposalScope{PlayerID: memberA.ID}, 0, 20)
	if err != nil {
		t.Fatalf("陪玩读取改期提案失败：%v", err)
	}
	if len(playerViews) != 1 || playerViews[0].ProposalID != proposal.ID {
		t.Fatalf("陪玩应只看到本单 1 张提案：%+v", playerViews)
	}
	if playerViews[0].MyDecision != "pending" || playerViews[0].MySeatIndex != 1 {
		t.Fatalf("陪玩未表态时应为 pending 并带席位序号：%+v", playerViews[0])
	}
	outsiderViews, err := listUserRescheduleProposals(ctx, db,
		rescheduleProposalScope{PlayerID: outsider.ID}, 0, 20)
	if err != nil {
		t.Fatalf("无关陪玩读取改期提案失败：%v", err)
	}
	if len(outsiderViews) != 0 {
		t.Fatalf("无席位的陪玩不应看到任何提案：%+v", outsiderViews)
	}

	// 成员 A 确认后：本人表态与进度摘要同步，其余确认人仍在待表态清单里。
	if _, err = confirmRescheduleProposal(ctx, db, proposal.ID,
		reschedulePlayerConfirmer(memberA.ID), true, "可以", now); err != nil {
		t.Fatalf("成员确认失败：%v", err)
	}
	playerViews, err = listUserRescheduleProposals(ctx, db,
		rescheduleProposalScope{PlayerID: memberA.ID}, 0, 20)
	if err != nil {
		t.Fatalf("再次读取陪玩提案失败：%v", err)
	}
	after := playerViews[0]
	if after.MyDecision != "confirmed" || after.Progress.PlayerConfirmedCount != 1 ||
		after.Progress.ConfirmedCount != 1 || len(after.Progress.PendingConfirmers) != 2 ||
		after.Progress.Complete {
		t.Fatalf("确认后进度摘要不正确：%+v", after)
	}

	// 按订单过滤：本人订单可见，他人订单 403。
	if items, err := listUserRescheduleProposals(ctx, db,
		rescheduleProposalScope{CustomerID: customerA.ID}, orderA.ID, 20); err != nil || len(items) != 1 {
		t.Fatalf("按本人订单过滤失败：len=%d err=%v", len(items), err)
	}
	if _, err := listUserRescheduleProposals(ctx, db,
		rescheduleProposalScope{CustomerID: customerA.ID}, orderB.ID, 20); !errors.Is(err, errForbidden) {
		t.Fatalf("按他人订单过滤应返回 403：%v", err)
	}

	// handler 维度：客户 / 陪玩两条路由各自解析登录身份。
	status, body := getUserJSON(t, accountA.UID, "/user/reschedule-proposals",
		"/user/reschedule-proposals", customerRescheduleProposalsHandler)
	if status != http.StatusOK {
		t.Fatalf("客户侧提案列表应返回 200，got=%d body=%+v", status, body)
	}
	if items, _ := body["data"].([]any); len(items) != 1 {
		t.Fatalf("客户 handler 应返回 1 条提案，got=%d", len(items))
	}
	status, body = getUserJSON(t, playerAccount.UID, "/user/player/reschedule-proposals",
		"/user/player/reschedule-proposals", playerRescheduleProposalsHandler)
	if status != http.StatusOK {
		t.Fatalf("陪玩侧提案列表应返回 200，got=%d body=%+v", status, body)
	}
	items, _ := body["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("陪玩 handler 应返回 1 条提案，got=%d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["proposal_id"] == nil || first["order_no"] == nil || first["expires_at"] == nil {
		t.Fatalf("响应应含 proposal_id / order_no / expires_at：%+v", first)
	}
	if progress, _ := first["progress"].(map[string]any); progress["pending_confirmers"] == nil {
		t.Fatalf("响应应含逐人确认进度摘要：%+v", first)
	}
}

// 客服提成结算单详情必须返回它自己的明细（修复前只查陪玩分成明细，entries 恒空）；
// 金额与结算单汇总一致，来源单号回填，陪玩结算单仍走原有明细口径。
func TestSettlementDetailReturnsCsEntries(t *testing.T) {
	db := w4UserReadDB(t, "w4_settlement_cs_entries")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	admin := db.Admin.Create().SetEmail("w4-cs-settle@example.com").SetPassword("test").
		SetUID("w4-cs-settle").SetUsername("w4-cs-settle").SaveX(ctx)
	_, customer := w4VerifiedCustomer(t, db, "w4-settle-customer")
	order := seedRescheduleOrder(t, db, "w4-settle-order", customer.ID, now.Add(24*time.Hour), 60)

	db.EsportsCsCommissionEntry.Create().SetIdemKey("w4-cs-settle-service").SetAdminID(admin.ID).
		SetOrderID(order.ID).SetKind(esportscsentry.KindService).
		SetRuleNo("CS-RULE-W4").SetRuleVersion(3).
		SetBaseCents(10000).SetAmountCents(1000).
		SetStatus(esportscsentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsCsCommissionEntry.Create().SetIdemKey("w4-cs-settle-reversal").SetAdminID(admin.ID).
		SetOrderID(order.ID).SetKind(esportscsentry.KindReversal).
		SetBaseCents(10000).SetAmountCents(-200).
		SetStatus(esportscsentry.StatusPendingSettlement).SaveX(ctx)

	settlement, err := settleCsCommission(ctx, db, admin.ID, now.Add(-time.Hour), now.Add(time.Hour), 0, now)
	if err != nil {
		t.Fatalf("生成客服提成结算单失败：%v", err)
	}
	if settlement.OwnerType != esportssettlement.OwnerTypeCs || settlement.EntryCount != 2 ||
		settlement.NetCents != 800 {
		t.Fatalf("客服提成结算单汇总不正确：%+v", settlement)
	}

	detail, err := getSettlementDetail(ctx, db, settlement.ID, 200)
	if err != nil {
		t.Fatalf("读取客服提成结算单详情失败：%v", err)
	}
	if len(detail.Entries) != 2 || detail.EntryCount != 2 || detail.DetailTruncated {
		t.Fatalf("客服提成结算单详情应返回 2 条明细：len=%d count=%d truncated=%v",
			len(detail.Entries), detail.EntryCount, detail.DetailTruncated)
	}
	first, ok := detail.Entries[0].(csSettlementEntryView)
	if !ok {
		t.Fatalf("客服提成明细应使用 csSettlementEntryView：%T", detail.Entries[0])
	}
	if first.OrderNo != order.OrderNo || first.AdminID != admin.ID || first.Kind != "service" ||
		first.AmountCents != 1000 || first.NetCents != 1000 || first.RuleNo != "CS-RULE-W4" ||
		first.RuleVersion != 3 || first.SettlementID == nil || *first.SettlementID != settlement.ID ||
		first.CreatedAt.IsZero() {
		t.Fatalf("客服提成明细字段不正确：%+v", first)
	}
	var sum int64
	for _, item := range detail.Entries {
		entry, ok := item.(csSettlementEntryView)
		if !ok {
			t.Fatalf("明细类型不一致：%T", item)
		}
		sum += entry.AmountCents
	}
	if sum != settlement.GrossCents+settlement.AdjustmentCents {
		t.Fatalf("明细金额合计与结算单汇总不一致：sum=%d gross=%d adjustment=%d",
			sum, settlement.GrossCents, settlement.AdjustmentCents)
	}

	// 明细超上限时给出截断标记，而不是静默漏数。
	trimmed, err := getSettlementDetail(ctx, db, settlement.ID, 1)
	if err != nil {
		t.Fatalf("截断读取失败：%v", err)
	}
	if len(trimmed.Entries) != 1 || !trimmed.DetailTruncated {
		t.Fatalf("超过上限应截断并标记：len=%d truncated=%v", len(trimmed.Entries), trimmed.DetailTruncated)
	}

	// 陪玩结算单仍走原有明细口径（避免分流后回归）。
	playerSettlement := db.EsportsSettlement.Create().SetSettlementNo("ST-W4-PLAYER").
		SetOwnerType(esportssettlement.OwnerTypePlayer).SetOwnerID(901).
		SetPeriodStart(now.Add(-time.Hour)).SetPeriodEnd(now.Add(time.Hour)).
		SetGrossCents(500).SetNetCents(500).SetEntryCount(1).
		SetStatus(esportssettlement.StatusDraft).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("w4-player-settle-service").SetOrderID(order.ID).
		SetPlayerID(901).SetKind(esportscommissionentry.KindService).
		SetBaseCents(5000).SetAmountCents(500).
		SetStatus(esportscommissionentry.StatusPendingSettlement).
		SetSettlementID(playerSettlement.ID).SaveX(ctx)
	playerDetail, err := getSettlementDetail(ctx, db, playerSettlement.ID, 200)
	if err != nil {
		t.Fatalf("读取陪玩结算单详情失败：%v", err)
	}
	if len(playerDetail.Entries) != 1 {
		t.Fatalf("陪玩结算单应返回 1 条明细，got=%d", len(playerDetail.Entries))
	}
	if entry, ok := playerDetail.Entries[0].(commissionEntryView); !ok || entry.AmountCents != 500 ||
		entry.NetCents != 500 {
		t.Fatalf("陪玩结算单明细口径不正确：%T %+v", playerDetail.Entries[0], playerDetail.Entries[0])
	}
}

// 手动调级的覆盖期入参：override_days / override_until 二选一且越界报错；
// 指定后覆盖到期时刻按它生效（覆盖记录 created_at 仍是真实调级时刻），到期回落成长值口径。
func TestSetMemberLevelOverrideWindow(t *testing.T) {
	db := openMemberDB(t, "w4_member_override_window")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	seedMemberLevel(t, db, memberLevelInput{Code: "w4w-silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000})
	seedMemberLevel(t, db, memberLevelInput{Code: "w4w-gold", Name: "金卡", GrowthThreshold: 10000, DiscountBps: 9000})
	customer := saveCustomerOrFail(t, db, 11)

	// 二选一：同时给出被拒，文案明确。
	_, err := setMemberLevelByAdmin(ctx, db, customer.ID, "w4w-silver", "客诉协商降级", 11, now,
		memberOverrideWindow{Days: 7, Until: now.AddDate(0, 0, 3)})
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "二选一") {
		t.Fatalf("同时给出 override_days 与 override_until 应返回参数错误：%v", err)
	}
	// 越界：天数上下限。
	if _, err = setMemberLevelByAdmin(ctx, db, customer.ID, "w4w-silver", "越界天数", 11, now,
		memberOverrideWindow{Days: -1}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("override_days 负数应被拒：%v", err)
	}
	if _, err = setMemberLevelByAdmin(ctx, db, customer.ID, "w4w-silver", "越界天数", 11, now,
		memberOverrideWindow{Days: 3651}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("override_days 超过 3650 天应被拒：%v", err)
	}
	// 越界：到期时刻必须晚于当前、且不超过 3650 天。
	if _, err = setMemberLevelByAdmin(ctx, db, customer.ID, "w4w-silver", "过去时刻", 11, now,
		memberOverrideWindow{Until: now.Add(-time.Hour)}); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "override_until") {
		t.Fatalf("override_until 为过去时刻应被拒：%v", err)
	}
	if _, err = setMemberLevelByAdmin(ctx, db, customer.ID, "w4w-silver", "过远时刻", 11, now,
		memberOverrideWindow{Until: now.AddDate(0, 0, 3651)}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("override_until 超过 3650 天应被拒：%v", err)
	}
	// 校验失败不得落任何覆盖记录。
	if rows := db.EsportsMemberLevelOverride.Query().CountX(ctx); rows != 0 {
		t.Fatalf("参数校验失败不应落覆盖记录，got=%d", rows)
	}

	// 覆盖期内人工等级为权威：成长值够金卡，人工降级到银卡后重算也不反弹。
	db.EsportsGrowthLedger.Create().SetCustomerID(customer.ID).SetDelta(20000).
		SetSourceType("manual").SetSourceID("w4w-seed-1").SaveX(ctx)
	if err := recomputeCustomerLevel(ctx, db, customer.ID, now); err != nil {
		t.Fatalf("按成长值定级失败：%v", err)
	}
	if _, err := setMemberLevelByAdmin(ctx, db, customer.ID, "w4w-silver", "临时降级观察", 11, now,
		memberOverrideWindow{Days: 7}); err != nil {
		t.Fatalf("按 override_days 调级失败：%v", err)
	}
	view, err := memberViewAt(ctx, db, customer.ID, 20, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("读取会员详情失败：%v", err)
	}
	if view.LevelCode != "w4w-silver" || !view.OverrideActive || view.OverrideExpiresAt == nil ||
		!view.OverrideExpiresAt.UTC().Equal(now.AddDate(0, 0, 7)) {
		t.Fatalf("override_days=7 的覆盖到期时刻不正确：%+v", view)
	}
	// 覆盖记录仍记录真实调级时刻（不得篡改为「到期时间 - 天数」）。
	if override := w4LastOverride(t, db, customer.ID); !override.CreatedAt.UTC().Truncate(time.Second).Equal(now) {
		t.Fatalf("覆盖记录 created_at 应为真实调级时刻 %s，got=%s", now, override.CreatedAt.UTC())
	}
	// 到期后回落成长值口径（金卡）。
	if count, err := applyMemberOverrideExpiries(ctx, db, now.AddDate(0, 0, 8), 100); err != nil || count != 1 {
		t.Fatalf("覆盖到期应回落 1 个客户：count=%d err=%v", count, err)
	}
	if got := db.EsportsCustomer.GetX(ctx, customer.ID); got.MemberLevelCode != "w4w-gold" {
		t.Fatalf("覆盖到期未回落到成长值口径：%s", got.MemberLevelCode)
	}
	if got, err := memberViewAt(ctx, db, customer.ID, 20, now.AddDate(0, 0, 8)); err != nil || got.OverrideActive {
		t.Fatalf("到期后 override_active 应为 false：%+v err=%v", got, err)
	}

	// override_until：直接指定到期时刻，覆盖期可短于也不受系统配置限制。
	setAt := now.AddDate(0, 0, 8)
	until := setAt.AddDate(0, 0, 3)
	if _, err = setMemberLevelByAdmin(ctx, db, customer.ID, "w4w-silver", "指定到期时刻", 11,
		setAt, memberOverrideWindow{Until: until}); err != nil {
		t.Fatalf("按 override_until 调级失败：%v", err)
	}
	view, err = memberViewAt(ctx, db, customer.ID, 20, setAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("读取会员详情失败：%v", err)
	}
	if !view.OverrideActive || view.OverrideExpiresAt == nil ||
		!view.OverrideExpiresAt.UTC().Equal(until) {
		t.Fatalf("override_until 的覆盖到期时刻不正确：%+v", view)
	}
}

// w4LastOverride 测试辅助：读取客户最近一次手动调级覆盖记录（无记录时 Fatal）。
func w4LastOverride(t *testing.T, db *ent.Client, customerID int) *ent.EsportsMemberLevelOverride {
	t.Helper()
	override, err := lastMemberLevelOverride(t.Context(), db, customerID)
	if err != nil || override == nil {
		t.Fatalf("未找到覆盖记录：%+v err=%v", override, err)
	}
	return override
}

// 三条用户端路由必须按仓库惯例以 AuthUser 注册、且指向本轮新增的 handler；
// 用户端自助路由不需要（也不应声明）PermAPI 以外的管理端权限。
func TestUserReadRoutesDeclared(t *testing.T) {
	app := esportsAppDeclaration(t)

	want := map[string]fiber.Handler{
		"/user/reviews":                     userReviewsHandler,
		"/user/reschedule-proposals":        customerRescheduleProposalsHandler,
		"/user/player/reschedule-proposals": playerRescheduleProposalsHandler,
	}
	for path, handler := range want {
		var route *cmfapp.Route
		for i := range app.Routes {
			if app.Routes[i].Method == http.MethodGet && app.Routes[i].Path == path {
				route = &app.Routes[i]
			}
		}
		if route == nil {
			t.Fatalf("缺少路由 GET %s", path)
		}
		if route.Auth != cmfapp.AuthUser {
			t.Fatalf("用户端自助路由应为 AuthUser：%s got=%v", path, route.Auth)
		}
		if route.Handler == nil || reflect.ValueOf(route.Handler).Pointer() != reflect.ValueOf(handler).Pointer() {
			t.Fatalf("路由 %s 未指向预期 handler", path)
		}
	}
}
