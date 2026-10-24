package esports

import (
	"context"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreview"
)

// orderMemberView 客户自助「订单服务成员」视图：已完成订单服务评价的前置数据。
type orderMemberView struct {
	PlayerID    int    `json:"player_id"`
	SeatIndex   int    `json:"seat_index"`
	DisplayName string `json:"display_name"`
	Reviewed    bool   `json:"reviewed"`
}

// listCustomerOrderMembers 取订单实际服务成员（陪玩 ID、席位、展示名与是否已评价），
// 仅限本人订单；无服务成员返回空切片（不是错误）。
func listCustomerOrderMembers(ctx context.Context, db *ent.Client,
	customerID, orderID int) ([]orderMemberView, error) {
	if customerID < 1 || orderID < 1 {
		return nil, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.CustomerID != customerID {
		return nil, errForbidden
	}
	members, err := loadOrderMembers(ctx, db.EsportsServiceSegment, db.EsportsDispatchSeat, orderID)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return []orderMemberView{}, nil
	}
	ids := make([]int, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.PlayerID)
	}
	players, err := db.EsportsPlayer.Query().Where(esportsplayer.IDIn(ids...)).
		Select(esportsplayer.FieldID, esportsplayer.FieldDisplayName).All(ctx)
	if err != nil {
		return nil, err
	}
	nameByID := make(map[int]string, len(players))
	for _, player := range players {
		nameByID[player.ID] = player.DisplayName
	}
	reviews, err := db.EsportsPlayerReview.Query().Where(
		esportsplayerreview.OrderIDEQ(orderID),
		esportsplayerreview.CustomerIDEQ(customerID),
	).Select(esportsplayerreview.FieldPlayerID).All(ctx)
	if err != nil {
		return nil, err
	}
	reviewed := make(map[int]bool, len(reviews))
	for _, review := range reviews {
		reviewed[review.PlayerID] = true
	}
	views := make([]orderMemberView, 0, len(members))
	for _, member := range members {
		views = append(views, orderMemberView{
			PlayerID:    member.PlayerID,
			SeatIndex:   member.SeatIndex,
			DisplayName: nameByID[member.PlayerID],
			Reviewed:    reviewed[member.PlayerID],
		})
	}
	return views, nil
}
