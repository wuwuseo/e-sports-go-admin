package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsseattransferrequest"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
)

func openTransferDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// setUpConfig 写入（或覆盖）一条系统配置。
func setUpConfig(t *testing.T, db *ent.Client, key, value string) {
	t.Helper()
	ctx := t.Context()
	if changed, err := db.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).
		SetValue(value).Save(ctx); err != nil {
		t.Fatal(err)
	} else if changed > 0 {
		return
	}
	db.SystemConfig.Create().SetKey(key).SetName(key).SetValue(value).
		SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)
}

// acceptSeat 指派并接受，得到一个 accepted 席位与 accepted 订单。
func acceptSeat(t *testing.T, db *ent.Client, orderID, seatIndex, playerID, adminID int, now time.Time) *ent.EsportsDispatchSeat {
	t.Helper()
	ctx := t.Context()
	seat, err := assignDispatchSeat(ctx, db, orderID, seatIndex, playerID, adminID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = respondPlayerInvite(ctx, db, playerID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	return db.EsportsDispatchSeat.GetX(ctx, seat.ID)
}

// 转单申请 → 客服确认通过：释放席位为待补位并把订单退回待派单。
func TestSeatTransferApproveReleasesSeat(t *testing.T) {
	db := openTransferDB(t, "esports_seat_transfer_approve")
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "transfer-approve-1", 1)
	player := seedPlayer(t, db, 201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat := acceptSeat(t, db, order.ID, 1, player.ID, 11, now)
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("接单后订单应为 accepted，got=%s", got)
	}

	item, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "临时有事", IdempotencyKey: "transfer-key-0001",
	}, now)
	if err != nil {
		t.Fatalf("发起转单应成功，err=%v", err)
	}
	if item.Status != esportsseattransferrequest.StatusPending || item.SeatID != seat.ID {
		t.Fatalf("转单申请内容不正确：%+v", item)
	}
	// 确认前席位与订单状态都不变。
	if got := db.EsportsDispatchSeat.GetX(ctx, seat.ID).Status; got != esportsdispatchseat.StatusAccepted {
		t.Fatalf("确认前席位应保持 accepted，got=%s", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("确认前订单应保持 accepted，got=%s", got)
	}
	// 同一席位已有待确认申请 → 冲突。
	if _, err = requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "再发一次", IdempotencyKey: "transfer-key-0002",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复申请应冲突，err=%v", err)
	}
	// 同一幂等键重放 → 返回既有申请。
	replay, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "临时有事", IdempotencyKey: "transfer-key-0001",
	}, now)
	if err != nil || replay.ID != item.ID {
		t.Fatalf("幂等重放应返回既有申请，got=%+v err=%v", replay, err)
	}

	decided, err := decideSeatTransfer(ctx, db, 22, item.ID, true, "已找到替补", now)
	if err != nil {
		t.Fatalf("确认转单应成功，err=%v", err)
	}
	if decided.Status != esportsseattransferrequest.StatusApproved ||
		decided.DecidedByAdminID == nil || *decided.DecidedByAdminID != 22 || decided.DecidedAt == nil {
		t.Fatalf("确认留痕不正确：%+v", decided)
	}
	released := db.EsportsDispatchSeat.GetX(ctx, seat.ID)
	if released.Status != esportsdispatchseat.StatusOpen || released.PlayerID != nil {
		t.Fatalf("通过后席位应回到待补位并清空陪玩：%+v", released)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("无剩余席位时订单应退回待派单，got=%s", got)
	}
	if countEventsByType(t, db)["order.seat_transfer_approved"] != 1 {
		t.Fatal("缺少 order.seat_transfer_approved 事件")
	}
	// 重复确认冲突。
	if _, err = decideSeatTransfer(ctx, db, 22, item.ID, true, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复确认应冲突，err=%v", err)
	}
}

