package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsrescheduleconfirmation"
	"go-admin/internal/ent/esportsrescheduleproposal"
)

// §3.3 限时改期提案闭环：提案 → 客户与已接单成员逐人确认 → 目标档期原子占位 / 超时收敛。

// seedRescheduleOrder 建一张已付款、已确认档期、绑定指定客户的订单（提案前置态）。
func seedRescheduleOrder(t *testing.T, db *ent.Client, orderNo string, customerID int,
	start time.Time, durationMinutes int) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(start.Add(-time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingAcceptance).
		SetRequestedStartAt(start.UTC()).
		SetServiceDurationMinutes(durationMinutes).
		SetAppointmentStatus(esportsorder.AppointmentStatusConfirmed).
		SaveX(t.Context())
}

// seedRescheduleSeat 为指定陪玩在指定订单上直接建一条 accepted 席位（跳过邀请流程）。
func seedRescheduleSeat(t *testing.T, db *ent.Client, orderID, seatIndex int,
	player *ent.EsportsPlayer) *ent.EsportsDispatchSeat {
	t.Helper()
	return db.EsportsDispatchSeat.Create().SetOrderID(orderID).SetSeatIndex(seatIndex).
		SetPlayerID(player.ID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(t.Context())
}

// seedAcceptedRescheduleMember 建一名合格在线陪玩并让其接单，返回该陪玩。
func seedAcceptedRescheduleMember(t *testing.T, db *ent.Client, orderID, seatIndex,
	userID int) *ent.EsportsPlayer {
	t.Helper()
	player := seedPlayer(t, db, userID, esportsplayer.QualificationQualified,
		esportsplayer.PresenceOnline)
	seedRescheduleSeat(t, db, orderID, seatIndex, player)
	return player
}

func rescheduleCustomerConfirmer(customerID int) rescheduleConfirmer {
	return rescheduleConfirmer{
		Type: esportsrescheduleconfirmation.ConfirmerTypeCustomer, ID: customerID}
}

func reschedulePlayerConfirmer(playerID int) rescheduleConfirmer {
	return rescheduleConfirmer{
		Type: esportsrescheduleconfirmation.ConfirmerTypePlayer, ID: playerID}
}

// rescheduleAssertOrderStart 断言订单档期，并返回最新订单。
func rescheduleAssertOrderStart(t *testing.T, db *ent.Client, orderID int,
	want time.Time, scene string) *ent.EsportsOrder {
	t.Helper()
	order := db.EsportsOrder.GetX(t.Context(), orderID)
	if order.RequestedStartAt == nil || !order.RequestedStartAt.Equal(want.UTC()) {
		t.Fatalf("%s：订单档期应为 %s，实为 %v", scene, want.UTC(), order.RequestedStartAt)
	}
	return order
}

// 提案需客户与全部已接单成员逐人确认，缺任一确认不生效；齐备后在事务内原子切换档期并占用资源。
func TestRescheduleProposalRequiresAllConfirmations(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_reschedule_all_confirm")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	customer := saveCustomerOrFail(t, db, 11)
	base := now.Add(48 * time.Hour).Truncate(time.Hour)
	order := seedRescheduleOrder(t, db, "rp-all-1", customer.ID, base, 60)
	memberA := seedAcceptedRescheduleMember(t, db, order.ID, 1, 9201)
	memberB := seedAcceptedRescheduleMember(t, db, order.ID, 2, 9202)

	target := base.Add(3 * time.Hour)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "客户要求改到当晚",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != esportsrescheduleproposal.StatusPending || item.ActiveOrderID == nil ||
		*item.ActiveOrderID != order.ID {
		t.Fatalf("提案应落为进行中并占用唯一键：%+v", item)
	}
	if item.OrderVersion != order.Version || item.FromStartAt == nil ||
		!item.FromStartAt.Equal(base) || item.FromServiceDurationMinutes != 60 ||
		item.ToServiceDurationMinutes != 60 {
		t.Fatalf("提案未按发起时刻快照原 / 目标档期：%+v", item)
	}
	// 提案时限取配置（缺省 24 小时）且不晚于目标开始时间。
	if !item.ExpiresAt.After(now) || item.ExpiresAt.After(target) {
		t.Fatalf("提案时限不合法：%s（now=%s target=%s）", item.ExpiresAt, now, target)
	}
	// 提案本身不改订单。
	rescheduleAssertOrderStart(t, db, order.ID, base, "提案阶段")

	// 1) 只有客户确认：成员未确认 → 不生效。
	got, err := confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(customer.ID),
		true, "同意", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != esportsrescheduleproposal.StatusPending {
		t.Fatalf("缺成员确认时不应生效：%s", got.Status)
	}
	rescheduleAssertOrderStart(t, db, order.ID, base, "仅客户确认")

	// 2) 成员 A 确认：仍缺成员 B 的确认 → 不生效。
	got, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(memberA.ID),
		true, "可以", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != esportsrescheduleproposal.StatusPending {
		t.Fatalf("缺任一成员确认时不应生效：%s", got.Status)
	}
	rescheduleAssertOrderStart(t, db, order.ID, base, "缺成员 B 确认")

	// 同一人重复确认幂等：不新增明细。
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(memberA.ID),
		true, "可以", now); err != nil {
		t.Fatalf("重复确认应幂等：%v", err)
	}
	if rows := db.EsportsRescheduleConfirmation.Query().CountX(ctx); rows != 2 {
		t.Fatalf("同一人重复确认不应新增明细，现有 %d 条", rows)
	}

	// 3) 全部确认齐备 → 档期原子切换。
	got, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(memberB.ID),
		true, "可以", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != esportsrescheduleproposal.StatusConfirmed || got.ActiveOrderID != nil ||
		got.DecidedAt == nil {
		t.Fatalf("确认齐备后提案应终结为 confirmed 并让出唯一键：%+v", got)
	}
	updated := rescheduleAssertOrderStart(t, db, order.ID, target, "生效后")
	if updated.ServiceDurationMinutes != 60 ||
		updated.AppointmentStatus != esportsorder.AppointmentStatusConfirmed ||
		updated.AppointmentConfirmationDeadline != nil ||
		updated.Version != order.Version+1 {
		t.Fatalf("生效后订单档期 / 状态未按目标档期更新：%+v", updated)
	}
	// 生效过程写订单事件与事务外盒，便于审计。
	if counts := countEventsByType(t, db); counts["order.reschedule_proposal_applied"] != 1 {
		t.Fatalf("未落 order.reschedule_proposal_applied 事件：%+v", counts)
	}
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.reschedule_applied")).ExistX(ctx) {
		t.Fatal("未落 order.reschedule_applied 事务外盒")
	}
	// 已终结的提案不再接受确认。
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(customer.ID),
		true, "再确认", now); !errors.Is(err, errConflict) {
		t.Fatalf("已生效提案不应接受再次确认：%v", err)
	}
}

