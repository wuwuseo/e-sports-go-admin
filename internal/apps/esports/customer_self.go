package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsmemberlevelrule"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/systemconfig"
)

// 客户自助端只读与发起接口：一律按「登录用户绑定的客户」做数据隔离，
// 订单 / 售后均校验归属，避免越权读到他人数据。

// customerOrderBucketStatuses 把客户端的分组桶映射为订单履约状态集合；空桶（all）不过滤。
func customerOrderBucketStatuses(bucket string) ([]esportsorder.FulfillmentStatus, error) {
	switch bucket {
	case "", "all":
		return nil, nil
	case "pending_payment":
		return []esportsorder.FulfillmentStatus{esportsorder.FulfillmentStatusPendingPayment}, nil
	case "pending_dispatch":
		return []esportsorder.FulfillmentStatus{
			esportsorder.FulfillmentStatusPendingDispatch,
			esportsorder.FulfillmentStatusPendingAcceptance,
			esportsorder.FulfillmentStatusGrabbing,
			esportsorder.FulfillmentStatusPendingCustomerConfirm,
		}, nil
	case "in_progress":
		return []esportsorder.FulfillmentStatus{
			esportsorder.FulfillmentStatusAccepted,
			esportsorder.FulfillmentStatusInService,
		}, nil
	case "pending_close":
		return []esportsorder.FulfillmentStatus{
			esportsorder.FulfillmentStatusPendingClose,
			esportsorder.FulfillmentStatusPendingCloseReview,
		}, nil
	case "completed":
		return []esportsorder.FulfillmentStatus{esportsorder.FulfillmentStatusCompleted}, nil
	case "cancelled":
		return []esportsorder.FulfillmentStatus{esportsorder.FulfillmentStatusCancelled}, nil
	default:
		return nil, errInvalidInput
	}
}

// listCustomerOrders 客户自助：我的订单（按桶过滤，按 ID 倒序）。
func listCustomerOrders(ctx context.Context, db *ent.Client, customerID int, bucket string,
	limit int) ([]*ent.EsportsOrder, error) {
	if customerID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	statuses, err := customerOrderBucketStatuses(bucket)
	if err != nil {
		return nil, err
	}
	q := db.EsportsOrder.Query().Where(esportsorder.CustomerIDEQ(customerID))
	if len(statuses) > 0 {
		q.Where(esportsorder.FulfillmentStatusIn(statuses...))
	}
	return q.Order(ent.Desc(esportsorder.FieldID)).Limit(limit).All(ctx)
}

// getCustomerOrder 客户自助：订单详情，非本人订单返回 403。
func getCustomerOrder(ctx context.Context, db *ent.Client, customerID, orderID int) (*ent.EsportsOrder, error) {
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
	return order, nil
}

// listCustomerAfterSales 客户自助：我的售后单（按 ID 倒序）。
func listCustomerAfterSales(ctx context.Context, db *ent.Client, customerID, limit int) ([]*ent.EsportsAfterSale, error) {
	if customerID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsAfterSale.Query().Where(
		esportsaftersale.CustomerIDEQ(customerID),
	).Order(ent.Desc(esportsaftersale.FieldID)).Limit(limit).All(ctx)
}

const customerAfterSaleWindowDaysKey = "app.esports.customer_aftersale_window_days"

func customerAfterSaleWindowDays(ctx context.Context, db *ent.Client) (int, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(customerAfterSaleWindowDaysKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return 30, nil
	}
	if err != nil {
		return 0, err
	}
	days, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || days < 0 || days > 365 {
		return 0, fmt.Errorf("%w：客户自助售后申请时限配置无效", errInvalidInput)
	}
	return days, nil // 0 = 不限时
}

// createCustomerAfterSale 客户自助发起售后：本人来源单据可发起，归属客服记为发起人。
// 不带凭证附件，等价于 createCustomerAfterSaleWithEvidence(ctx, db, customer, in, nil, now)。
func createCustomerAfterSale(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	in afterSaleInput, now time.Time) (*ent.EsportsAfterSale, error) {
	return createCustomerAfterSaleWithEvidence(ctx, db, customer, in, nil, now)
}

