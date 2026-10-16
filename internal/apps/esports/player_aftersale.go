package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsservicesegment"
)

// playerAfterSaleKinds（§13.2）：陪玩端只开放「服务履约申诉」类问题，
// 退款口径与赔付金额由客服在售后方案中确定，陪玩不能直接指定金额。
var playerAfterSaleKinds = map[string]bool{
	"service_mismatch": true,
	"duration_short":   true,
	"no_show":          true,
	"wrong_item":       true,
	"overcharge":       true,
	"player_appeal":    true,
	"other":            true,
}

type playerAfterSaleInput struct {
	OrderID      int    `json:"order_id"`
	Kind         string `json:"kind"`
	Demand       string `json:"demand"`
	ExpectedPlan string `json:"expected_plan"`
	EvidenceRef  string `json:"evidence_ref"`
	Contact      string `json:"contact"`
}

// createPlayerOrderAfterSale 陪玩就本人服务的订单发起售后申诉（§13.2）：
// 仅该单服务成员可发起，落 channel=player 与来源陪玩；客服受理后走统一售后状态机。
func createPlayerOrderAfterSale(ctx context.Context, db *ent.Client, playerID int,
	in playerAfterSaleInput, now time.Time) (*ent.EsportsAfterSale, error) {
	in.Kind = strings.TrimSpace(in.Kind)
	if playerID < 1 || in.OrderID < 1 || !playerAfterSaleKinds[in.Kind] {
		return nil, errInvalidInput
	}
	onDuty, err := playerServesOrder(ctx, db, playerID, in.OrderID)
	if err != nil {
		return nil, err
	}
	if !onDuty {
		return nil, errForbidden
	}
	actor := playerID
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := createAfterSaleTx(ctx, tx, afterSaleInput{
		SourceType: "order", SourceID: in.OrderID, Kind: in.Kind, Channel: "player",
		Demand: in.Demand, ExpectedPlan: in.ExpectedPlan,
		EvidenceRef: in.EvidenceRef, Contact: in.Contact,
	}, afterSaleCreateOptions{
		ActorType: "player", ActorID: strconv.Itoa(playerID), PlayerID: &actor,
	})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, item.ID)
}

// playerServesOrder 判断陪玩是否为本单服务成员：有派单席位记录或已产生服务段即视为成员。
func playerServesOrder(ctx context.Context, db *ent.Client, playerID, orderID int) (bool, error) {
	seated, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID), esportsdispatchseat.PlayerIDEQ(playerID),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	if seated {
		return true, nil
	}
	return db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID), esportsservicesegment.PlayerIDEQ(playerID),
	).Exist(ctx)
}

// listPlayerOrderAfterSales 陪玩端查询本人相关的订单售后（本人发起或系统按本人服务段自动建单）。
func listPlayerOrderAfterSales(ctx context.Context, db *ent.Client, playerID, limit int) ([]*ent.EsportsAfterSale, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	return db.EsportsAfterSale.Query().Where(
		esportsaftersale.PlayerIDEQ(playerID),
	).Order(ent.Desc(esportsaftersale.FieldID)).Limit(limit).All(ctx)
}