// 任一确认人拒绝即否决提案（订单不变）；非本单已接单成员与已绑定身份客户之外的人不得确认。
func TestRescheduleProposalMemberRejectionKeepsOrder(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_reschedule_reject")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	customer := saveCustomerOrFail(t, db, 11)
	base := now.Add(48 * time.Hour).Truncate(time.Hour)
	order := seedRescheduleOrder(t, db, "rp-reject-1", customer.ID, base, 60)
	member := seedAcceptedRescheduleMember(t, db, order.ID, 1, 9211)
	outsider := seedPlayer(t, db, 9212, esportsplayer.QualificationQualified,
		esportsplayer.PresenceOnline)

	target := base.Add(2 * time.Hour)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "客户申请改期",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(outsider.ID),
		true, "", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本单已接单成员不应可确认：%v", err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(outsider.ID),
		true, "", now); !errors.Is(err, errForbidden) {
		t.Fatalf("陪玩 ID 冒充客户确认应被拒：%v", err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(customer.ID),
		true, "同意", now); err != nil {
		t.Fatal(err)
	}
	// 成员拒绝 → 提案终结为 rejected，订单档期保持原样。
	rejected, err := confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(member.ID),
		false, "该时段已有安排", now)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != esportsrescheduleproposal.StatusRejected {
		t.Fatalf("成员拒绝后提案应为 rejected：%s", rejected.Status)
	}
	rescheduleAssertOrderStart(t, db, order.ID, base, "成员拒绝后")
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.reschedule_rejected")).ExistX(ctx) {
		t.Fatal("未落 order.reschedule_rejected 事务外盒")
	}
	// 已表态的人不能反转结论，须撤回后重新提案。
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(member.ID),
		true, "改主意了", now); !errors.Is(err, errConflict) {
		t.Fatalf("已终结提案不应可改结论：%v", err)
	}
}