// 多人单通过转单只释放该席位，订单退回待接单等待补位。
func TestSeatTransferMultiSeatKeepsPendingAcceptance(t *testing.T) {
	db := openTransferDB(t, "esports_seat_transfer_multi")
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "transfer-multi-1", 2)
	first := seedPlayer(t, db, 301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	second := seedPlayer(t, db, 302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	acceptSeat(t, db, order.ID, 1, first.ID, 11, now)
	secondSeat := acceptSeat(t, db, order.ID, 2, second.ID, 11, now)

	item, err := requestSeatTransfer(ctx, db, second.ID, order.ID, seatTransferInput{
		Reason: "有事", IdempotencyKey: "transfer-multi-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decideSeatTransfer(ctx, db, 22, item.ID, true, "", now); err != nil {
		t.Fatalf("确认转单应成功，err=%v", err)
	}
	if got := db.EsportsDispatchSeat.GetX(ctx, secondSeat.ID).Status; got != esportsdispatchseat.StatusOpen {
		t.Fatalf("该席位应回到待补位，got=%s", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusPendingAcceptance {
		t.Fatalf("仍有队友占位时应退回待接单，got=%s", got)
	}
}

// 驳回保持原状；驳回须填原因；撤回后不能再确认。
func TestSeatTransferRejectAndCancel(t *testing.T) {
	db := openTransferDB(t, "esports_seat_transfer_reject")
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "transfer-reject-1", 1)
	player := seedPlayer(t, db, 401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat := acceptSeat(t, db, order.ID, 1, player.ID, 11, now)

	item, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "有事", IdempotencyKey: "transfer-reject-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 驳回必须填原因。
	if _, err = decideSeatTransfer(ctx, db, 22, item.ID, false, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("驳回未填原因应被拒，err=%v", err)
	}
	rejected, err := decideSeatTransfer(ctx, db, 22, item.ID, false, "人手不足，先坚持一下", now)
	if err != nil {
		t.Fatalf("驳回应成功，err=%v", err)
	}
	if rejected.Status != esportsseattransferrequest.StatusRejected || rejected.DecisionNote == "" {
		t.Fatalf("驳回留痕不正确：%+v", rejected)
	}
	// 驳回后席位与订单都不变。
	if got := db.EsportsDispatchSeat.GetX(ctx, seat.ID).Status; got != esportsdispatchseat.StatusAccepted {
		t.Fatalf("驳回后席位应保持 accepted，got=%s", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID).FulfillmentStatus; got != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("驳回后订单应保持 accepted，got=%s", got)
	}
	// 他人不能撤回；本人撤回后不能再确认。
	if _, err = cancelSeatTransfer(ctx, db, other.ID, item.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("他人撤回应被拒，err=%v", err)
	}
	if _, err = cancelSeatTransfer(ctx, db, player.ID, item.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("已驳回的申请不能撤回，err=%v", err)
	}

	// 新申请由本人撤回后再确认应冲突。
	second, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "再试一次", IdempotencyKey: "transfer-cancel-key",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := cancelSeatTransfer(ctx, db, player.ID, second.ID, now)
	if err != nil {
		t.Fatalf("本人撤回应成功，err=%v", err)
	}
	if cancelled.Status != esportsseattransferrequest.StatusCancelled {
		t.Fatalf("撤回后状态应为 cancelled，got=%s", cancelled.Status)
	}
	if _, err = decideSeatTransfer(ctx, db, 22, second.ID, true, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("已撤回的申请不能确认，err=%v", err)
	}
}

// 转单守卫：非本人席位、未接单、参数非法、已开始服务都被拒。
func TestSeatTransferGuards(t *testing.T) {
	db := openTransferDB(t, "esports_seat_transfer_guards")
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "transfer-guard-1", 1)
	player := seedPlayer(t, db, 501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 502, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	acceptSeat(t, db, order.ID, 1, player.ID, 11, now)

	// 非本单陪玩发起 → 无权。
	if _, err := requestSeatTransfer(ctx, db, other.ID, order.ID, seatTransferInput{
		Reason: "有事", IdempotencyKey: "transfer-guard-key-1",
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本单陪玩发起应被拒，err=%v", err)
	}
	// 参数非法。
	for i, in := range []seatTransferInput{
		{Reason: "  ", IdempotencyKey: "transfer-guard-key-2"},
		{Reason: "有事", IdempotencyKey: "short"},
	} {
		if _, err := requestSeatTransfer(ctx, db, player.ID, order.ID, in, now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法入参应被拒，err=%v", i+1, err)
		}
	}
	// ：服务中允许转单（确认通过时先结算原陪玩服务段再由新陪玩接手），原「已开始服务后
	// 不可转单」的口径已被取代；仅当该陪玩在本单的服务段**已结算**时才拒绝。
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(player.ID).
		SetSeatIndex(1).SetStartedAt(now.UTC()).
		SetStatus(esportsservicesegment.StatusInService).SaveX(ctx)
	if _, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "有事", IdempotencyKey: "transfer-guard-key-3",
	}, now); err != nil {
		t.Fatalf("服务中应允许发起转单，err=%v", err)
	}
	db.EsportsServiceSegment.Update().Where(
		esportsservicesegment.OrderIDEQ(order.ID),
		esportsservicesegment.PlayerIDEQ(player.ID),
	).SetStatus(esportsservicesegment.StatusFinished).SetEndedAt(now.UTC()).SaveX(ctx)
	if _, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "有事", IdempotencyKey: "transfer-guard-key-3b",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("服务段已结算应被拒，err=%v", err)
	}
	// 订单已结束 → 不可发起。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	if _, err := requestSeatTransfer(ctx, db, player.ID, order.ID, seatTransferInput{
		Reason: "有事", IdempotencyKey: "transfer-guard-key-4",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("已结束订单应被拒，err=%v", err)
	}
	// 状态过滤非法。
	if _, err := listSeatTransfers(ctx, db, "unknown", 0, 0, 10); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态应被拒，err=%v", err)
	}
}

