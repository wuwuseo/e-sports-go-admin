package esports

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsprioritybookingintent"
)

// 会员优先预约：启用 priority_booking 等级的客户在自助端提交「陪玩 + 期望时段 + 备注」
// 的预约意向；客服工作台待办出现「会员优先预约」，并按会员等级置顶处理。意向与正式订单解耦，
// 客服确认后再走既有报单链路，不污染订单 / 排单 / 报表。

// priorityBookingInput 是自助端提交预约意向的入参。
type priorityBookingInput struct {
	PlayerID         int    `json:"player_id"`
	RequestedStartAt string `json:"requested_start_at"`
	Note             string `json:"note"`
}

// createPriorityBooking 客户自助提交会员优先预约意向：
//   - 仅当前等级启用 priority_booking 的客户可提交，否则返回 errForbidden（无权益不出现入口）；
//   - 陪玩须存在，期望时段须为未来时间，备注限 500 字；意向初始 status=pending。
func createPriorityBooking(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	in priorityBookingInput, now time.Time) (*ent.EsportsPriorityBookingIntent, error) {
	if customer == nil || customer.ID < 1 {
		return nil, errForbidden
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.PlayerID < 1 || len([]rune(in.Note)) > 500 {
		return nil, errInvalidInput
	}
	// 权益校验：仅当前等级启用 priority_booking 的客户可提交。
	rule, err := customerMemberRule(ctx, db, customer.MemberLevelCode)
	if err != nil {
		return nil, err
	}
	if rule == nil || !rule.PriorityBooking {
		return nil, fmt.Errorf("%w：当前会员等级未开通优先预约权益", errForbidden)
	}
	player, err := db.EsportsPlayer.Query().Where(esportsplayer.IDEQ(in.PlayerID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errInvalidInput
	}
	if err != nil {
		return nil, err
	}
	requestedAt, err := parseReportTime(in.RequestedStartAt)
	if err != nil || requestedAt.IsZero() || !requestedAt.After(now.UTC()) {
		return nil, fmt.Errorf("%w：期望时段须为未来时间", errInvalidInput)
	}
	return db.EsportsPriorityBookingIntent.Create().
		SetCustomerID(customer.ID).SetPlayerID(player.ID).SetPlayerName(player.DisplayName).
		SetRequestedStartAt(requestedAt).SetNote(in.Note).
		SetStatus(esportsprioritybookingintent.StatusPending).Save(ctx)
}

// ownedCustomerIDs 返回归属指定客服的客户 ID 列表（数据隔离口径）。
func ownedCustomerIDs(ctx context.Context, db *ent.Client, adminID int) ([]int, error) {
	rows, err := db.EsportsCustomer.Query().Where(
		esportscustomer.OwnerAdminIDEQ(adminID),
	).Select(esportscustomer.FieldID).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

// countPriorityBookingsPending 统计待处理预约意向数（客服待办）；非超管只统计归属本人客户。
func countPriorityBookingsPending(ctx context.Context, db *ent.Client, adminID int, isSuper bool) (int, error) {
	q := db.EsportsPriorityBookingIntent.Query().Where(
		esportsprioritybookingintent.StatusEQ(esportsprioritybookingintent.StatusPending),
	)
	if !isSuper {
		ids, err := ownedCustomerIDs(ctx, db, adminID)
		if err != nil {
			return 0, err
		}
		if len(ids) == 0 {
			return 0, nil
		}
		q.Where(esportsprioritybookingintent.CustomerIDIn(ids...))
	}
	return q.Count(ctx)
}

// priorityBookingView 是客服待办列表里的预约意向视图，附客户会员等级信息。
type priorityBookingView struct {
	ID               int        `json:"id"`
	CustomerID       int        `json:"customer_id"`
	CustomerName     string     `json:"customer_name"`
	PlayerID         int        `json:"player_id"`
	PlayerName       string     `json:"player_name"`
	RequestedStartAt *time.Time `json:"requested_start_at"`
	Note             string     `json:"note"`
	Status           string     `json:"status"`
	MemberLevelCode  string     `json:"member_level_code"`
	MemberLevelName  string     `json:"member_level_name"`
	LevelSortOrder   int        `json:"level_sort_order"`
	CreatedAt        time.Time  `json:"created_at"`
}

// listPriorityBookings 列出待处理预约意向并按会员等级置顶：
// 等级越高的客户意向越靠前（growth_threshold 降序），同等级按提交时间升序；
// 未定级 / 停用等级排最后。非超管只看归属本人客户的意向。
func listPriorityBookings(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	limit int) ([]*priorityBookingView, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsPriorityBookingIntent.Query().Where(
		esportsprioritybookingintent.StatusEQ(esportsprioritybookingintent.StatusPending),
	)
	if !isSuper {
		ids, err := ownedCustomerIDs(ctx, db, adminID)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []*priorityBookingView{}, nil
		}
		q.Where(esportsprioritybookingintent.CustomerIDIn(ids...))
	}
	intents, err := q.Order(ent.Asc(esportsprioritybookingintent.FieldCreatedAt)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	// 批量取客户与等级规则，计算各意向的等级排序权重。
	customerIDs := make([]int, 0, len(intents))
	for _, item := range intents {
		customerIDs = append(customerIDs, item.CustomerID)
	}
	customers, err := db.EsportsCustomer.Query().Where(
		esportscustomer.IDIn(customerIDs...),
	).Select(esportscustomer.FieldID, esportscustomer.FieldDisplayName, esportscustomer.FieldMemberLevelCode).All(ctx)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[int]string, len(customers))
	levelByID := make(map[int]string, len(customers))
	codes := make([]string, 0, len(customers))
	for _, customer := range customers {
		nameByID[customer.ID] = customer.DisplayName
		levelByID[customer.ID] = customer.MemberLevelCode
		if customer.MemberLevelCode != "" {
			codes = append(codes, customer.MemberLevelCode)
		}
	}
	type levelMeta struct {
		name      string
		threshold int64
		sortOrder int
	}
	metaByCode := make(map[string]levelMeta, len(codes))
	if len(codes) > 0 {
		rules, err := db.EsportsMemberLevelRule.Query().Where(
			esportsmemberlevelrule.CodeIn(codes...),
			esportsmemberlevelrule.StatusEQ(esportsmemberlevelrule.StatusEnabled),
		).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, rule := range rules {
			metaByCode[rule.Code] = levelMeta{name: rule.Name, threshold: rule.GrowthThreshold, sortOrder: rule.SortOrder}
		}
	}
	views := make([]*priorityBookingView, 0, len(intents))
	for _, item := range intents {
		code := levelByID[item.CustomerID]
		meta := metaByCode[code]
		views = append(views, &priorityBookingView{
			ID: item.ID, CustomerID: item.CustomerID, CustomerName: nameByID[item.CustomerID],
			PlayerID: item.PlayerID, PlayerName: item.PlayerName,
			RequestedStartAt: item.RequestedStartAt, Note: item.Note, Status: item.Status.String(),
			MemberLevelCode: code, MemberLevelName: meta.name,
			LevelSortOrder: meta.sortOrder, CreatedAt: item.CreatedAt,
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i], views[j]
		// 已启用等级的按门槛降序（等级越高越靠前）；未定级 / 停用等级排最后。
		aThreshold, aRanked := metaByCode[a.MemberLevelCode].threshold, metaByCode[a.MemberLevelCode] != levelMeta{}
		bThreshold, bRanked := metaByCode[b.MemberLevelCode].threshold, metaByCode[b.MemberLevelCode] != levelMeta{}
		if aRanked != bRanked {
			return aRanked
		}
		if aRanked && aThreshold != bThreshold {
			return aThreshold > bThreshold
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	return views, nil
}