// 提案超时：超时后不可确认，由定时任务转 expired，重复跑任务幂等且不改订单。
func TestRescheduleProposalExpires(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_reschedule_expire")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	db.SystemConfig.Create().SetKey(rescheduleProposalHoursKey).SetName("改期提案确认时限").
		SetValue("2").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	base := now.Add(48 * time.Hour).Truncate(time.Hour)
	order := seedRescheduleOrder(t, db, "rp-expire-1", customer.ID, base, 60)
	member := seedAcceptedRescheduleMember(t, db, order.ID, 1, 9221)

	target := base.Add(time.Hour)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "客户申请改期",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(2 * time.Hour); !item.ExpiresAt.Equal(want) {
		t.Fatalf("提案时限应取配置的 2 小时：%s，want %s", item.ExpiresAt, want)
	}
	// 时限不得超过目标开始时间。
	soonStart := now.Add(30 * time.Minute)
	db.EsportsRescheduleProposal.UpdateOneID(item.ID).SetToStartAt(soonStart).SaveX(ctx)
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(customer.ID),
		true, "", now); err != nil {
		t.Fatal(err)
	}
	// 强制把时限推到过去，模拟超时。
	db.EsportsRescheduleProposal.UpdateOneID(item.ID).
		SetExpiresAt(now.Add(-time.Minute)).SaveX(ctx)
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(member.ID),
		true, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("超时提案不应接受确认：%v", err)
	}
	count, err := expireRescheduleProposals(ctx, db, now, 100)
	if err != nil || count != 1 {
		t.Fatalf("超时任务应过期 1 张提案：count=%d err=%v", count, err)
	}
	expired := db.EsportsRescheduleProposal.GetX(ctx, item.ID)
	if expired.Status != esportsrescheduleproposal.StatusExpired || expired.ActiveOrderID != nil ||
		expired.DecidedAt == nil {
		t.Fatalf("超时提案未正确终结：%+v", expired)
	}
	rescheduleAssertOrderStart(t, db, order.ID, base, "提案超时后")
	if again, err := expireRescheduleProposals(ctx, db, now, 100); err != nil || again != 0 {
		t.Fatalf("重复执行超时任务应幂等：count=%d err=%v", again, err)
	}
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.reschedule_expired")).ExistX(ctx) {
		t.Fatal("未落 order.reschedule_expired 事务外盒")
	}
}

