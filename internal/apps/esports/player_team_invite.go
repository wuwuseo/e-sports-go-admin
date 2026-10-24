package esports

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// playerInviteTeammates 主陪玩在陪玩端就自己已接单的订单主动邀请固排占剩余空席位（§5.3）。
// 与后台「一次成队」的差别：主陪玩席位已是 accepted，本接口只新增固排邀请，不重复占用主陪玩容量。
// 固排仍须满足与该主陪玩在订单分区已建立固排关系、资质合格、在线、分区等级达标与容量可用。
func playerInviteTeammates(ctx context.Context, db *ent.Client, playerID, orderID int,
	teammatePlayerIDs []int, now time.Time) ([]*ent.EsportsDispatchSeat, error) {
	if playerID < 1 || orderID < 1 || len(teammatePlayerIDs) < 1 || len(teammatePlayerIDs) > 9 {
		return nil, errInvalidInput
	}
	seen := map[int]bool{}
	for _, id := range teammatePlayerIDs {
		if id < 1 || id == playerID || seen[id] {
			return nil, errInvalidInput
		}
		seen[id] = true
	}
	ttl := seatInviteTTL(ctx, db)
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err := ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		(order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch &&
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance) {
		return nil, errConflict
	}
	if err = ensureAppointmentConfirmed(order); err != nil {
		return nil, err
	}
	// 主陪玩身份：本人须在该订单上持有已接单席位。
	onDuty, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !onDuty {
		return nil, errForbidden
	}
	game, err := orderGameIn(ctx, tx, order.SkuID)
	if err != nil {
		return nil, err
	}
	requiredLevel := order.RequiredLevel
	levels := skillLevels(ctx, tx.Client())
	validSet, err := confirmedTeammatePlayerIDs(ctx, tx.Client(), playerID, game)
	if err != nil {
		return nil, err
	}
	valid := make(map[int]bool, len(validSet))
	for _, id := range validSet {
		valid[id] = true
	}
	team := make([]*ent.EsportsPlayer, 0, len(teammatePlayerIDs))
	for _, tid := range teammatePlayerIDs {
		if !valid[tid] {
			return nil, fmt.Errorf("%w：陪玩 %d 不是主陪玩在该分区的固排", errInvalidInput, tid)
		}
		p, err := tx.EsportsPlayer.Get(ctx, tid)
		if err != nil {
			return nil, err
		}
		if p.Qualification != esportsplayer.QualificationQualified ||
			p.Presence != esportsplayer.PresenceOnline {
			return nil, fmt.Errorf("%w：固排 %s 未通过考核或未在线", errForbidden, p.DisplayName)
		}
		if err = reservePlayerDispatchCapacity(ctx, tx, p, order, now, 0); err != nil {
			return nil, err
		}
		if err = checkPlayerGameLevel(levels, p, game, requiredLevel); err != nil {
			return nil, err
		}
		team = append(team, p)
	}
	if err = ensureDispatchSeats(ctx, tx, order); err != nil {
		return nil, err
	}
	open, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusOpen),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(open) < len(team) {
		return nil, fmt.Errorf("%w：空席位不足（需 %d，实有 %d）", errConflict, len(team), len(open))
	}
	// 陪玩发起的邀请不写 invited_by_admin_id，来源记 fixed_team，操作人由订单事件承载。
	for i, p := range team {
		changed, err := tx.EsportsDispatchSeat.Update().Where(
			esportsdispatchseat.IDEQ(open[i].ID),
			esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusOpen),
		).SetPlayerID(p.ID).SetStatus(esportsdispatchseat.StatusInvited).
			SetSource(esportsdispatchseat.SourceFixedTeam).
			SetExpiresAt(now.UTC().Add(ttl)).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version,
		esportsorder.FulfillmentStatusPendingAcceptance)
	if err != nil {
		return nil, err
	}
	if _, err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstInvited, now); err != nil {
		return nil, err
	}
	type seatRef struct {
		SeatIndex int `json:"seat_index"`
		PlayerID  int `json:"player_id"`
	}
	refs := make([]seatRef, 0, len(team))
	for i, p := range team {
		refs = append(refs, seatRef{SeatIndex: open[i].SeatIndex, PlayerID: p.ID})
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.team_invited_by_player").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{
			"team": refs, "main_player_id": playerID,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
}

// playerTeammateCandidates 陪玩端查看本人可邀请的固排候选：先校验本人是该单已接单主陪玩。
func playerTeammateCandidates(ctx context.Context, db *ent.Client, playerID, orderID int) ([]teammateDispatchCandidate, error) {
	if playerID < 1 || orderID < 1 {
		return nil, errInvalidInput
	}
	onDuty, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !onDuty {
		return nil, errForbidden
	}
	return listTeammateCandidatesForOrder(ctx, db, orderID, playerID)
}
