package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/user"
)

// currentPlayer 按 uid 解析陪玩档案：uid → ent.User（active）→ EsportsPlayer（user_id）。
// 空 uid / 查不到 / 用户非 active 一律 errForbidden，仿 customerForVerifiedUser 范式。
func currentPlayer(ctx context.Context, db *ent.Client, uid string) (*ent.EsportsPlayer, error) {
	if uid == "" {
		return nil, errForbidden
	}
	account, err := db.User.Query().Where(user.UIDEQ(uid), user.StatusEQ(user.StatusActive)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	player, err := db.EsportsPlayer.Query().Where(esportsplayer.UserIDEQ(account.ID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	return player, nil
}

type playerInput struct {
	UserID      int    `json:"user_id"`
	DisplayName string `json:"display_name"`
}

// savePlayer 管理端新建陪玩档案：初始资格 pending、在线状态 offline。
func savePlayer(ctx context.Context, db *ent.Client, in playerInput) (*ent.EsportsPlayer, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.UserID < 1 || in.DisplayName == "" || len([]rune(in.DisplayName)) > 120 {
		return nil, errInvalidInput
	}
	return db.EsportsPlayer.Create().SetUserID(in.UserID).SetDisplayName(in.DisplayName).
		SetQualification(esportsplayer.QualificationPending).
		SetPresence(esportsplayer.PresenceOffline).Save(ctx)
}

type playerUpdateInput struct {
	DisplayName   string `json:"display_name"`
	Qualification string `json:"qualification"`
	Presence      string `json:"presence"`
	// 接单上限（§5.5 / §6.4）：0 = 不限。
	DailyLimit      int `json:"daily_limit"`
	ConcurrentLimit int `json:"concurrent_limit"`
	// 接单游戏分区（§5.5 / §6.4）：空表示不限。
	Games []string `json:"games"`
}

// updatePlayer 管理端修改昵称/资格/在线状态/接单上限/接单游戏，条件更新后 version 递增。
func updatePlayer(ctx context.Context, db *ent.Client, id int, in playerUpdateInput) (*ent.EsportsPlayer, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	validQualification := in.Qualification == "pending" || in.Qualification == "qualified" || in.Qualification == "suspended"
	validPresence := in.Presence == "offline" || in.Presence == "online" || in.Presence == "protected_abnormal"
	if id < 1 || in.DisplayName == "" || len([]rune(in.DisplayName)) > 120 ||
		!validQualification || !validPresence || in.DailyLimit < 0 || in.ConcurrentLimit < 0 {
		return nil, errInvalidInput
	}
	// §6.1：实名认证通过前不得将资格置为合格。
	if err := ensurePlayerQualificationAllowed(ctx, db, id, esportsplayer.Qualification(in.Qualification)); err != nil {
		return nil, err
	}
	update := db.EsportsPlayer.Update().Where(esportsplayer.IDEQ(id)).
		SetDisplayName(in.DisplayName).
		SetQualification(esportsplayer.Qualification(in.Qualification)).
		SetPresence(esportsplayer.Presence(in.Presence)).
		SetDailyLimit(in.DailyLimit).SetConcurrentLimit(in.ConcurrentLimit).
		SetGames(in.Games)
	// §6.4：管理端把陪玩置为在线时同样写入心跳基线，超时任务才能据此判断失联。
	if in.Presence == "online" {
		update.SetLastHeartbeatAt(time.Now().UTC())
	}
	changed, err := update.AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, &ent.NotFoundError{}
	}
	return db.EsportsPlayer.Get(ctx, id)
}

// listPlayers 列出陪玩档案，keyword 非空时按昵称模糊匹配（不区分大小写）。
func listPlayers(ctx context.Context, db *ent.Client, keyword string, limit int) ([]*ent.EsportsPlayer, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsPlayer.Query()
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q.Where(esportsplayer.DisplayNameContainsFold(keyword))
	}
	return q.Order(ent.Desc(esportsplayer.FieldID)).Limit(limit).All(ctx)
}

// publicPlayerView 是客户端陪玩推荐的公开字段：绝不包含 user_id 等敏感信息。
type publicPlayerView struct {
	ID            int      `json:"id"`
	DisplayName   string   `json:"display_name"`
	Level         string   `json:"level"`
	Games         []string `json:"games"`
	Qualification string   `json:"qualification"`
	Presence      string   `json:"presence"`
	Online        bool     `json:"online"`
}

// listPublicPlayers 陪玩推荐（AuthPublic）：仅返回合格且在线的陪玩；game 非空时按游戏分区过滤，
// games 为空表示不限分区（§6.4 缺省口径）。
func listPublicPlayers(ctx context.Context, db *ent.Client, game string, limit int) ([]publicPlayerView, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	items, err := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
		esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
	).Order(ent.Desc(esportsplayer.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	game = strings.TrimSpace(game)
	views := make([]publicPlayerView, 0, len(items))
	for _, p := range items {
		if game != "" && len(p.Games) > 0 && !playerGamesContain(p.Games, game) {
			continue
		}
		games := p.Games
		if games == nil {
			games = []string{}
		}
		views = append(views, publicPlayerView{
			ID: p.ID, DisplayName: p.DisplayName, Level: p.Level.String(),
			Games: games, Qualification: p.Qualification.String(),
			Presence: p.Presence.String(), Online: p.Presence == esportsplayer.PresenceOnline,
		})
	}
	return views, nil
}

func playerGamesContain(games []string, game string) bool {
	for _, g := range games {
		if g == game {
			return true
		}
	}
	return false
}

// setPlayerPresence 陪玩自助上下线：条件 UPDATE 设置 presence，冲突时 errConflict。
func setPlayerPresence(ctx context.Context, db *ent.Client, playerID int, online bool) (*ent.EsportsPlayer, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	presence := esportsplayer.PresenceOffline
	if online {
		presence = esportsplayer.PresenceOnline
	}
	update := db.EsportsPlayer.Update().Where(esportsplayer.IDEQ(playerID)).SetPresence(presence)
	// §6.4：上线即写入心跳基线，客户端失联后由超时任务接管自动离线。
	if online {
		update.SetLastHeartbeatAt(time.Now().UTC())
	}
	changed, err := update.AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPlayer.Get(ctx, playerID)
}

type playerDashboardView struct {
	Presence          string `json:"presence"`
	Online            bool   `json:"online"`
	PendingInvites    int    `json:"pending_invites"`
	ActiveOrders      int    `json:"active_orders"`
	TodayCatfoodCents int64  `json:"today_catfood_cents"`
	// 可提现猫粮余额（§6.3 收入状态：待结算 / 已结算可提现 / 已提现）。
	CatfoodBalanceCents int64 `json:"catfood_balance_cents"`
}

// playerOrderIDs 返回我作为有效席位（invited/accepted）关联的订单 ID 集合（去重）。
func playerOrderIDs(ctx context.Context, db *ent.Client, playerID int) ([]int, error) {
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int]struct{}{}
	ids := make([]int, 0, len(seats))
	for _, seat := range seats {
		if _, ok := seen[seat.OrderID]; ok {
			continue
		}
		seen[seat.OrderID] = struct{}{}
		ids = append(ids, seat.OrderID)
	}
	return ids, nil
}

// playerDashboard 陪玩端首页：在线状态、待接邀请数、进行中订单数、当日猫粮收入。
// ponytail: 收入口径简化（未接入 §12 分成），当日口径用 UTC 自然日。
func playerDashboard(ctx context.Context, db *ent.Client, playerID int, now time.Time) (*playerDashboardView, error) {
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return nil, err
	}
	pending, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
		esportsdispatchseat.ExpiresAtGT(now.UTC()),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	active := 0
	ids, err := playerOrderIDs(ctx, db, playerID)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		active, err = db.EsportsOrder.Query().Where(
			esportsorder.IDIn(ids...),
			esportsorder.FulfillmentStatusIn(
				esportsorder.FulfillmentStatusAccepted, esportsorder.FulfillmentStatusInService),
		).Count(ctx)
		if err != nil {
			return nil, err
		}
	}
	utc := now.UTC()
	dayStart := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypePlayerCatfood),
		esportswallettx.OwnerIDEQ(playerID),
		esportswallettx.CreatedAtGTE(dayStart),
	).Select(esportswallettx.FieldPrincipalDeltaCents, esportswallettx.FieldBonusDeltaCents).All(ctx)
	if err != nil {
		return nil, err
	}
	var today int64
	for _, row := range rows {
		// 只统计正值：收入口径，不冲抵扣减。
		if delta := row.PrincipalDeltaCents + row.BonusDeltaCents; delta > 0 {
			today += delta
		}
	}
	// 已结算可提现＝猫粮账户余额（结算时写入，提现申请时扣减）。
	catfood, err := walletBalanceOf(ctx, db, walletPlayerCatfood, playerID)
	if err != nil {
		return nil, err
	}
	return &playerDashboardView{
		Presence:            player.Presence.String(),
		Online:              player.Presence == esportsplayer.PresenceOnline,
		PendingInvites:      pending,
		ActiveOrders:        active,
		TodayCatfoodCents:   today,
		CatfoodBalanceCents: catfood.total(),
	}, nil
}

