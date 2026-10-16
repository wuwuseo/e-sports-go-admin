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
	"go-admin/internal/ent/esportsteammate"
)

// 固排成队（§5.3）：组队派单时，客服可勾选「主陪玩 + 其固排」一次成队，
// 主陪玩占第一个空席位，选中固排依次占后续空席位，全部变更同事务。

// teammateDispatchCandidate 是固排成队候选（主陪玩在该订单上可一起成队的合格固排）。
type teammateDispatchCandidate struct {
	PlayerID      int    `json:"player_id"`
	DisplayName   string `json:"display_name"`
	Level         string `json:"level"`
	Qualification string `json:"qualification"`
	Presence      string `json:"presence"`
}

// listTeammateCandidatesForOrder 列出主陪玩在该订单上可一次成队的合格固排。
// 硬性条件与人工指派一致（资质合格 + 在线 + 分区/等级 + 未占用席位）；接单上限在成队动作硬校验。
func listTeammateCandidatesForOrder(ctx context.Context, db *ent.Client, orderID, mainPlayerID int) ([]teammateDispatchCandidate, error) {
	if orderID < 1 || mainPlayerID < 1 {
		return nil, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		(order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch &&
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance) {
		return nil, errConflict
	}
	game, err := orderGameWith(ctx, db.EsportsSku, db.EsportsProductCategory, order.SkuID)
	if err != nil {
		return nil, err
	}
	ids, err := confirmedTeammatePlayerIDs(ctx, db, mainPlayerID, game)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []teammateDispatchCandidate{}, nil
	}
	players, err := db.EsportsPlayer.Query().Where(esportsplayer.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	occupied, err := occupiedPlayerIDs(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	requiredLevel := order.RequiredLevel.String()
	out := make([]teammateDispatchCandidate, 0, len(players))
	for _, p := range players {
		if occupied[p.ID] {
			continue
		}
		if p.Qualification != esportsplayer.QualificationQualified ||
			p.Presence != esportsplayer.PresenceOnline {
			continue
		}
		if err := checkPlayerGameLevel(p, game, requiredLevel); err != nil {
			continue
		}
		out = append(out, teammateDispatchCandidate{
			PlayerID:      p.ID,
			DisplayName:   p.DisplayName,
			Level:         p.Level.String(),
			Qualification: p.Qualification.String(),
			Presence:      p.Presence.String(),
		})
	}
	return out, nil
}

// teamDispatch 一次成队（§5.3）：主陪玩占第一个空席位，选中固排依次占后续空席位。
// 主陪玩与每个固排都按人工指派的硬性条件校验；固排必须是与主陪玩在（该订单分区）已建立的固排。
func teamDispatch(ctx context.Context, db *ent.Client, orderID, mainPlayerID int,
	teammatePlayerIDs []int, adminID int, now time.Time) ([]*ent.EsportsDispatchSeat, error) {
	if orderID < 1 || mainPlayerID < 1 || adminID < 1 {
		return nil, errInvalidInput
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
	if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		(order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch &&
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance) {
		return nil, errConflict
	}
	if err = ensureAppointmentConfirmed(order); err != nil {
		return nil, err
	}
	game, err := orderGameIn(ctx, tx, order.SkuID)
	if err != nil {
		return nil, err
	}
	requiredLevel := order.RequiredLevel.String()

	// 主陪玩硬性校验。
	main, err := tx.EsportsPlayer.Get(ctx, mainPlayerID)
	if err != nil {
		return nil, err
	}
	if main.Qualification != esportsplayer.QualificationQualified ||
		main.Presence != esportsplayer.PresenceOnline {
		return nil, errForbidden
	}
	if err = reservePlayerDispatchCapacity(ctx, tx, main, order, now, 0); err != nil {
		return nil, err
	}
	if err = checkPlayerGameLevel(main, game, requiredLevel); err != nil {
		return nil, err
	}

	// 固排必须是主陪玩在该分区已建立的固排，且不重复、不包含主陪玩。
	validSet, err := confirmedTeammatePlayerIDs(ctx, tx.Client(), mainPlayerID, game)
	if err != nil {
		return nil, err
	}
	valid := make(map[int]bool, len(validSet))
	for _, id := range validSet {
		valid[id] = true
	}
	seen := map[int]bool{}
	team := []*ent.EsportsPlayer{main}
	for _, tid := range teammatePlayerIDs {
		if tid < 1 || tid == mainPlayerID || seen[tid] {
			return nil, errInvalidInput
		}
		seen[tid] = true
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
		if err = checkPlayerGameLevel(p, game, requiredLevel); err != nil {
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
		return nil, fmt.Errorf("%w：空席位不足（需 %d，实有 %d），无法一次成队", errConflict, len(team), len(open))
	}

	// 逐席邀请（同一事务，全部成功或全部回滚）；固排成队来源记 fixed_team。
	for i, p := range team {
		changed, err := tx.EsportsDispatchSeat.Update().Where(
			esportsdispatchseat.IDEQ(open[i].ID),
			esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusOpen),
		).SetPlayerID(p.ID).SetStatus(esportsdispatchseat.StatusInvited).
			SetSource(esportsdispatchseat.SourceFixedTeam).SetInvitedByAdminID(adminID).
			SetExpiresAt(now.UTC().Add(ttl)).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, esportsorder.FulfillmentStatusPendingAcceptance)
	if err != nil {
		return nil, err
	}
	if err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstInvited, now); err != nil {
		return nil, err
	}
	// (order_id, order_version) 唯一：一次成队只写一条汇总事件，席位明细挂在 details。
	type seatRef struct {
		SeatIndex int `json:"seat_index"`
		PlayerID  int `json:"player_id"`
	}
	refs := make([]seatRef, 0, len(team))
	for i, p := range team {
		refs = append(refs, seatRef{SeatIndex: open[i].SeatIndex, PlayerID: p.ID})
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.team_dispatched").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"team": refs}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
}

// confirmedTeammatePlayerIDs 返回陪玩在该分区（game 为空表示不限）已建立的固排对方 ID 列表。
func confirmedTeammatePlayerIDs(ctx context.Context, db *ent.Client, playerID int, game string) ([]int, error) {
	q := db.EsportsTeammate.Query().Where(
		esportsteammate.StatusEQ(esportsteammate.StatusConfirmed),
		esportsteammate.Or(
			esportsteammate.PlayerIDEQ(playerID),
			esportsteammate.TeammatePlayerIDEQ(playerID),
		),
	)
	if game != "" {
		q = q.Where(esportsteammate.GameEQ(game))
	}
	records, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(records))
	for _, r := range records {
		other := r.TeammatePlayerID
		if other == playerID {
			other = r.PlayerID
		}
		ids = append(ids, other)
	}
	return ids, nil
}

// occupiedPlayerIDs 返回该订单上已占用席位的陪玩 ID 集合（invited / accepted 均视为占用）。
func occupiedPlayerIDs(ctx context.Context, db *ent.Client, orderID int) (map[int]bool, error) {
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
		esportsdispatchseat.PlayerIDNotNil(),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	occupied := make(map[int]bool, len(seats))
	for _, s := range seats {
		if s.PlayerID != nil {
			occupied[*s.PlayerID] = true
		}
	}
	return occupied, nil
}
