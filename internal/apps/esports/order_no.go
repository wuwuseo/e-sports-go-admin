package esports

import (
	"context"
	"fmt"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
)

// 管理端统一以订单号（order_no，全局唯一）作为订单的业务标识：
// 筛选 / 表单输入一律收订单号，内部存储与寻址仍是订单 ID。

// orderIDByNo 按订单号解析订单 ID。空串返回 (0, nil)（表示未指定）；
// 订单不存在返回参数错误，错误文案含订单号便于排查。
func orderIDByNo(ctx context.Context, db *ent.Client, orderNo string) (int, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return 0, nil
	}
	order, err := db.EsportsOrder.Query().Where(esportsorder.OrderNoEQ(orderNo)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return 0, fmt.Errorf("%w：订单号 %s 不存在", errInvalidInput, orderNo)
		}
		return 0, err
	}
	return order.ID, nil
}

// orderNosOf 批量解析订单号（列表视图展示用）：返回 orderID → order_no，
// 查不到的 ID 不在结果里（展示侧回落显示 #ID）。
func orderNosOf(ctx context.Context, db *ent.Client, ids []int) (map[int]string, error) {
	uniq := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	out := make(map[int]string, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	orders, err := db.EsportsOrder.Query().
		Where(esportsorder.IDIn(uniq...)).
		Select(esportsorder.FieldID, esportsorder.FieldOrderNo).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, order := range orders {
		out[order.ID] = order.OrderNo
	}
	return out, nil
}

// ── 管理端列表视图：实体 + 订单号（订单列统一展示订单号，不再裸显示订单 ID） ──

// offlinePaymentProofView 待核验收款列表视图（客服工作台 / 收款管理共用）。
type offlinePaymentProofView struct {
	*ent.EsportsOfflinePaymentProof
	OrderNo string `json:"order_no"`
}

func offlinePaymentProofViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsOfflinePaymentProof) ([]offlinePaymentProofView, error) {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.OrderID)
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	views := make([]offlinePaymentProofView, 0, len(items))
	for _, item := range items {
		views = append(views, offlinePaymentProofView{EsportsOfflinePaymentProof: item, OrderNo: nos[item.OrderID]})
	}
	return views, nil
}

// paymentReversalView 渠道撤销 / 拒付列表视图。
type paymentReversalView struct {
	*ent.EsportsPaymentReversal
	OrderNo string `json:"order_no"`
}

func paymentReversalViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsPaymentReversal) ([]paymentReversalView, error) {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.OrderID)
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	views := make([]paymentReversalView, 0, len(items))
	for _, item := range items {
		views = append(views, paymentReversalView{EsportsPaymentReversal: item, OrderNo: nos[item.OrderID]})
	}
	return views, nil
}

// playerReviewAdminView 管理端评价列表视图。
type playerReviewAdminView struct {
	*ent.EsportsPlayerReview
	OrderNo string `json:"order_no"`
}

func playerReviewAdminViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsPlayerReview) ([]playerReviewAdminView, error) {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.OrderID)
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	views := make([]playerReviewAdminView, 0, len(items))
	for _, item := range items {
		views = append(views, playerReviewAdminView{EsportsPlayerReview: item, OrderNo: nos[item.OrderID]})
	}
	return views, nil
}

// fulfillmentMakeupView 售后补履约任务列表视图。
type fulfillmentMakeupView struct {
	*ent.EsportsFulfillmentMakeup
	OrderNo string `json:"order_no"`
}

func fulfillmentMakeupViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsFulfillmentMakeup) ([]fulfillmentMakeupView, error) {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.OrderID)
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	views := make([]fulfillmentMakeupView, 0, len(items))
	for _, item := range items {
		views = append(views, fulfillmentMakeupView{EsportsFulfillmentMakeup: item, OrderNo: nos[item.OrderID]})
	}
	return views, nil
}

// giftOrderView 礼物单列表视图（order_id 仅追溯，Nillable）。
type giftOrderView struct {
	*ent.EsportsGiftOrder
	OrderNo string `json:"order_no"`
}

func giftOrderViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsGiftOrder) ([]giftOrderView, error) {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		if item.OrderID != nil {
			ids = append(ids, *item.OrderID)
		}
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	views := make([]giftOrderView, 0, len(items))
	for _, item := range items {
		no := ""
		if item.OrderID != nil {
			no = nos[*item.OrderID]
		}
		views = append(views, giftOrderView{EsportsGiftOrder: item, OrderNo: no})
	}
	return views, nil
}

// seatTransferView 转单记录列表视图。
type seatTransferView struct {
	*ent.EsportsSeatTransferRequest
	OrderNo string `json:"order_no"`
}

// reviewAppealView 评价申诉列表视图（申诉实体自带 order_id 快照）。
type reviewAppealView struct {
	*ent.EsportsPlayerReviewAppeal
	OrderNo string `json:"order_no"`
}

func reviewAppealViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsPlayerReviewAppeal) ([]reviewAppealView, error) {
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.OrderID)
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	views := make([]reviewAppealView, 0, len(items))
	for _, item := range items {
		views = append(views, reviewAppealView{EsportsPlayerReviewAppeal: item, OrderNo: nos[item.OrderID]})
	}
	return views, nil
}