// 目标档期被该陪玩的其他占位占用时整单回滚：订单档期、提案状态与已占位资源都不落库。
func TestRescheduleProposalTargetSlotConflictRollsBack(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_reschedule_conflict")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	customer := saveCustomerOrFail(t, db, 11)
	base := now.Add(48 * time.Hour).Truncate(time.Hour)
	player := seedPlayer(t, db, 9221, esportsplayer.QualificationQualified,
		esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)

	order := seedRescheduleOrder(t, db, "rp-cf-1", customer.ID, base, 60)
	seedRescheduleSeat(t, db, order.ID, 1, player)
	// 另一张已付款订单在同一陪玩的目标档期上已占位（15 分钟后开始，仍与目标档期重叠）。
	other := seedRescheduleOrder(t, db, "rp-cf-2", customer.ID,
		base.Add(3*time.Hour+15*time.Minute), 60)
	otherSeat := seedRescheduleSeat(t, db, other.ID, 1, player)
	otherBefore := db.EsportsOrder.GetX(ctx, other.ID)

	target := base.Add(3 * time.Hour)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "客户要求改到当晚",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(customer.ID),
		true, "同意", now); err != nil {
		t.Fatal(err)
	}
	_, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(player.ID),
		true, "可以", now)
	if !errors.Is(err, errAppointmentResource) {
		t.Fatalf("目标档期冲突应返回资源冲突并说明原因：%v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "目标档期不可用") {
		t.Fatalf("资源冲突应给出明确原因：%v", err)
	}
	// 整单回滚：订单档期与版本不动。
	unchanged := rescheduleAssertOrderStart(t, db, order.ID, base, "目标档期冲突回滚")
	if unchanged.Version != order.Version ||
		unchanged.AppointmentStatus != esportsorder.AppointmentStatusConfirmed {
		t.Fatalf("回滚后订单不应被改动：%+v", unchanged)
	}
	// 提案仍是进行中、未被推进（确认明细保留，便于客服看到谁已确认）。
	pending := db.EsportsRescheduleProposal.GetX(ctx, item.ID)
	if pending.Status != esportsrescheduleproposal.StatusPending || pending.Version != 1 {
		t.Fatalf("回滚后提案应保持进行中：%+v", pending)
	}
	if rows := db.EsportsRescheduleConfirmation.Query().CountX(ctx); rows != 2 {
		t.Fatalf("已留痕的确认不应因生效失败丢失：%d", rows)
	}
	// 目标档期资源占用随事务回滚：陪玩版本与其它订单都不变。
	if db.EsportsPlayer.GetX(ctx, player.ID).Version != player.Version {
		t.Fatal("回滚后陪玩版本不应被占用")
	}
	if nowOther := db.EsportsOrder.GetX(ctx, other.ID); nowOther.Version != otherBefore.Version {
		t.Fatal("回滚后其它订单不应被改动")
	}
	if seat := db.EsportsDispatchSeat.GetX(ctx, otherSeat.ID); seat.Status != esportsdispatchseat.StatusAccepted {
		t.Fatalf("回滚后其它订单席位应保持已接单：%s", seat.Status)
	}
	if counts := countEventsByType(t, db); counts["order.reschedule_proposal_applied"] != 0 {
		t.Fatalf("回滚不应落生效事件：%+v", counts)
	}
	if db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.reschedule_applied")).ExistX(ctx) {
		t.Fatal("回滚不应落生效外盒")
	}
}

// 生效后成员的资源占用从原档期迁移到目标档期（§5.5 口径按新档期判定）。
func TestRescheduleProposalMovesMemberOccupancyToTargetWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_reschedule_occupancy")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	customer := saveCustomerOrFail(t, db, 11)
	base := now.Add(48 * time.Hour).Truncate(time.Hour)
	player := seedPlayer(t, db, 9231, esportsplayer.QualificationQualified,
		esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)

	order := seedRescheduleOrder(t, db, "rp-mv-1", customer.ID, base, 60)
	seedRescheduleSeat(t, db, order.ID, 1, player)

	target := base.Add(3 * time.Hour)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "客户要求改到当晚",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(customer.ID),
		true, "同意", now); err != nil {
		t.Fatal(err)
	}
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, reschedulePlayerConfirmer(player.ID),
		true, "可以", now); err != nil {
		t.Fatal(err)
	}
	rescheduleAssertOrderStart(t, db, order.ID, target, "生效后")

	// 原档期已释放：另一张占用原档期的订单可正常派位。
	freed := seedRescheduleOrder(t, db, "rp-mv-freed", customer.ID, base, 60)
	if _, err := assignDispatchSeat(ctx, db, freed.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("改期生效后原档期应已释放：%v", err)
	}
	// 目标档期已占用：与之重叠的新订单被并发上限拦下。
	clash := seedRescheduleOrder(t, db, "rp-mv-clash", customer.ID, target.Add(30*time.Minute), 60)
	if _, err := assignDispatchSeat(ctx, db, clash.ID, 1, player.ID, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("改期生效后目标档期应已占用：%v", err)
	}
}

