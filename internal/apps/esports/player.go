package esports

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
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
	// 接单上限：0 = 不限。
	DailyLimit      int `json:"daily_limit"`
	ConcurrentLimit int `json:"concurrent_limit"`
	// 接单游戏分区：空表示不限。
	Games []string `json:"games"`
	// 五项接单配置：指针表示「未传即不修改」（管理端表单不会因漏字段清空陪玩自设配置）。
	ServiceSlotStartMinute   *int   `json:"service_slot_start_minute"`
	ServiceSlotEndMinute     *int   `json:"service_slot_end_minute"`
	AutoOnline               *bool  `json:"auto_online"`
	AcceptPriceMinCents      *int64 `json:"accept_price_min_cents"`
	AcceptPriceMaxCents      *int64 `json:"accept_price_max_cents"`
	FixedTeamEnabled         *bool  `json:"fixed_team_enabled"`
	FixedTeamSlotStartMinute *int   `json:"fixed_team_slot_start_minute"`
	FixedTeamSlotEndMinute   *int   `json:"fixed_team_slot_end_minute"`
}

// updatePlayer 管理端修改昵称/资格/在线状态/接单上限/接单游戏/五项接单配置，条件更新后 version 递增。
func updatePlayer(ctx context.Context, db *ent.Client, id int, in playerUpdateInput) (*ent.EsportsPlayer, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	validQualification := in.Qualification == "pending" || in.Qualification == "qualified" || in.Qualification == "suspended"
	validPresence := in.Presence == "offline" || in.Presence == "online" || in.Presence == "protected_abnormal"
	if id < 1 || in.DisplayName == "" || len([]rune(in.DisplayName)) > 120 ||
		!validQualification || !validPresence || in.DailyLimit < 0 || in.ConcurrentLimit < 0 {
		return nil, errInvalidInput
	}
	// ：实名认证通过前不得将资格置为合格。
	if err := ensurePlayerQualificationAllowed(ctx, db, id, esportsplayer.Qualification(in.Qualification)); err != nil {
		return nil, err
	}
	update := db.EsportsPlayer.Update().Where(esportsplayer.IDEQ(id)).
		SetDisplayName(in.DisplayName).
		SetQualification(esportsplayer.Qualification(in.Qualification)).
		SetPresence(esportsplayer.Presence(in.Presence)).
		SetDailyLimit(in.DailyLimit).SetConcurrentLimit(in.ConcurrentLimit).
		SetGames(in.Games)
	// ：五项接单配置（未传字段不覆盖）。
	update, err := applyPlayerAcceptConfig(update, playerAcceptConfig{
		ServiceSlotStartMinute:   in.ServiceSlotStartMinute,
		ServiceSlotEndMinute:     in.ServiceSlotEndMinute,
		AutoOnline:               in.AutoOnline,
		AcceptPriceMinCents:      in.AcceptPriceMinCents,
		AcceptPriceMaxCents:      in.AcceptPriceMaxCents,
		FixedTeamEnabled:         in.FixedTeamEnabled,
		FixedTeamSlotStartMinute: in.FixedTeamSlotStartMinute,
		FixedTeamSlotEndMinute:   in.FixedTeamSlotEndMinute,
	})
	if err != nil {
		return nil, err
	}
	// ：管理端把陪玩置为在线时同样写入心跳基线，超时任务才能据此判断失联。
	if in.Presence == "online" {
		update.SetLastHeartbeatAt(time.Now().UTC())
	}
	// P1-01-02：客服把陪玩置为下线同样属于**人工下线**，下一次心跳不得自动顶回 online：
	// 除非本次显式同时开启 auto_online（视为有意配置自动上线），否则一并关闭该开关。
	if in.Presence == "offline" && (in.AutoOnline == nil || !*in.AutoOnline) {
		update.SetAutoOnline(false)
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

// listPlayers 列出陪玩档案，keyword 非空时同时匹配陪玩昵称与关联用户的用户名 / 昵称 / 手机号。
func listPlayers(ctx context.Context, db *ent.Client, keyword string, limit int) ([]*ent.EsportsPlayer, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsPlayer.Query()
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		userIDs, err := db.User.Query().Where(user.Or(
			user.UsernameContainsFold(keyword),
			user.NicknameContainsFold(keyword),
			user.NameContainsFold(keyword),
			user.EmailContainsFold(keyword),
			user.PhoneContainsFold(keyword),
		)).IDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(userIDs) > 0 {
			q.Where(esportsplayer.Or(
				esportsplayer.DisplayNameContainsFold(keyword),
				esportsplayer.UserIDIn(userIDs...),
			))
		} else {
			q.Where(esportsplayer.DisplayNameContainsFold(keyword))
		}
	}
	return q.Order(ent.Desc(esportsplayer.FieldID)).Limit(limit).All(ctx)
}

// adminPlayerView 是管理端陪玩列表项：在档案字段外补充关联用户的可读信息，
// 避免界面只显示 user_id。
type adminPlayerView struct {
	*ent.EsportsPlayer
	UserDisplayName string `json:"user_display_name"`
	UserUsername    string `json:"user_username"`
	UserPhone       string `json:"user_phone"`
	UserEmail       string `json:"user_email"`
}

// playerUserLabel 取用户最可读的名称：昵称 → 姓名 → 用户名 → 邮箱 → 手机号。
func playerUserLabel(account *ent.User) string {
	for _, value := range []string{account.Nickname, account.Name, account.Username, account.Email, account.Phone} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// playerViewsWithUsers 批量补齐陪玩档案关联的用户名称 / 用户名 / 手机号 / 邮箱。
func playerViewsWithUsers(ctx context.Context, db *ent.Client, players []*ent.EsportsPlayer) ([]*adminPlayerView, error) {
	userIDs := make([]int, 0, len(players))
	for _, player := range players {
		userIDs = append(userIDs, player.UserID)
	}
	byID := make(map[int]*ent.User, len(userIDs))
	if len(userIDs) > 0 {
		accounts, err := db.User.Query().Where(user.IDIn(userIDs...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			byID[account.ID] = account
		}
	}
	views := make([]*adminPlayerView, 0, len(players))
	for _, player := range players {
		view := &adminPlayerView{EsportsPlayer: player}
		if account, ok := byID[player.UserID]; ok {
			view.UserDisplayName = playerUserLabel(account)
			view.UserUsername = account.Username
			view.UserPhone = account.Phone
			view.UserEmail = account.Email
		}
		views = append(views, view)
	}
	return views, nil
}

// playerUserCandidate 是「新建陪玩」时可选择的用户账号：已存在陪玩档案的用户会被排除。
type playerUserCandidate struct {
	ID          int    `json:"id"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
}

// listPlayerUserCandidates 按关键词搜索尚未建陪玩档案的用户账号。
func listPlayerUserCandidates(ctx context.Context, db *ent.Client, keyword string, limit int) ([]*playerUserCandidate, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	existing, err := db.EsportsPlayer.Query().Select(esportsplayer.FieldUserID).All(ctx)
	if err != nil {
		return nil, err
	}
	excluded := make([]int, 0, len(existing))
	for _, player := range existing {
		excluded = append(excluded, player.UserID)
	}
	q := db.User.Query()
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q.Where(user.Or(
			user.UsernameContainsFold(keyword),
			user.NicknameContainsFold(keyword),
			user.NameContainsFold(keyword),
			user.EmailContainsFold(keyword),
			user.PhoneContainsFold(keyword),
		))
	}
	if len(excluded) > 0 {
		q.Where(user.IDNotIn(excluded...))
	}
	accounts, err := q.Order(ent.Desc(user.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]*playerUserCandidate, 0, len(accounts))
	for _, account := range accounts {
		candidates = append(candidates, &playerUserCandidate{
			ID:          account.ID,
			DisplayName: playerUserLabel(account),
			Username:    account.Username,
			Email:       account.Email,
			Phone:       account.Phone,
		})
	}
	return candidates, nil
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
	// Bookable 表示当前时刻是否真正可接单：在线连接健康 ∩ 服务时段 ∩ 固排时段 ∩
	// 并发 / 每日接单容量。客户端据此决定是否展示「立即下单」，
	// 不能再用 Online 代替（在线 ≠ 可接单）。
	Bookable bool `json:"bookable"`
}

// listPublicPlayers 陪玩推荐（AuthPublic）：仅返回合格且在线的陪玩；game 非空时按游戏分区过滤，
// games 为空表示不限分区（缺省口径）。
//
// 保留既有签名（其余调用方与既有回归测试沿用）；筛选逻辑统一收敛到 listPublicPlayersByProps，
// 由 publicPlayers 按装修 props 传入 game / level / online / bookable。
func listPublicPlayers(ctx context.Context, db *ent.Client, game string, limit int) ([]publicPlayerView, error) {
	return listPublicPlayersByProps(ctx, db, playerRecommendProps{Game: game, Limit: limit}, time.Now())
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
//
// P1-01-02：**人工下线必须不被下一次心跳自动改回 online**。自动上线（auto_online）只对
// 「因心跳超时被动下线」的陪玩生效，因此本人手动下线时一并关闭 auto_online 开关：
// 开关关闭后心跳只刷新时戳，不会再把人顶回 online；需要自动上线时由本人在接单配置里重新开启。
func setPlayerPresence(ctx context.Context, db *ent.Client, playerID int, online bool) (*ent.EsportsPlayer, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	presence := esportsplayer.PresenceOffline
	if online {
		presence = esportsplayer.PresenceOnline
	}
	update := db.EsportsPlayer.Update().Where(esportsplayer.IDEQ(playerID)).SetPresence(presence)
	// ：上线即写入心跳基线，客户端失联后由超时任务接管自动离线。
	if online {
		update.SetLastHeartbeatAt(time.Now().UTC())
	} else {
		// P1-01-02：人工下线同时关闭自动上线，避免下一次心跳把人工下线的陪玩改回在线。
		update.SetAutoOnline(false)
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

// playerPresenceLabel 把 presence 枚举翻译成可直接展示的中文状态文案（P1-01）。
//
// 只有 offline 才叫「已下线」；心跳超时且存在在途订单的陪玩落 protected_abnormal，
// 语义是「连接异常（受保护）」——他仍在履约，前端不得展示成已下线。
func playerPresenceLabel(presence esportsplayer.Presence) string {
	switch presence {
	case esportsplayer.PresenceOnline:
		return "在线"
	case esportsplayer.PresenceProtectedAbnormal:
		return "连接异常（受保护）"
	default:
		return "已下线"
	}
}

type playerDashboardView struct {
	Presence string `json:"presence"`
	// PresenceLabel 是 presence 的可直接展示文案（在线 / 已下线 / 连接异常（受保护）），
	// 前端应以此字段渲染状态，不要用 online 布尔值自行拼「已下线」。
	PresenceLabel     string `json:"presence_label"`
	Online            bool   `json:"online"`
	PendingInvites    int    `json:"pending_invites"`
	ActiveOrders      int    `json:"active_orders"`
	TodayCatfoodCents int64  `json:"today_catfood_cents"`
	// 可提现猫粮余额（收入状态：待结算 / 已结算可提现 / 已提现）。
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
// ponytail: 收入口径简化（未接入 分成），当日口径用 UTC 自然日。
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
		PresenceLabel:       playerPresenceLabel(player.Presence),
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
	SkuID             int       `json:"product_id"`
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
	// 提前查订单：接受时校验分区 / 等级，拒绝时用于回退判断。
	order, err := tx.EsportsOrder.Get(ctx, seat.OrderID)
	if err != nil {
		return err
	}
	if err := ensureOrderNotFrozen(order); err != nil {
		return err
	}
	if accept {
		if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
			order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingAcceptance {
			return errConflict
		}
		// ：接受邀请必须复核资质与在线状态 —— 邀请发出后陪玩可能已被停资格、
		// 自动离线或转保护异常，此时不得再让其占位。
		if player.Qualification != esportsplayer.QualificationQualified ||
			player.Presence == esportsplayer.PresenceProtectedAbnormal ||
			player.Presence == esportsplayer.PresenceOffline {
			return errForbidden
		}
		// 本席位在邀请时已预占容量；接受时排除自身，并串行检查其他订单的占位。
		if err = reservePlayerDispatchCapacity(ctx, tx, player, order, now, seat.ID); err != nil {
			return err
		}
		game, gErr := orderGameIn(ctx, tx, order.SkuID)
		if gErr != nil {
			return gErr
		}
		if err = checkPlayerGameLevel(skillLevels(ctx, tx.Client()), player, game, order.RequiredLevel); err != nil {
			return err
		}
	}
	update := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.IDEQ(seatID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusInvited),
	).SetStatus(target).SetRespondedAt(now.UTC()).AddVersion(1)
	if accept {
		rule, ruleErr := resolveCommissionRuleForPlayerIn(ctx, tx.EsportsCommissionRule,
			tx.EsportsSku, tx.EsportsProductCategory, order, playerID, player.Level, now)
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
		// 只有仍待回应的 invited 与可补位的 open 席位算「未接满」；
		// rejected/expired/revoked 已是无效席位，不应把多席订单永久卡在待接单。
		notAllAccepted, err := tx.EsportsDispatchSeat.Query().Where(
			esportsdispatchseat.OrderIDEQ(seat.OrderID),
			esportsdispatchseat.StatusIn(
				esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusOpen),
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
		// ：首次有效接单的时间点（只写一次）。
		if _, err = markOrderTimelineIn(ctx, tx, seat.OrderID, timelineFirstAccepted, now); err != nil {
			return err
		}
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(seat.OrderID).
			SetOrderVersion(newVersion).SetEventType("order.seat_accepted").
			SetActorType("player").SetActorID(actorID).
			SetDetails(map[string]interface{}{"seat_index": seat.SeatIndex}).Save(ctx); err != nil {
			return err
		}
		// ：全部席位就绪 = 「可以开工」，写独立事件与 ready_to_start_at（只写一次）。
		if _, err = markOrderReadyToStartIn(ctx, tx, seat.OrderID, newVersion, now); err != nil {
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
	// 退回待派单：优先通知已开过窗时直接释放到大厅，否则按名单重新开窗（业务口径）。
	if targetStatus == esportsorder.FulfillmentStatusPendingDispatch {
		if err = onOrderEnteredPendingDispatch(ctx, tx, seat.OrderID, now); err != nil {
			return err
		}
	}
	eventType := "order.seat_rejected"
	details := map[string]interface{}{"seat_index": seat.SeatIndex}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(seat.OrderID).
		SetOrderVersion(newVersion).SetEventType(eventType).
		SetActorType("player").SetActorID(actorID).
		SetDetails(details).Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// listGrabHall 抢单大厅：可抢的 grabbing 单席位订单，外加「优先通知中且本人被通知」的待派单单席位订单。
// 优先通知窗口内的订单对未被通知的陪玩**不可见**（名单内独占）；窗口超时、名单内全部拒绝或客服提前
// 释放后订单转 grabbing，才对所有人开放（业务口径）。
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
		if err = checkPlayerGameLevel(skillLevels(ctx, tx.Client()), player, game, order.RequiredLevel); err != nil {
			if errors.Is(err, errForbidden) {
				continue
			}
			return nil, err
		}
		visible = append(visible, order)
	}
	return visible, nil
}

// playerGrabHallItem 是抢单大厅条目（P1-06）：**专用最小信息视图**，
// 不再是完整订单实体。只暴露「可见即可抢」所必需的字段：订单号、商品/规格名、游戏分区、
// 席位、档期与时长、单价与总价、等级要求与优先通知截止时间。
//
// 显式不返回（超出最小可见边界）：customer_id（抢单不需要知道客户是谁）、客户手机号、
// 分成 / 提成字段（commission_* / cs_commission_* / manual_commission_*）、渠道成本
// （channel_cost_cents）、退款与调整金额（refund_* / discount_cents / manual_adjust_cents）、
// 会员等级与报单人（responsible_admin_id / created_by_admin_id）。
// 为兼容既有客户端类型定义，customer_id / addon_cents / addon_units 仍保留键位但恒为 0
// 并用 omitempty 从 JSON 中省略（见报告「被脱敏字段」清单）。
type playerGrabHallItem struct {
	ID       int    `json:"id"`
	OrderNo  string `json:"order_no"`
	SkuID    int    `json:"product_id"`
	SkuCode  string `json:"product_code"`
	SkuName  string `json:"product_name"`
	SpecName string `json:"spec_name"`
	// Game 由「SKU → 商品分类」解析出的游戏分区；解析不到时为空串（不得用 priority_game 顶替）。
	Game string `json:"game"`
	// PriorityGame 是优先通知窗口开窗时的分区快照（未开过窗为空串）。
	PriorityGame string `json:"priority_game"`
	// 席位与档期。
	Quantity               int64      `json:"quantity"`
	RequiredSeats          int        `json:"required_seats"`
	PromisedServiceUnits   int64      `json:"promised_service_units"`
	PerSeatServiceUnits    int64      `json:"per_seat_service_units"`
	ServiceDurationMinutes int        `json:"service_duration_minutes"`
	RequiredLevel          string     `json:"required_level"`
	AppointmentStatus      string     `json:"appointment_status"`
	RequestedStartAt       *time.Time `json:"requested_start_at"`
	// 价格口径：只给单价与订单总价（单席位订单即该单价格），不含折扣 / 调整 / 退款明细。
	UnitPriceCents int64 `json:"unit_price_cents"`
	PayableCents   int64 `json:"payable_cents"`
	// 状态口径：抢单大厅只列已付款可抢的订单，状态字段仅供客户端展示。
	FulfillmentStatus    string     `json:"fulfillment_status"`
	PaymentStatus        string     `json:"payment_status"`
	PriorityNoticeStatus string     `json:"priority_notice_status"`
	PriorityNoticeUntil  *time.Time `json:"priority_notice_until"`
	CreatedAt            time.Time  `json:"created_at"`
	// 以下字段为「键位兼容但已脱敏」：恒为 0 且从 JSON 省略。
	CustomerID int   `json:"customer_id,omitempty"`
	AddonCents int64 `json:"addon_cents,omitempty"`
	AddonUnits int64 `json:"addon_units,omitempty"`
}

// listGrabHallItems 组装抢单大厅响应（P1-06 专用最小视图）：每张可抢单只带抢单必需字段。
// 游戏分区口径与 checkPlayerGameLevel 的调用方完全一致（orderGame：SKU → 分类 → game）；
// 按 sku_id 缓存解析结果，避免同一 SKU 的订单重复查询。
func listGrabHallItems(ctx context.Context, db *ent.Client, playerID int, now time.Time) ([]playerGrabHallItem, error) {
	orders, err := listGrabHall(ctx, db, playerID, now)
	if err != nil {
		return nil, err
	}
	items := make([]playerGrabHallItem, 0, len(orders))
	gameCache := make(map[int]string, len(orders))
	for _, order := range orders {
		game, ok := gameCache[order.SkuID]
		if !ok {
			if game, err = orderGame(ctx, db, order.SkuID); err != nil {
				return nil, err
			}
			gameCache[order.SkuID] = game
		}
		items = append(items, playerGrabHallItem{
			ID: order.ID, OrderNo: order.OrderNo,
			SkuID: order.SkuID, SkuCode: order.SkuCode, SkuName: order.SkuName,
			SpecName: order.SpecName, Game: game, PriorityGame: order.PriorityGame,
			Quantity: order.Quantity, RequiredSeats: order.RequiredSeats,
			PromisedServiceUnits:   order.PromisedServiceUnits,
			PerSeatServiceUnits:    order.PerSeatServiceUnits,
			ServiceDurationMinutes: order.ServiceDurationMinutes,
			RequiredLevel:          order.RequiredLevel,
			AppointmentStatus:      order.AppointmentStatus.String(),
			RequestedStartAt:       order.RequestedStartAt,
			UnitPriceCents:         order.UnitPriceCents, PayableCents: order.PayableCents,
			FulfillmentStatus:    order.FulfillmentStatus.String(),
			PaymentStatus:        order.PaymentStatus.String(),
			PriorityNoticeStatus: order.PriorityNoticeStatus.String(),
			PriorityNoticeUntil:  order.PriorityNoticeUntil,
			CreatedAt:            order.CreatedAt,
		})
	}
	return items, nil
}

// grabPlayerOrder 抢单：合格且「可抢新单」的陪玩抢下 grabbing 单席位订单的唯一 open 席位。
// 可抢新单 = 非 protected_abnormal 且（online 或 心跳新鲜）：人工下线但连接正常仍可抢，
// 自动离线（心跳超时）与连接异常（protected_abnormal）禁止抢新单。
//
// ：抢单成功写 order.seat_accepted 通知外盒（经 order.* 收件人解析定向通知客户与责任客服）；
// 落败（席位/订单已被他人抢下）时在事务回滚后补一条 order.grab_attempt_lost 定向通知本人，
// 使「谁抢过本单」可查。
func grabPlayerOrder(ctx context.Context, db *ent.Client, playerID, orderID int, now time.Time) (*ent.EsportsOrder, error) {
	order, err := grabPlayerOrderTx(ctx, db, playerID, orderID, now)
	if errors.Is(err, errConflict) && order != nil {
		recordGrabAttemptLost(ctx, db, order, playerID)
	}
	return order, err
}

// recordGrabAttemptLost 记录一次落败的抢单尝试。event_key 按 (订单号, 陪玩) 去重，
// 同一人对同一单只留一条痕迹；写失败只记日志，不影响抢单主流程。
func recordGrabAttemptLost(ctx context.Context, db *ent.Client, order *ent.EsportsOrder, playerID int) {
	if order == nil || playerID < 1 {
		return
	}
	if _, err := db.EsportsOutbox.Create().
		SetEventKey("order.grab_attempt_lost:" + order.OrderNo + ":" + strconv.Itoa(playerID)).
		SetEventType("order.grab_attempt_lost").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo, "player_id": playerID}).
		Save(ctx); err != nil && !ent.IsConstraintError(err) {
		log.Printf("[esports][dispatch] 落败抢单留痕失败 order=%s player=%d err=%v", order.OrderNo, playerID, err)
	}
}

// grabPlayerOrderTx 是抢单的事务主体。返回已读取的订单，便于外层在冲突时补写落败留痕。
func grabPlayerOrderTx(ctx context.Context, db *ent.Client, playerID, orderID int, now time.Time) (*ent.EsportsOrder, error) {
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
		return order, errConflict
	}
	game, err := orderGameIn(ctx, tx, order.SkuID)
	if err != nil {
		return nil, err
	}
	if err = checkPlayerGameLevel(skillLevels(ctx, tx.Client()), player, game, order.RequiredLevel); err != nil {
		return nil, err
	}
	rule, err := resolveCommissionRuleForPlayerIn(ctx, tx.EsportsCommissionRule,
		tx.EsportsSku, tx.EsportsProductCategory, order, playerID, player.Level, now)
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
			return order, errDispatchPriorityHold
		}
	case order.FulfillmentStatus == esportsorder.FulfillmentStatusGrabbing:
	default:
		return order, errConflict
	}
	if err = ensureDispatchSeats(ctx, tx, order); err != nil {
		return nil, err
	}
	update := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		// 单席订单可能只剩被回收的席位（rejected/expired/revoked），抢单时一并接管，
		// 避免释放到大厅后仍因没有 open 席位而抢不了。
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusOpen, esportsdispatchseat.StatusRejected,
			esportsdispatchseat.StatusExpired, esportsdispatchseat.StatusRevoked,
		),
	).SetPlayerID(playerID).SetStatus(esportsdispatchseat.StatusAccepted).
		SetSource(esportsdispatchseat.SourceGrab).SetRespondedAt(now.UTC()).
		SetCommissionRuleLockedAt(now.UTC()).ClearExpiresAt().AddVersion(1)
	// ：抢单不是「发出邀请」，不写 invited_at；但同样按订单档期落本席服务时段。
	update = applySeatServiceSlot(update, order)
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
		return order, errConflict
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
		return order, errConflict
	}
	// ：首次有效接单的时间点（只写一次）。
	if _, err = markOrderTimelineIn(ctx, tx, orderID, timelineFirstAccepted, now); err != nil {
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
	// ：单席抢单成功即全部席位就绪，「可以开工」写独立事件与 ready_to_start_at（只写一次）。
	if _, err = markOrderReadyToStartIn(ctx, tx, orderID, eventVersion, now); err != nil {
		return nil, err
	}
	// ：抢单成功写通知外盒，经 order.* 收件人解析定向通知客户与责任客服。
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.seat_accepted:" + order.OrderNo + ":" + strconv.Itoa(playerID)).
		SetEventType("order.seat_accepted").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "player_id": playerID, "seat_index": 1,
			"source": "grab", "from_priority_notice": fromNotice,
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
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
	// ：开工（首个成员开始服务）的时间点（只写一次）。
	if _, err = markOrderTimelineIn(ctx, tx, orderID, timelineServiceStarted, now); err != nil {
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

// adminStoppedSeatIndexes 从只增事件派生「当前处于客服记名停工状态」的席位集合。
//
// 口径：按事件 ID 顺序重放 order.service_paused / order.service_resumed，
// 只有 actor_type=admin 的暂停才把席位标记为「记名停工」；任何一次恢复都会清掉标记。
// 陪玩本人暂停不会被计为记名停工，因此本人暂停不会触发整单转待结单。
func adminStoppedSeatIndexes(ctx context.Context, db *ent.Client, orderID int) (map[int]bool, error) {
	events, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID),
		esportsorderevent.EventTypeIn("order.service_paused", "order.service_resumed"),
	).Order(ent.Asc(esportsorderevent.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	stopped := map[int]bool{}
	for _, event := range events {
		seat := seatIndexOf(event.Details)
		if seat < 1 {
			continue
		}
		if event.EventType == "order.service_paused" && event.ActorType == "admin" {
			stopped[seat] = true
			continue
		}
		if event.EventType == "order.service_resumed" {
			stopped[seat] = false
		}
	}
	return stopped, nil
}

// transitionToPendingCloseOnAdminStop 客服记名停工后判定整单是否进入待结单。
//
// 口径（可验收断言）：
//   - 仅当订单仍在 in_service 时评估；
//   - 订单还有任一 in_service 服务段 → 不转（还有人真的在服务）；
//   - 存在「从未开工」的席位（无服务段且仍占位）→ 不转（P1-02：该席位须先被终止 / 移除）；
//   - 所有未结束的服务段都处于「客服记名停工」（actor=admin 的暂停且未恢复）→ 整单转入
//     pending_close，并把剩余履约量（承诺量 + 加钟量 − 已上报量）登记进只增事件
//     order.pending_close_entered，供结单 / 改派 / 售后取用；
//   - 陪玩本人暂停不算记名停工，不会误触发。
//
// 返回是否发生了整单转待结单。
func transitionToPendingCloseOnAdminStop(ctx context.Context, tx *ent.Tx, orderID, expectedVersion, adminID int, now time.Time) (bool, error) {
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return false, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		return false, nil
	}
	// P1-02：存在「从未开工」的席位时不得整单转待结单——该席位还没有任何服务段，
	// 先由客服终止 / 移除它，再收敛订单状态；否则一张有人从未上场的订单会被结单。
	if unstarted, err := orderUnstartedSeatIndexes(ctx, tx, orderID); err != nil {
		return false, err
	} else if len(unstarted) > 0 {
		return false, nil
	}
	segments, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).All(ctx)
	if err != nil {
		return false, err
	}
	for _, segment := range segments {
		if segment.Status == esportsservicesegment.StatusInService {
			return false, nil
		}
	}
	stopped, err := adminStoppedSeatIndexes(ctx, tx.Client(), orderID)
	if err != nil {
		return false, err
	}
	stoppedSeats := make([]int, 0, len(stopped))
	for _, segment := range segments {
		if segment.Status == esportsservicesegment.StatusFinished {
			continue
		}
		if !stopped[segment.SeatIndex] {
			return false, nil
		}
	}
	for seat, isStopped := range stopped {
		if isStopped {
			stoppedSeats = append(stoppedSeats, seat)
		}
	}
	summary := summarizeOrderFulfillment(order, segments)
	newVersion, err := bumpOrder(ctx, tx, orderID, expectedVersion, esportsorder.FulfillmentStatusPendingClose)
	if err != nil {
		return false, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.pending_close_entered").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason":                 "admin_service_stop",
			"stopped_seat_indexes":   stoppedSeats,
			"promised_service_units": summary.PromisedUnits,
			"addon_units":            summary.AddonUnits,
			"served_units":           summary.ServedUnits,
			"unfulfilled_units":      summary.UnfulfilledUnits,
		}).Save(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// seatDisposed 判定席位是否已被移除（不再占位、也不会再开工）。
// 只有 revoked / rejected / expired 三个终态算「已处置」，open / invited / accepted 仍在占位。
func seatDisposed(status esportsdispatchseat.Status) bool {
	switch status {
	case esportsdispatchseat.StatusRevoked, esportsdispatchseat.StatusRejected,
		esportsdispatchseat.StatusExpired:
		return true
	}
	return false
}

// orderSeatSegmentMap 读取订单的全部在册席位，并按席位序号归集服务段（P1-02 判定共用）。
func orderSeatSegmentMap(ctx context.Context, tx *ent.Tx, orderID int) (
	[]*ent.EsportsDispatchSeat, map[int][]*ent.EsportsServiceSegment, error) {
	seats, err := tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(seats) == 0 {
		return seats, nil, nil
	}
	segments, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	bySeat := make(map[int][]*ent.EsportsServiceSegment, len(segments))
	for _, segment := range segments {
		bySeat[segment.SeatIndex] = append(bySeat[segment.SeatIndex], segment)
	}
	return seats, bySeat, nil
}

// orderUnstartedSeatIndexes 返回「从未开工」的席位序号：该席位没有任何服务段，且仍占着位
// （open / invited / accepted）。已移除（revoked / rejected / expired）的席位算已处置，不在其中。
//
// P1-02：这类席位必须有明确处置（终止或移除）后才能让整单进入待结单/结单口径收敛。
func orderUnstartedSeatIndexes(ctx context.Context, tx *ent.Tx, orderID int) ([]int, error) {
	seats, bySeat, err := orderSeatSegmentMap(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	unstarted := make([]int, 0)
	for _, seat := range seats {
		if len(bySeat[seat.SeatIndex]) > 0 {
			continue
		}
		if !seatDisposed(seat.Status) {
			unstarted = append(unstarted, seat.SeatIndex)
		}
	}
	return unstarted, nil
}

// orderAllSeatsServiceSettled 判定订单全部在册席位是否都已了结（P1-02）。
//
// 口径（可验收断言）：
//   - 每个在册席位都要有明确结论：该席位的服务段全部进入终态（finished），或该席位已被移除
//     （revoked / rejected / expired）；
//   - 任一席位既没有服务段、也仍在 open / invited / accepted（**从未开工**）→ 未了结，
//     订单不得转待结单。此前用「finishedCount >= seatCount」判定，有席位从未开工时
//     永远凑不满数，订单会永久卡在服务中；
//   - 没有席位（历史单 / 异常数据）时不做判定，保持既有「不转待结单」行为。
//
// 未了结时第二个返回值列出未了结的席位序号，供事件与客服提示使用。
func orderAllSeatsServiceSettled(ctx context.Context, tx *ent.Tx, orderID int) (bool, []int, error) {
	seats, bySeat, err := orderSeatSegmentMap(ctx, tx, orderID)
	if err != nil {
		return false, nil, err
	}
	if len(seats) == 0 {
		return false, nil, nil
	}
	pending := make([]int, 0)
	for _, seat := range seats {
		seatSegments := bySeat[seat.SeatIndex]
		if len(seatSegments) == 0 {
			if !seatDisposed(seat.Status) {
				pending = append(pending, seat.SeatIndex)
			}
			continue
		}
		for _, segment := range seatSegments {
			if segment.Status != esportsservicesegment.StatusFinished {
				pending = append(pending, seat.SeatIndex)
				break
			}
		}
	}
	if len(pending) > 0 {
		return false, pending, nil
	}
	return true, nil, nil
}

// finishPlayerService 结束服务：分别保存计费服务量、陪玩报告的服务秒数与系统有效秒数。
// 差异过大时写客服核实事件，全部在册席位都了结后订单进入 pending_close。
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
	// P1-02：只有「全部在册席位都了结」才允许转待结单：该席位服务段已结束，或席位已被移除；
	// 仍存在从未开工的席位（无服务段且仍占位）时保持服务中，等客服终止 / 移除该席位后再收敛。
	settled, pendingSeats, err := orderAllSeatsServiceSettled(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	target := esportsorder.FulfillmentStatus("")
	if settled {
		target = esportsorder.FulfillmentStatusPendingClose
	}
	newVersion, err := bumpOrder(ctx, tx, orderID, order.Version, target)
	if err != nil {
		return nil, err
	}
	durationMismatch := serviceDurationMismatch(systemSeconds, reportedSeconds,
		serviceMismatchMinimumSeconds(ctx, tx.Client()))
	details := map[string]interface{}{
		"seat_index": segment.SeatIndex, "reported_units": reportedUnits,
		"reported_seconds": reportedSeconds, "system_seconds": systemSeconds,
		"duration_mismatch": durationMismatch,
	}
	if len(pendingSeats) > 0 {
		// 未了结席位明细进事件：客服据此定位「哪个席位从未开工」，走终止 / 移除处置。
		details["unsettled_seat_indexes"] = pendingSeats
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(newVersion).SetEventType("order.service_finished").
		SetActorType("player").SetActorID(strconv.Itoa(playerID)).
		SetDetails(details).Save(ctx); err != nil {
		return nil, err
	}
	// ：服务段结束即进入待结算，写一条 settlement.* 生产事件定向通知该陪玩，
	// 让「服务完成 → 待结算」在陪玩端消息中心可见（结算周期单由结算模块按周期生成）。
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("settlement.segment_finished:" + strconv.Itoa(segment.ID)).
		SetEventType("settlement.segment_finished").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "player_id": playerID,
			"seat_index": segment.SeatIndex, "system_seconds": systemSeconds,
			"reported_units": reportedUnits, "reported_seconds": reportedSeconds,
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return nil, err
		}
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
	// ：差异超过严重阈值时自动建立售后单（channel=system，来源陪玩为该成员），
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