// createCustomerAfterSaleWithEvidence 客户自助发起售后：本人来源单据可发起，归属客服记为发起人。
// 订单来源沿用「终态订单 + 配置时限」的窗口；礼物 / 充值来源带自身单据语义，不做自助时限。
// 考核处罚来源属陪玩申诉，不由客户自助发起。
// evidenceAttachmentIDs 非空时校验凭证附件为本人上传（图片/文档私有附件），并把引用序列化为
// evidence_ref 的 JSON 数组落库；引用他人附件返回 errForbidden，非图片/非私有目录返回 errInvalidInput。
func createCustomerAfterSaleWithEvidence(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	in afterSaleInput, evidenceAttachmentIDs []int, now time.Time) (*ent.EsportsAfterSale, error) {
	if customer == nil || customer.ID < 1 || in.SourceID < 1 {
		return nil, errInvalidInput
	}
	if len(evidenceAttachmentIDs) > 0 {
		subject, err := customerUploadSubject(ctx, db, customer)
		if err != nil {
			return nil, err
		}
		if _, err := resolveAfterSaleEvidence(ctx, db, evidenceAttachmentIDs, subject); err != nil {
			return nil, err
		}
		ref, err := afterSaleEvidenceRef(evidenceAttachmentIDs)
		if err != nil {
			return nil, err
		}
		in.EvidenceRef = ref
	}
	if in.SourceType == "" {
		in.SourceType = "order"
	}
	if !validAfterSaleSourceType(in.SourceType) || in.SourceType == "assessment" {
		return nil, errInvalidInput
	}
	source, err := resolveAfterSaleSource(ctx, db, in.SourceType, in.SourceID)
	if err != nil {
		return nil, err
	}
	if source.CustomerID == nil || *source.CustomerID != customer.ID {
		return nil, errForbidden
	}
	if order := source.Order; order != nil &&
		(order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
			order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled) {
		days, err := customerAfterSaleWindowDays(ctx, db)
		if err != nil {
			return nil, err
		}
		anchor := order.CreatedAt
		if order.PaidAt != nil {
			anchor = *order.PaidAt
		}
		if order.ClosedAt != nil {
			anchor = *order.ClosedAt
		}
		if days > 0 && now.UTC().After(anchor.UTC().AddDate(0, 0, days)) {
			return nil, fmt.Errorf("%w：超过客户自助售后申请时限，请联系客服", errInvalidInput)
		}
	}
	operator := 0
	if source.Order != nil && source.Order.ResponsibleAdminID != nil {
		operator = *source.Order.ResponsibleAdminID
	} else if customer.OwnerAdminID != nil {
		operator = *customer.OwnerAdminID
	}
	if operator < 1 {
		return nil, fmt.Errorf("%w：该来源暂无归属客服，请联系客服发起售后", errForbidden)
	}
	in.Channel = "customer"
	customerID := customer.ID
	in.CustomerID = &customerID
	return createAfterSale(ctx, db, in, operator, now)
}

// customerSelfProfile 客户自助「我的」资料（会员等级与成长值；风险状态只读）。
type customerSelfProfile struct {
	ID               int      `json:"id"`
	DisplayName      string   `json:"display_name"`
	AgeStatus        string   `json:"age_status"`
	MemberLevelCode  string   `json:"member_level_code"`
	MemberLevelName  string   `json:"member_level_name"`
	GrowthPoints     int64    `json:"growth_points"`
	NextLevelCode    string   `json:"next_level_code"`
	NextLevelGrowth  int64    `json:"next_level_growth"`
	Tags             []string `json:"tags"`
	SelfOrderEnabled bool     `json:"self_order_enabled"`
	// 会员优先预约入口：当前等级启用 priority_booking 时才为 true，前端据此显隐预约入口。
	PriorityBookingEnabled bool   `json:"priority_booking_enabled"`
	RiskRestricted         bool   `json:"risk_restricted"`
	RiskReason             string `json:"risk_reason"`
}

// customerSelfProfileOf 组装客户自助资料：成长值按只增流水求和，下一档取第一个门槛高于当前成长值的等级。
// ponytail: 下一档依赖等级规则按 `sort_order` 升序且门槛递增，未做门槛单调性校验。
func customerSelfProfileOf(ctx context.Context, db *ent.Client,
	customer *ent.EsportsCustomer) (*customerSelfProfile, error) {
	if customer == nil {
		return nil, errForbidden
	}
	levels, err := db.EsportsMemberLevelRule.Query().
		Order(ent.Asc(esportsmemberlevelrule.FieldSortOrder)).All(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.CustomerIDEQ(customer.ID),
	).Select(esportsgrowthledger.FieldDelta).All(ctx)
	if err != nil {
		return nil, err
	}
	var growth int64
	for _, row := range rows {
		growth += row.Delta
	}
	tags := customer.Tags
	if tags == nil {
		tags = []string{}
	}
	profile := &customerSelfProfile{
		ID: customer.ID, DisplayName: customer.DisplayName,
		AgeStatus:       string(customer.AgeStatus),
		MemberLevelCode: customer.MemberLevelCode, GrowthPoints: growth,
		Tags: tags, SelfOrderEnabled: customer.SelfOrderEnabled,
	}
	for _, level := range levels {
		if level.Code == customer.MemberLevelCode {
			profile.MemberLevelName = level.Name
			// 预约入口：仅当前等级启用 priority_booking 且等级未停用才开放。
			if level.Status == esportsmemberlevelrule.StatusEnabled {
				profile.PriorityBookingEnabled = level.PriorityBooking
			}
		}
		if level.GrowthThreshold > growth {
			profile.NextLevelCode = level.Code
			profile.NextLevelGrowth = level.GrowthThreshold
			break
		}
	}
	if customer.RiskStatus == esportscustomer.RiskStatusRestricted {
		profile.RiskRestricted = true
		profile.RiskReason = customer.RiskReason
	}
	return profile, nil
}

// customerSelfWallet 客户自助「余额与权益」：余额（本金 / 赠送分列）。
type customerSelfWallet struct {
	PrincipalCents int64 `json:"principal_cents"`
	BonusCents     int64 `json:"bonus_cents"`
	TotalCents     int64 `json:"total_cents"`
}

// customerSelfWalletOf 汇总客户余额（余额由流水求和派生，不落库）。
func customerSelfWalletOf(ctx context.Context, db *ent.Client, customerID int) (*customerSelfWallet, error) {
	if customerID < 1 {
		return nil, errForbidden
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customerID)
	if err != nil {
		return nil, err
	}
	return &customerSelfWallet{
		PrincipalCents: balance.PrincipalCents, BonusCents: balance.BonusCents,
		TotalCents: balance.total(),
	}, nil
}