// 同一订单同时只允许一张进行中提案；撤回后可重新提案；未付款订单不走提案。
func TestRescheduleProposalRejectsDuplicateAndWithdrawAllowsRetry(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_reschedule_duplicate")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	customer := saveCustomerOrFail(t, db, 11)
	base := now.Add(48 * time.Hour).Truncate(time.Hour)
	order := seedRescheduleOrder(t, db, "rp-dup-1", customer.ID, base, 60)
	seedAcceptedRescheduleMember(t, db, order.ID, 1, 9241)

	target := base.Add(3 * time.Hour)
	item, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "客户要求改到当晚",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target.Add(time.Hour), Reason: "再开一张",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("同一订单重复提案应被拒：%v", err)
	}
	// 撤回后让出唯一键，可重新提案。
	withdrawn, err := withdrawRescheduleProposal(ctx, db, item.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if withdrawn.Status != esportsrescheduleproposal.StatusWithdrawn || withdrawn.ActiveOrderID != nil {
		t.Fatalf("撤回后的提案状态不正确：%+v", withdrawn)
	}
	rescheduleAssertOrderStart(t, db, order.ID, base, "撤回后")
	if _, err = confirmRescheduleProposal(ctx, db, item.ID, rescheduleCustomerConfirmer(customer.ID),
		true, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("已撤回提案不应接受确认：%v", err)
	}
	second, err := proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "重新提案",
	}, now)
	if err != nil || second.ID == item.ID {
		t.Fatalf("撤回后应可重新提案：%+v err=%v", second, err)
	}
	// 未付款订单不走提案（仍走 §3.2 直接改期）。
	unpaid := seedUnpaidOrder(t, db, "rp-dup-unpaid")
	if _, err = proposeReschedule(ctx, db, unpaid.ID, 11, rescheduleProposalInput{
		ExpectedVersion: unpaid.Version, StartAt: target, Reason: "未付款订单",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("未付款订单不应走改期提案：%v", err)
	}
	// 目标开始时间必须晚于当前，服务时长缺失时须显式给出。
	if _, err = proposeReschedule(ctx, db, order.ID, 11, rescheduleProposalInput{
		ExpectedVersion: order.Version, StartAt: now.Add(-time.Hour), Reason: "过去时间",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("过去的目标开始时间应被拒：%v", err)
	}
	noDuration := seedPaidOrder(t, db, "rp-dup-noduration", 1)
	if _, err = proposeReschedule(ctx, db, noDuration.ID, 11, rescheduleProposalInput{
		ExpectedVersion: noDuration.Version, StartAt: target, Reason: "缺服务时长",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("无服务时长快照时应要求显式指定目标服务时长：%v", err)
	}
	// 尚无成员接单时，成员集合为空，客户确认即可生效。
	solo := seedRescheduleOrder(t, db, "rp-dup-solo", customer.ID, base.Add(24*time.Hour), 60)
	soloItem, err := proposeReschedule(ctx, db, solo.ID, 11, rescheduleProposalInput{
		ExpectedVersion: solo.Version, StartAt: base.Add(30 * time.Hour), Reason: "无人接单的预约单",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	soloDone, err := confirmRescheduleProposal(ctx, db, soloItem.ID,
		rescheduleCustomerConfirmer(customer.ID), true, "同意", now)
	if err != nil {
		t.Fatal(err)
	}
	if soloDone.Status != esportsrescheduleproposal.StatusConfirmed {
		t.Fatalf("无已接单成员时客户确认即可生效：%s", soloDone.Status)
	}
	rescheduleAssertOrderStart(t, db, solo.ID, base.Add(30*time.Hour), "无成员场景生效后")
	// 已付款订单不再允许直接改期，须走提案。
	if _, err = rescheduleOrder(ctx, db, order.ID, 11, rescheduleInput{
		ExpectedVersion: order.Version, StartAt: target, Reason: "直接改期",
	}, now); !errors.Is(err, errRescheduleProposalRequired) {
		t.Fatalf("已付款订单应被拦截并指向提案：%v", err)
	}
}