type playerInviteView struct {
	ID                int       `json:"id"`
	SeatIndex         int       `json:"seat_index"`
	OrderID           int       `json:"order_id"`
	OrderNo           string    `json:"order_no"`
	CustomerID        int       `json:"customer_id"`
	SkuID             int       `json:"sku_id"`
	PayableCents      int64     `json:"payable_cents"`
	FulfillmentStatus string    `json:"fulfillment_status"`
	RequiredSeats     int       `json:"required_seats"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// listPlayerInvites 列出我的待接邀请（status=invited 且未过期），附订单摘要。
func listPlayerInvites(ctx context.Context, db *ent.Client, playerID int, now time.Time) ([]*playerInviteView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
		esportsdispatchseat.ExpiresAtGT(now.UTC()),
	).Order(ent.Desc(esportsdispatchseat.FieldID)).Limit(100).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]*playerInviteView, 0, len(seats))
	for _, seat := range seats {
		order, err := db.EsportsOrder.Get(ctx, seat.OrderID)
		if err != nil {
			return nil, err
		}
		if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
			continue
		}
		view := &playerInviteView{
			ID: seat.ID, SeatIndex: seat.SeatIndex, OrderID: order.ID, OrderNo: order.OrderNo,
			CustomerID: order.CustomerID, SkuID: order.SkuID, PayableCents: order.PayableCents,
			FulfillmentStatus: order.FulfillmentStatus.String(), RequiredSeats: order.RequiredSeats,
		}
		if seat.ExpiresAt != nil {
			view.ExpiresAt = *seat.ExpiresAt
		}
		views = append(views, view)
	}
	return views, nil
}

// respondPlayerInvite 陪玩回应邀请。接受时若该订单全部席位均已 accepted，则把订单推到 accepted；
// 拒绝时若无剩余有效席位，则把订单退回 pending_dispatch。全部变更同事务并写订单事件。
func respondPlayerInvite(ctx context.Context, db *ent.Client, playerID, seatID int, accept bool, now time.Time) error {
	if playerID < 1 || seatID < 1 {
		return errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seat, err := tx.EsportsDispatchSeat.Get(ctx, seatID)
	if err != nil {
		return err
	}
	if seat.PlayerID == nil || *seat.PlayerID != playerID {
		return errForbidden
	}
	if seat.Status != esportsdispatchseat.StatusInvited ||
		seat.ExpiresAt == nil || !seat.ExpiresAt.After(now.UTC()) {
		return errConflict
	}
	target := esportsdispatchseat.StatusRejected
	var player *ent.EsportsPlayer
	if accept {
		target = esportsdispatchseat.StatusAccepted
		player, err = tx.EsportsPlayer.Get(ctx, playerID)
		if err != nil {
			return err
		}
	}
	// 提前查订单：接受时校验分区 / 等级（§5.5），拒绝时用于回退判断。
	order, err := tx.EsportsOrder.Get(ctx, seat.OrderID)
	if err != nil {
		return err
	}
	if accept {
		if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
			return errConflict
		}
		// 本席位在邀请时已预占容量；接受时排除自身，并串行检查其他订单的占位。
		if err = reservePlayerDispatchCapacity(ctx, tx, player, order, now, seat.ID); err != nil {
			return err
		}
		game, gErr := orderGameIn(ctx, tx, order.SkuID)
		if gErr != nil {
			return gErr
		}
		if err = checkPlayerGameLevel(player, game, order.RequiredLevel.String()); err != nil {
			return err
		}
	}
	update := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.IDEQ(seatID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
	).SetStatus(target).SetRespondedAt(now.UTC()).AddVersion(1)
	if accept {
		rule, ruleErr := resolveCommissionRuleForPlayerIn(ctx, tx.EsportsCommissionRule,
			tx.EsportsSku, tx.EsportsProductCategory, order, playerID, player.Level.String(), now)
		if ruleErr != nil {
			return ruleErr
		}
		update.SetCommissionRuleLockedAt(now.UTC())
		if rule != nil {
			update.SetCommissionRuleID(rule.ID)
		} else {
			update.ClearCommissionRuleID()
		}
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	actorID := strconv.Itoa(playerID)
	if accept {
		// 存在任一非 accepted 席位则表示尚未接满。
		notAllAccepted, err := tx.EsportsDispatchSeat.Query().Where(
			esportsdispatchseat.OrderIDEQ(seat.OrderID),
			esportsdispatchseat.StatusNEQ(esportsdispatchseat.StatusAccepted),
		).Exist(ctx)
		if err != nil {
			return err
		}
		if notAllAccepted {
			return tx.Commit()
		}
		newVersion, err := bumpOrder(ctx, tx, seat.OrderID, order.Version, esportsorder.FulfillmentStatusAccepted)
		if err != nil {
			return err
		}
		// §5.9：首次有效接单的时间点（只写一次）。
		if err = markOrderTimelineIn(ctx, tx, seat.OrderID, timelineFirstAccepted, now); err != nil {
			return err
		}
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(seat.OrderID).
			SetOrderVersion(newVersion).SetEventType("order.seat_accepted").
			SetActorType("player").SetActorID(actorID).
			SetDetails(map[string]interface{}{"seat_index": seat.SeatIndex}).Save(ctx); err != nil {
			return err
		}
		return tx.Commit()
	}
	back, err := rollbackIfNoActiveSeats(ctx, tx, order)
	if err != nil {
		return err
	}
	targetStatus := esportsorder.FulfillmentStatus("")
	if back {
		targetStatus = esportsorder.FulfillmentStatusPendingDispatch
	}
	newVersion, err := bumpOrder(ctx, tx, seat.OrderID, order.Version, targetStatus)
	if err != nil {
		return err
	}
	// 退回待派单：优先通知已开过窗时直接释放到大厅，否则按名单重新开窗（§5.4 业务口径）。
	if targetStatus == esportsorder.FulfillmentStatusPendingDispatch {
		if err = onOrderEnteredPendingDispatch(ctx, tx, seat.OrderID, now); err != nil {
			return err
		}
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(seat.OrderID).
		SetOrderVersion(newVersion).SetEventType("order.seat_rejected").
		SetActorType("player").SetActorID(actorID).
		SetDetails(map[string]interface{}{"seat_index": seat.SeatIndex}).Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// listGrabHall 抢单大厅：可抢的 grabbing 单席位订单，外加「优先通知中且本人被通知」的待派单单席位订单。
// 优先通知窗口内的订单对未被通知的陪玩**不可见**（名单内独占）；窗口超时、名单内全部拒绝或客服提前
// 释放后订单转 grabbing，才对所有人开放（§5.4 业务口径）。
func listGrabHall(ctx context.Context, db *ent.Client, playerID int, now time.Time) ([]*ent.EsportsOrder, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	public, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusGrabbing),
		esportsorder.RequiredSeatsEQ(1),
	).Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(ctx)
	if err != nil {
		return nil, err
	}
	noticedIDs, err := noticedPendingOrderIDs(ctx, db, playerID, now)
	if err != nil {
		return nil, err
	}
	var noticed []*ent.EsportsOrder
	if len(noticedIDs) > 0 {
		noticed, err = db.EsportsOrder.Query().Where(
			esportsorder.IDIn(noticedIDs...),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
			esportsorder.RequiredSeatsEQ(1),
		).Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(ctx)
		if err != nil {
			return nil, err
		}
	}
	// 优先通知单排在前面：它们对本人有时限，先到先得。
	merged := make([]*ent.EsportsOrder, 0, len(noticed)+len(public))
	merged = append(merged, noticed...)
	merged = append(merged, public...)
	if len(merged) == 0 {
		return merged, nil
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	player, err := tx.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return nil, err
	}
	if player.Qualification != esportsplayer.QualificationQualified {
		return []*ent.EsportsOrder{}, nil
	}
	// 大厅筛选无特定订单：按「是否还有容量」粗筛，不参与时段重叠判定。
	if err = checkPlayerDispatchLimit(ctx, tx, player, nil, now, 0); err != nil {
		if errors.Is(err, errInvalidInput) {
			return []*ent.EsportsOrder{}, nil
		}
		return nil, err
	}
	ids := make([]int, 0, len(merged))
	for _, order := range merged {
		ids = append(ids, order.ID)
	}
	occupied, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDIn(ids...),
		esportsdispatchseat.StatusIn(esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	occupiedOrders := make(map[int]bool, len(occupied))
	for _, seat := range occupied {
		occupiedOrders[seat.OrderID] = true
	}
	gameCache := map[int]string{}
	visible := make([]*ent.EsportsOrder, 0, len(merged))
	for _, order := range merged {
		if order.PaymentStatus != esportsorder.PaymentStatusPaid || occupiedOrders[order.ID] {
			continue
		}
		game, ok := gameCache[order.SkuID]
		if !ok {
			game, err = orderGameIn(ctx, tx, order.SkuID)
			if err != nil {
				return nil, err
			}
			gameCache[order.SkuID] = game
		}
		if err = checkPlayerGameLevel(player, game, order.RequiredLevel.String()); err != nil {
			if errors.Is(err, errForbidden) {
				continue
			}
			return nil, err
		}
		visible = append(visible, order)
	}
	return visible, nil
}

// grabPlayerOrder 抢单：合格且「可抢新单」的陪玩抢下 grabbing 单席位订单的唯一 open 席位。
// 可抢新单 = 非 protected_abnormal 且（online 或 心跳新鲜）：人工下线但连接正常仍可抢，
// 自动离线（心跳超时）与连接异常（protected_abnormal）禁止抢新单（§6.4）。
func grabPlayerOrder(ctx context.Context, db *ent.Client, playerID, orderID int, now time.Time) (*ent.EsportsOrder, error) {
	if playerID < 1 || orderID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	player, err := tx.EsportsPlayer.Get(ctx, playerID)
	if err != nil {
		return nil, err
	}
	if player.Qualification != esportsplayer.QualificationQualified {
		return nil, errForbidden
	}
	if !playerCanGrabNewOrder(ctx, db, player, now) {
		return nil, errForbidden
	}
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if err = ensureAppointmentConfirmed(order); err != nil {
		return nil, err
	}
	if err = reservePlayerDispatchCapacity(ctx, tx, player, order, now, 0); err != nil {
		return nil, err
	}
	if order.RequiredSeats != 1 {
		return nil, errConflict
	}
	game, err := orderGameIn(ctx, tx, order.SkuID)
	if err != nil {
		return nil, err
	}
	if err = checkPlayerGameLevel(player, game, order.RequiredLevel.String()); err != nil {
		return nil, err
	}
	rule, err := resolveCommissionRuleForPlayerIn(ctx, tx.EsportsCommissionRule,
		tx.EsportsSku, tx.EsportsProductCategory, order, playerID, player.Level.String(), now)
	if err != nil {
		return nil, err
	}
	// 两种可抢状态：① 已释放到大厅的 grabbing；② 优先通知窗口内的待派单（仅限被通知的本人）。
	fromNotice := order.FulfillmentStatus == esportsorder.FulfillmentStatusPendingDispatch &&
		dispatchPriorityWindowOpen(order, now)
	switch {
	case fromNotice:
		noticed, err := playerNotifiedForOrderTx(ctx, tx, orderID, playerID)
		if err != nil {
			return nil, err
		}
		if !noticed {
			return nil, errDispatchPriorityHold
		}
	case order.FulfillmentStatus == esportsorder.FulfillmentStatusGrabbing:
	default:
		return nil, errConflict
	}
	if err = ensureDispatchSeats(ctx, tx, order); err != nil {
		return nil, err
	}
	update := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusOpen),
	).SetPlayerID(playerID).SetStatus(esportsdispatchseat.StatusAccepted).
		SetSource(esportsdispatchseat.SourceGrab).SetRespondedAt(now.UTC()).
		SetCommissionRuleLockedAt(now.UTC()).AddVersion(1)
	if rule != nil {
		update.SetCommissionRuleID(rule.ID)
	} else {
		update.ClearCommissionRuleID()
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if fromNotice {
		// 名单内接单：闭合窗口（其余待回应通知作废），订单直接进入已接单。
		if err = closeDispatchPriorityWindowOnAccept(ctx, tx, order, playerID, now); err != nil {
			return nil, err
		}
		changed, err = tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version+1),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
		).SetFulfillmentStatus(esportsorder.FulfillmentStatusAccepted).AddVersion(1).Save(ctx)
	} else {
		changed, err = tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusGrabbing),
		).SetFulfillmentStatus(esportsorder.FulfillmentStatusAccepted).AddVersion(1).Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	// §5.9：首次有效接单的时间点（只写一次）。
	if err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstAccepted, now); err != nil {
		return nil, err
	}
	eventVersion := order.Version + 1
	if fromNotice {
		eventVersion = order.Version + 2
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(eventVersion).SetEventType("order.grabbed").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{"seat_index": 1, "from_priority_notice": fromNotice}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// listPlayerOrders 我的订单：按 bucket 过滤我作为有效席位关联的订单；bucket 为空则返回全部未结束订单。
func listPlayerOrders(ctx context.Context, db *ent.Client, playerID int, bucket string) ([]*ent.EsportsOrder, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	bucket = strings.TrimSpace(bucket)
	buckets := map[string]esportsorder.FulfillmentStatus{
		"pending_acceptance": esportsorder.FulfillmentStatusPendingAcceptance,
		"accepted":           esportsorder.FulfillmentStatusAccepted,
		"in_service":         esportsorder.FulfillmentStatusInService,
		"pending_close":      esportsorder.FulfillmentStatusPendingClose,
		"completed":          esportsorder.FulfillmentStatusCompleted,
	}
	if bucket != "" {
		if _, ok := buckets[bucket]; !ok {
			return nil, errInvalidInput
		}
	}
	ids, err := playerOrderIDs(ctx, db, playerID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []*ent.EsportsOrder{}, nil
	}
	q := db.EsportsOrder.Query().Where(esportsorder.IDIn(ids...))
	if bucket != "" {
		q.Where(esportsorder.FulfillmentStatusEQ(buckets[bucket]))
	} else {
		q.Where(esportsorder.FulfillmentStatusNotIn(
			esportsorder.FulfillmentStatusCompleted, esportsorder.FulfillmentStatusCancelled))
	}
	return q.Order(ent.Desc(esportsorder.FieldID)).Limit(100).All(ctx)
}

// startPlayerService 开始服务：为我的已接单席位创建 in_service 服务段并把订单推进到 in_service。
func startPlayerService(ctx context.Context, db *ent.Client, playerID, orderID int, now time.Time) (*ent.EsportsOrder, error) {
	if playerID < 1 || orderID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	// 允许 accepted 与 in_service：多人单里后续成员也可各自开始服务段。
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return nil, errConflict
	}
	seat, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.PlayerIDEQ(playerID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	createSegment := tx.EsportsServiceSegment.Create().SetOrderID(orderID).SetPlayerID(playerID).
		SetSeatIndex(seat.SeatIndex).SetStatus(esportsservicesegment.StatusInService).
		SetStartedAt(now.UTC())
	if seat.CommissionRuleLockedAt != nil {
		createSegment.SetCommissionRuleLockedAt(*seat.CommissionRuleLockedAt)
		if seat.CommissionRuleID != nil {
			createSegment.SetCommissionRuleID(*seat.CommissionRuleID)
		}
	}
	if _, err = createSegment.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	// 首个成员开工才把订单推进到 in_service；后续成员开工只抬版本以承载唯一事件。
	target := esportsorder.FulfillmentStatus("")
	if order.FulfillmentStatus == esportsorder.FulfillmentStatusAccepted {
		target = esportsorder.FulfillmentStatusInService
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, target)
	if err != nil {
		return nil, err
	}
	// §5.9：开工（首个成员开始服务）的时间点（只写一次）。
	if err = markOrderTimelineIn(ctx, tx, orderID, timelineServiceStarted, now); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.service_started").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{"seat_index": seat.SeatIndex}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// finishPlayerService 结束服务：分别保存计费服务量、陪玩报告的服务秒数与系统有效秒数。
// 差异过大时写客服核实事件，全部席位结束后订单进入 pending_close。
func finishPlayerService(ctx context.Context, db *ent.Client, playerID, orderID int, reportedUnits, reportedSeconds int64, evidenceRef string, now time.Time) (*ent.EsportsOrder, error) {
	evidenceRef = strings.TrimSpace(evidenceRef)
	if playerID < 1 || orderID < 1 || reportedUnits < 0 || reportedSeconds <= 0 ||
		len([]rune(evidenceRef)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	segment, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.PlayerIDEQ(playerID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	systemSeconds := int64(now.UTC().Sub(segment.StartedAt).Seconds()) - segment.PausedSeconds
	if systemSeconds < 0 {
		systemSeconds = 0
	}
	changed, err := tx.EsportsServiceSegment.Update().Where(
		esportsservicesegment.IDEQ(segment.ID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusInService),
	).SetStatus(esportsservicesegment.StatusFinished).SetEndedAt(now.UTC()).
		SetSystemSeconds(systemSeconds).SetReportedUnits(reportedUnits).SetReportedSeconds(reportedSeconds).
		SetEvidenceRef(evidenceRef).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	seatCount, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	finishedCount, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	target := esportsorder.FulfillmentStatus("")
	if seatCount > 0 && finishedCount >= seatCount {
		target = esportsorder.FulfillmentStatusPendingClose
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, target)
	if err != nil {
		return nil, err
	}
	durationMismatch := serviceDurationMismatch(systemSeconds, reportedSeconds,
		serviceMismatchMinimumSeconds(ctx, tx.Client()))
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.service_finished").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(map[string]interface{}{
			"seat_index": segment.SeatIndex, "reported_units": reportedUnits,
			"reported_seconds": reportedSeconds, "system_seconds": systemSeconds,
			"duration_mismatch": durationMismatch,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if durationMismatch {
		payload := map[string]interface{}{
			"order_no": order.OrderNo, "order_id": orderID, "player_id": playerID,
			"seat_index": segment.SeatIndex, "reported_seconds": reportedSeconds,
			"system_seconds": systemSeconds,
		}
		if order.ResponsibleAdminID != nil {
			payload["admin_id"] = *order.ResponsibleAdminID
		}
		if _, err = tx.EsportsOutbox.Create().
			SetEventKey("service.duration_mismatch:" + strconv.Itoa(segment.ID)).
			SetEventType("service.duration_mismatch").SetAggregateRef(order.OrderNo).
			SetPayload(payload).Save(ctx); err != nil {
			return nil, err
		}
	}
	// §13.2：差异超过严重阈值时自动建立售后单（channel=system，来源陪玩为该成员），
	// 让客服在统一售后状态机内核实与处置；同订单同问题已有进行中售后单时跳过（幂等）。
	if durationMismatch && serviceDurationMismatch(systemSeconds, reportedSeconds,
		serviceSevereSeconds(ctx, tx.Client())) {
		actor := playerID
		_, err = createAfterSaleTx(ctx, tx, afterSaleInput{
			SourceType: "order", SourceID: orderID, Kind: "duration_short", Channel: "system",
			Demand: fmt.Sprintf("系统检测到严重服务时长差异：陪玩上报 %d 秒，系统计时 %d 秒，请核实并处置",
				reportedSeconds, systemSeconds),
			ExpectedPlan: "核实后按差额退款或补钟",
		}, afterSaleCreateOptions{
			ActorType: "system", ActorID: "service_duration", PlayerID: &actor,
			Details: map[string]interface{}{
				"auto_created": true, "seat_index": segment.SeatIndex,
				"reported_seconds": reportedSeconds, "system_seconds": systemSeconds,
			},
		})
		if err != nil && !errors.Is(err, errConflict) {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}
