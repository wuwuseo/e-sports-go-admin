package esports

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
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

// playerNamesOf 批量解析陪玩显示名（展示侧用）：返回 playerID → display_name，
// 查不到的 ID 不在结果里（展示侧回落显示 #ID）。
func playerNamesOf(ctx context.Context, db *ent.Client, ids []int) (map[int]string, error) {
	uniq := uniquePositiveIDs(ids)
	out := make(map[int]string, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	players, err := db.EsportsPlayer.Query().
		Where(esportsplayer.IDIn(uniq...)).
		Select(esportsplayer.FieldID, esportsplayer.FieldDisplayName).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, player := range players {
		out[player.ID] = player.DisplayName
	}
	return out, nil
}

// customerNamesOf 批量解析客户显示名，口径同 playerNamesOf。
func customerNamesOf(ctx context.Context, db *ent.Client, ids []int) (map[int]string, error) {
	uniq := uniquePositiveIDs(ids)
	out := make(map[int]string, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	customers, err := db.EsportsCustomer.Query().
		Where(esportscustomer.IDIn(uniq...)).
		Select(esportscustomer.FieldID, esportscustomer.FieldDisplayName).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, customer := range customers {
		out[customer.ID] = customer.DisplayName
	}
	return out, nil
}

// uniquePositiveIDs 去重并剔除非法 ID（<=0），供批量查询收窄 IN 列表。
func uniquePositiveIDs(ids []int) []int {
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
	return uniq
}

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
	// PlayerName 承接陪玩显示名；查不到时为空串，前端回落 #ID。
	PlayerName string `json:"player_name"`
}

func fulfillmentMakeupViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsFulfillmentMakeup) ([]fulfillmentMakeupView, error) {
	ids := make([]int, 0, len(items))
	playerIDs := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.OrderID)
		playerIDs = append(playerIDs, item.PlayerID)
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	names, err := playerNamesOf(ctx, db, playerIDs)
	if err != nil {
		return nil, err
	}
	views := make([]fulfillmentMakeupView, 0, len(items))
	for _, item := range items {
		views = append(views, fulfillmentMakeupView{
			EsportsFulfillmentMakeup: item,
			OrderNo:                  nos[item.OrderID],
			PlayerName:               names[item.PlayerID],
		})
	}
	return views, nil
}

// giftOrderView 礼物单列表视图（order_id 仅追溯，Nillable）。
type giftOrderView struct {
	*ent.EsportsGiftOrder
	OrderNo string `json:"order_no"`
	// CustomerName 客户显示名；查不到时为空串，前端回落 #ID。
	CustomerName string `json:"customer_name"`
}

func giftOrderViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsGiftOrder) ([]giftOrderView, error) {
	ids := make([]int, 0, len(items))
	customerIDs := make([]int, 0, len(items))
	for _, item := range items {
		if item.OrderID != nil {
			ids = append(ids, *item.OrderID)
		}
		customerIDs = append(customerIDs, item.CustomerID)
	}
	nos, err := orderNosOf(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	names, err := customerNamesOf(ctx, db, customerIDs)
	if err != nil {
		return nil, err
	}
	views := make([]giftOrderView, 0, len(items))
	for _, item := range items {
		no := ""
		if item.OrderID != nil {
			no = nos[*item.OrderID]
		}
		views = append(views, giftOrderView{
			EsportsGiftOrder: item,
			OrderNo:          no,
			CustomerName:     names[item.CustomerID],
		})
	}
	return views, nil
}

// adminGiftIntentView 管理端礼物确认单列表视图（补客户显示名，避免列表裸显示客户 ID）。
// 与 gift_intent.go 的 giftIntentView 区分：这里保留实体全字段，仅追加展示字段。
type adminGiftIntentView struct {
	*ent.EsportsGiftIntent
	CustomerName string `json:"customer_name"`
}

func adminGiftIntentViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsGiftIntent) ([]adminGiftIntentView, error) {
	customerIDs := make([]int, 0, len(items))
	for _, item := range items {
		customerIDs = append(customerIDs, item.CustomerID)
	}
	names, err := customerNamesOf(ctx, db, customerIDs)
	if err != nil {
		return nil, err
	}
	views := make([]adminGiftIntentView, 0, len(items))
	for _, item := range items {
		views = append(views, adminGiftIntentView{
			EsportsGiftIntent: item,
			CustomerName:      names[item.CustomerID],
		})
	}
	return views, nil
}

// adminOrderView 管理端订单列表视图（补客户显示名，避免列表裸显示客户 ID）。
type adminOrderView struct {
	*ent.EsportsOrder
	CustomerName string `json:"customer_name"`
}

func adminOrderViewsOf(ctx context.Context, db *ent.Client,
	items []*ent.EsportsOrder) ([]adminOrderView, error) {
	customerIDs := make([]int, 0, len(items))
	for _, item := range items {
		customerIDs = append(customerIDs, item.CustomerID)
	}
	names, err := customerNamesOf(ctx, db, customerIDs)
	if err != nil {
		return nil, err
	}
	views := make([]adminOrderView, 0, len(items))
	for _, item := range items {
		views = append(views, adminOrderView{EsportsOrder: item, CustomerName: names[item.CustomerID]})
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

// ── 订单号生成 ──

// 订单号格式：{前缀}{下单时间}{客户ID}{随机数字}，例如 ES2026112319123400012345678901。
// 前缀由后台系统配置控制；时间按业务时区（UTC+8）取到秒；随机段避免同一客户同一秒内撞号。
const (
	orderNoPrefixConfigKey = "app.esports.order_no_prefix"
	orderNoDefaultPrefix   = "ES"
	orderNoPrefixMaxLen    = 8
	orderNoRandomDigits    = 8
	orderNoTimeLayout      = "20060102150405"
)

// newOrderNo 生成可读订单号。order_no 有唯一索引，随机段用 crypto/rand，
// 同一客户同一秒内需要生成 1e8 个量级才可能碰撞，实际业务可忽略。
func newOrderNo(ctx context.Context, db *ent.Client, customerID int, now time.Time) string {
	return fmt.Sprintf("%s%s%d%0*d",
		orderNoPrefix(ctx, db),
		now.In(cstZone).Format(orderNoTimeLayout),
		customerID,
		orderNoRandomDigits,
		orderNoRandomSuffix(),
	)
}

// orderNoPrefix 读取并规整后台配置的前缀：只保留大写字母与数字，最长 8 位；
// 未配置或规整后为空时回落默认前缀，避免生成难以辨认的订单号。
func orderNoPrefix(ctx context.Context, db *ent.Client) string {
	raw := strings.ToUpper(strings.TrimSpace(systemConfigValue(ctx, db, orderNoPrefixConfigKey)))
	var b strings.Builder
	for _, r := range raw {
		if (r >= 0x41 && r <= 0x5A) || (r >= 0x30 && r <= 0x39) {
			b.WriteRune(r)
			if b.Len() >= orderNoPrefixMaxLen {
				break
			}
		}
	}
	if b.Len() == 0 {
		return orderNoDefaultPrefix
	}
	return b.String()
}

// orderNoRandomSuffix 返回定长随机数字段；熵源异常时退回纳秒，保证仍能生成。
func orderNoRandomSuffix() int {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return int(time.Now().UnixNano() % 100000000)
	}
	return int(binary.BigEndian.Uint32(buf[:]) % 100000000)
}
