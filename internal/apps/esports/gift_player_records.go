package esports

import (
	"context"
	"sort"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsgiftorder"
)

// 陪玩端收礼记录与本人榜单排名。
//
// 礼物收入此前只在陪玩分成明细（kind=gift）里可见，榜单只有管理端入口；
// 本查询给陪玩端补上「谁送了什么礼物、送了多少钱」的收礼明细，以及本人在收礼榜上的名次。

// playerGiftRecord 是一条收礼记录。
type playerGiftRecord struct {
	GiftNo      string    `json:"gift_no"`
	GiftName    string    `json:"gift_name"`
	SenderName  string    `json:"sender_name"`
	SenderID    int       `json:"sender_id"`
	AmountCents int64     `json:"amount_cents"`
	IncomeCents int64     `json:"income_cents"`
	Quantity    int       `json:"quantity"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// playerGiftRecordView 是陪玩端收礼概览：本人收礼记录 + 本人榜单名次。
type playerGiftRecordView struct {
	PlayerID    int                `json:"player_id"`
	PlayerName  string             `json:"player_name"`
	Period      string             `json:"period"`
	TotalCents  int64              `json:"total_cents"`
	TotalCount  int                `json:"total_count"`
	Rank        int                `json:"rank"`
	BoardSize   int                `json:"board_size"`
	RankInBoard bool               `json:"rank_in_board"`
	Records     []playerGiftRecord `json:"records"`
}

// listPlayerGiftRecords 查询当前登录陪玩的收礼记录与本人榜单排名。
// 只统计已支付且未全额退款的礼物（与榜单同口径）；period 支持 daily/weekly/monthly。
func listPlayerGiftRecords(ctx context.Context, db *ent.Client, uid, period string,
	now time.Time, limit int) (*playerGiftRecordView, error) {
	player, err := currentPlayer(ctx, db, uid)
	if err != nil {
		return nil, err
	}
	start, err := giftPeriodStart(period, now)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 20
	}
	orders, err := db.EsportsGiftOrder.Query().Where(
		esportsgiftorder.PlayerIDEQ(player.ID),
		esportsgiftorder.CreatedAtGTE(start),
		esportsgiftorder.RefundStatusNEQ(esportsgiftorder.RefundStatusFull),
	).Order(ent.Desc(esportsgiftorder.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	view := &playerGiftRecordView{
		PlayerID: player.ID, PlayerName: player.DisplayName, Period: period,
		Records: make([]playerGiftRecord, 0, len(orders)),
	}
	giftNames := map[int]string{}
	senderNames := map[int]string{}
	for _, order := range orders {
		name, ok := giftNames[order.GiftID]
		if !ok {
			if gift, err := db.EsportsGift.Get(ctx, order.GiftID); err == nil {
				name = gift.Name
			}
			giftNames[order.GiftID] = name
		}
		sender, ok := senderNames[order.CustomerID]
		if !ok {
			if customer, err := db.EsportsCustomer.Get(ctx, order.CustomerID); err == nil {
				sender = customer.DisplayName
			}
			senderNames[order.CustomerID] = sender
		}
		view.Records = append(view.Records, playerGiftRecord{
			GiftNo: order.GiftNo, GiftName: name, SenderName: sender, SenderID: order.CustomerID,
			AmountCents: order.AmountCents, IncomeCents: order.IncomeCents,
			Quantity: order.Quantity, Status: order.Status.String(), CreatedAt: order.CreatedAt,
		})
		view.TotalCents += order.AmountCents
		view.TotalCount++
	}
	// 本人榜单名次：与榜单同口径（金额倒序、同额按笔数倒序）。
	rankRows, err := giftLeaderboardAll(ctx, db, "player", period, now)
	if err != nil {
		return nil, err
	}
	view.BoardSize = len(rankRows)
	for i, row := range rankRows {
		if row.OwnerID == player.ID {
			view.Rank, view.RankInBoard = i+1, true
			break
		}
	}
	return view, nil
}

// giftLeaderboardAll 返回完整榜单（不截断），供「本人排名」使用。
func giftLeaderboardAll(ctx context.Context, db *ent.Client, board, period string, now time.Time) ([]giftLeaderboardRow, error) {
	rows, err := giftLeaderboard(ctx, db, board, period, now, 200)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].AmountCents != rows[j].AmountCents {
			return rows[i].AmountCents > rows[j].AmountCents
		}
		return rows[i].Count > rows[j].Count
	})
	return rows, nil
}