// 催单阶梯：按可配置档位逐级提醒，第 1 档提醒陪玩、第 2 档起提醒客服，重复运行不重复发。
func TestRemindLadderMatchesAndIsIdempotent(t *testing.T) {
	db := openTransferDB(t, "esports_remind_ladder")
	ctx := t.Context()
	now := time.Now()
	// 邀请有效期 60 分钟，便于构造「已过 20 分钟」。
	setUpConfig(t, db, dispatchInviteTTLKey, "60")
	setUpConfig(t, db, remindLadderKey, "5,15,30")
	order := seedPaidOrder(t, db, "remind-ladder-1", 1)
	player := seedPlayer(t, db, 601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	// 反推邀请发出时间 = 到期时间 − 60 分钟；置为 20 分钟前发出 → 命中第 2 档。
	db.EsportsDispatchSeat.UpdateOneID(seat.ID).
		SetExpiresAt(now.UTC().Add(40 * time.Minute)).SaveX(ctx)

	if levels := remindLadderMinutes(ctx, db); len(levels) != 3 || levels[0] != 5 || levels[2] != 30 {
		t.Fatalf("阶梯解析不正确：%v", levels)
	}
	if got := matchedRemindLevel([]int{5, 15, 30}, 20); got != 2 {
		t.Fatalf("20 分钟应命中第 2 档，got=%d", got)
	}
	if got := matchedRemindLevel([]int{5, 15, 30}, 4); got != 0 {
		t.Fatalf("未到第 1 档应返回 0，got=%d", got)
	}
	count, err := runRemindLadder(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("阶梯催单应成功，err=%v", err)
	}
	if count != 2 {
		t.Fatalf("应补发第 1、2 档共 2 条提醒，got=%d", count)
	}
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("dispatch.remind_player")).ExistX(ctx) {
		t.Fatal("第 1 档应提醒陪玩")
	}
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("dispatch.remind_admin")).ExistX(ctx) {
		t.Fatal("第 2 档应升级提醒客服")
	}
	if countEventsByType(t, db)["order.reminded"] != 2 {
		t.Fatal("应写两条 order.reminded 事件")
	}
	// 幂等：重复运行不重复发。
	if again, err := runRemindLadder(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	} else if again != 0 {
		t.Fatalf("重复运行不应重复提醒，got=%d", again)
	}
	// 时间推进到第 3 档时才补发第 3 档。
	if sent, err := runRemindLadder(ctx, db, now.Add(15*time.Minute), 100); err != nil {
		t.Fatal(err)
	} else if sent != 1 {
		t.Fatalf("推进到第 3 档应补发 1 条，got=%d", sent)
	}
}

// 服务超时提醒：服务段有效时长超过阈值提醒客服，重复运行幂等，阈值为 0 时关闭。
func TestServiceOvertimeReminder(t *testing.T) {
	db := openTransferDB(t, "esports_service_overtime")
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "service-overtime-1", 1)
	player := seedPlayer(t, db, 701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(player.ID).
		SetSeatIndex(1).SetStartedAt(now.UTC().Add(-40 * time.Minute)).
		SetStatus(esportsservicesegment.StatusInService).SaveX(ctx)

	// 未配置阈值 → 关闭，不发提醒。
	if count, err := checkServiceOvertime(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	} else if count != 0 {
		t.Fatalf("阈值为 0 时应关闭提醒，got=%d", count)
	}
	setUpConfig(t, db, serviceOvertimeKey, "30")
	if minutes := serviceOvertimeMinutes(ctx, db); minutes != 30 {
		t.Fatalf("阈值解析不正确：%d", minutes)
	}
	count, err := checkServiceOvertime(ctx, db, now, 100)
	if err != nil {
		t.Fatalf("服务超时检查应成功，err=%v", err)
	}
	if count != 1 {
		t.Fatalf("应提醒 1 次，got=%d", count)
	}
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("dispatch.service_overtime")).ExistX(ctx) {
		t.Fatal("应写服务超时提醒事件")
	}
	if countEventsByType(t, db)["order.service_overtime"] != 1 {
		t.Fatal("应写 order.service_overtime 订单事件")
	}
	// 幂等。
	if again, err := checkServiceOvertime(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	} else if again != 0 {
		t.Fatalf("重复运行不应重复提醒，got=%d", again)
	}
	// 提高阈值后，尚未超过新阈值的服务段不再提醒。
	setUpConfig(t, db, serviceOvertimeKey, "120")
	if count, err := checkServiceOvertime(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	} else if count != 0 {
		t.Fatalf("提高阈值后不应新增提醒，got=%d", count)
	}
}
