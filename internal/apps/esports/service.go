package esports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/systemconfig"
)

var (
	errInvalidInput  = errors.New("请求参数无效")
	errConflict      = errors.New("业务状态已变化，请刷新后重试")
	errForbidden     = errors.New("无权访问该客户或订单")
	errAgeUnverified = errors.New("客户年龄资格尚未核验或不符合购买要求")
	errNotConfigured = errors.New("支付时限尚未由业务方配置")
)

type skuInput struct {
	Code                 string `json:"code"`
	Title                string `json:"title"`
	ServiceType          string `json:"service_type"`
	CategoryID           *int   `json:"category_id"`
	Description          string `json:"description"`
	UnitPriceCents       int64  `json:"unit_price_cents"`
	MinUnitPriceCents    int64  `json:"min_unit_price_cents"`
	AdjustmentApprovalBp int    `json:"adjustment_approval_bp"`
	ServiceUnits         int64  `json:"service_units"`
	ServiceUnitName      string `json:"service_unit_name"`
	RequiredSeats        int    `json:"required_seats"`
	// PerSeatServiceUnits 为多人 SKU 每个席位承诺的服务量（§15.3-01 / §5.3-05）：required_seats > 1 时必填，
	// 下单按「每席量 × 席位数 × 份数」落承诺量；0 表示未配置（单人单回落整单服务量）。
	PerSeatServiceUnits int64 `json:"per_seat_service_units"`
	// CostCents 为 SKU 成本价（分，可空，§15.8-03）：仅授权角色可读，用于毛利口径；nil 表示未登记成本。
	CostCents *int64 `json:"cost_cents"`
	// ServiceDurationMinutes 为单次服务标准时长（分钟，§5.5）：用于预约时段重叠与容量判定，0 表示未配置。
	ServiceDurationMinutes int `json:"service_duration_minutes"`
	// CloseRuleCode 为该 SKU 的「结单要求」（§15.3）：引用生效结单规则的 code，留空按游戏 / 全局回落。
	CloseRuleCode string `json:"close_rule_code"`
	QuotaTotal    int64  `json:"quota_total"`
	SortOrder     int    `json:"sort_order"`
	Status        string `json:"status"`
}

type categoryInput struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Game      string `json:"game"`
	SortOrder int    `json:"sort_order"`
	Status    string `json:"status"`
}

type skuUpdateInput struct {
	Title                string `json:"title"`
	ServiceType          string `json:"service_type"`
	CategoryID           *int   `json:"category_id"`
	Description          string `json:"description"`
	UnitPriceCents       int64  `json:"unit_price_cents"`
	MinUnitPriceCents    int64  `json:"min_unit_price_cents"`
	AdjustmentApprovalBp int    `json:"adjustment_approval_bp"`
	ServiceUnits         int64  `json:"service_units"`
	ServiceUnitName      string `json:"service_unit_name"`
	RequiredSeats        int    `json:"required_seats"`
	// PerSeatServiceUnits 同 skuInput（§15.3-01）。
	PerSeatServiceUnits int64 `json:"per_seat_service_units"`
	// CostCents 同 skuInput（§15.8-03）。
	CostCents *int64 `json:"cost_cents"`
	// ServiceDurationMinutes 为单次服务标准时长（分钟，§5.5）：用于预约时段重叠与容量判定，0 表示未配置。
	ServiceDurationMinutes int `json:"service_duration_minutes"`
	// CloseRuleCode 为该 SKU 的「结单要求」（§15.3）：引用生效结单规则的 code，留空按游戏 / 全局回落。
	CloseRuleCode string `json:"close_rule_code"`
	QuotaTotal    int64  `json:"quota_total"`
	SortOrder     int    `json:"sort_order"`
	Status        string `json:"status"`
}

type customerInput struct {
	DisplayName string `json:"display_name"`
}

type orderInput struct {
	CustomerID int   `json:"customer_id"`
	SkuID      int   `json:"sku_id"`
	Quantity   int64 `json:"quantity"`
	// 客户指定陪玩（§5.4）：可选。omitempty 让不传该字段时的幂等哈希与既有口径保持一致。
	PreferredPlayerID int `json:"preferred_player_id,omitempty"`
	// 预约开始时间（§3.2 / §5.5）：可选。带值时订单落为预约单（appointment_status=request，
	// 须确认档期后才能派单），并在下单时按派单同一套并发 / 每日容量口径复核指定陪玩（§4.6）。
	RequestedStartAt *time.Time `json:"requested_start_at,omitempty"`
	// 重复报单确认（§4.6）：时间窗内已有相同报单时须显式置 true 才放行。omitempty 让不传该字段时
	// 的幂等哈希与既有口径保持一致。
	ConfirmDuplicate bool `json:"confirm_duplicate,omitempty"`
	// 报单入口（§3.4）：移动客服端传 cs_mobile 以区别于后台 cs_pc；客户自助下单由身份前缀决定。
	// 仅允许 cs_pc / cs_mobile / wecom_sidebar 三个客服侧入口，非法值回落 cs_pc。
	EntryPoint string `json:"entry_point,omitempty"`
	// 经授权的人工调整（§4.2）：非零时须同时给出 4–255 字的 manual_adjust_reason；
	// 绝对值超过 SKU 的 adjustment_approval_bp 免审额度时，报单环节不予受理（须先落单再走改价审批）。
	ManualAdjustCents  int64  `json:"manual_adjust_cents,omitempty"`
	ManualAdjustReason string `json:"manual_adjust_reason,omitempty"`
	// 负责客服（§4.2）：留空默认建单人；显式指定时须为启用中的管理员。
	ResponsibleAdminID int `json:"responsible_admin_id,omitempty"`
	// 站内收款方式（§4.2 / §4.3）：customer_balance / stored_value_card 时在下单环节复核资金可用余额；
	// 留空或 external 表示走外部收款，下单不锁资金。
	PaymentChannel string `json:"payment_channel,omitempty"`
	// 订单价明显偏离 SKU 标准价时的偏离原因（§4.6-02）：超阈值且开启强制时必填。
	PriceDeviationReason string `json:"price_deviation_reason,omitempty"`
}

// normalizeEntryPoint 规范化报单入口：仅客服侧入口可显式指定，客户自助入口由调用方身份前缀决定，
// 不允许客服伪造 customer_self / admin_backfill。
func normalizeEntryPoint(raw string, principal string) esportsorder.EntryPoint {
	raw = strings.TrimSpace(raw)
	switch raw {
	case string(esportsorder.EntryPointCsMobile):
		return esportsorder.EntryPointCsMobile
	case string(esportsorder.EntryPointWecomSidebar):
		return esportsorder.EntryPointWecomSidebar
	}
	return esportsorder.EntryPointCsPc
}

func currentAdmin(ctx context.Context, db *ent.Client, uid string) (*ent.Admin, error) {
	if uid == "" {
		return nil, errForbidden
	}
	return db.Admin.Query().Where(admin.UIDEQ(uid), admin.StatusEQ("active")).Only(ctx)
}

func saveSku(ctx context.Context, db *ent.Client, in skuInput) (*ent.EsportsSku, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Title = strings.TrimSpace(in.Title)
	in.ServiceUnitName = strings.TrimSpace(in.ServiceUnitName)
	in.Description = strings.TrimSpace(in.Description)
	if in.MinUnitPriceCents == 0 {
		in.MinUnitPriceCents = in.UnitPriceCents
	}
	if in.AdjustmentApprovalBp == 0 {
		in.AdjustmentApprovalBp = 2000
	}
	if in.Code == "" || len(in.Code) > 80 || in.Title == "" || len([]rune(in.Title)) > 160 ||
		in.ServiceUnitName == "" || len([]rune(in.Description)) > 500 ||
		in.UnitPriceCents <= 0 || in.MinUnitPriceCents < 1 || in.MinUnitPriceCents > in.UnitPriceCents ||
		in.AdjustmentApprovalBp < 1 || in.AdjustmentApprovalBp > 10000 || in.ServiceUnits <= 0 ||
		in.RequiredSeats < 1 || in.QuotaTotal < -1 || (in.QuotaTotal != -1 && in.QuotaTotal < int64(in.RequiredSeats)) ||
		in.PerSeatServiceUnits < 0 ||
		in.ServiceDurationMinutes < 0 || in.ServiceDurationMinutes > 24*60 ||
		(in.Status != "draft" && in.Status != "on_sale") {
		return nil, errInvalidInput
	}
	if err := validateSkuServiceType(in.ServiceType); err != nil {
		return nil, err
	}
	if err := validateSkuCostCents(in.CostCents); err != nil {
		return nil, err
	}
	if in.CategoryID != nil {
		exists, err := db.EsportsProductCategory.Query().Where(
			esportsproductcategory.IDEQ(*in.CategoryID),
			esportsproductcategory.StatusEQ(esportsproductcategory.StatusEnabled),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errInvalidInput
		}
	}
	if err := validateSkuCloseRuleCode(ctx, db, in.CloseRuleCode); err != nil {
		return nil, err
	}
	create := db.EsportsSku.Create().SetCode(in.Code).SetTitle(in.Title).
		SetServiceType(esportssku.ServiceType(in.ServiceType)).SetDescription(in.Description).
		SetUnitPriceCents(in.UnitPriceCents).
		SetMinUnitPriceCents(in.MinUnitPriceCents).SetAdjustmentApprovalBp(in.AdjustmentApprovalBp).
		SetServiceUnits(in.ServiceUnits).SetServiceUnitName(in.ServiceUnitName).
		SetRequiredSeats(in.RequiredSeats).SetPerSeatServiceUnits(in.PerSeatServiceUnits).
		SetServiceDurationMinutes(in.ServiceDurationMinutes).
		SetQuotaTotal(in.QuotaTotal).
		SetCloseRuleCode(strings.TrimSpace(in.CloseRuleCode)).
		SetSortOrder(in.SortOrder).SetStatus(esportssku.Status(in.Status))
	if in.CategoryID != nil {
		create.SetCategoryID(*in.CategoryID)
	}
	if in.CostCents != nil {
		create.SetCostCents(*in.CostCents)
	}
	return create.Save(ctx)
}

// validateSkuServiceType 校验 SKU 服务类型（§3.1-04）：代练（boosting）为预留枚举位，
// 首版未开放上架与下单——必须显式返回「未开放」而不是与非法值混为一谈的笼统参数错误。
func validateSkuServiceType(raw string) error {
	switch strings.TrimSpace(raw) {
	case "play", "teaching", "entertainment":
		return nil
	case "boosting":
		return fmt.Errorf("%w：代练（boosting）首版未开放，暂不能上架或下单", errInvalidInput)
	default:
		return errInvalidInput
	}
}

// validateSkuCostCents 校验 SKU 成本价（§15.8-03）：可空；登记时须为非负整数分。
func validateSkuCostCents(cost *int64) error {
	if cost == nil {
		return nil
	}
	if *cost < 0 {
		return fmt.Errorf("%w：SKU 成本价不能为负", errInvalidInput)
	}
	return nil
}

func saveCustomer(ctx context.Context, db *ent.Client, adminID int, in customerInput) (*ent.EsportsCustomer, error) {
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if in.DisplayName == "" || len([]rune(in.DisplayName)) > 120 {
		return nil, errInvalidInput
	}
	return db.EsportsCustomer.Create().SetDisplayName(in.DisplayName).SetOwnerAdminID(adminID).Save(ctx)
}

// 分类编码创建后不可变，更新时不校验编码。
func validateCategoryFields(in categoryInput) (categoryInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Game = strings.TrimSpace(in.Game)
	if in.Name == "" || len([]rune(in.Name)) > 120 ||
		len([]rune(in.Game)) > 120 || (in.Status != "enabled" && in.Status != "disabled") {
		return in, errInvalidInput
	}
	return in, nil
}

func saveCategory(ctx context.Context, db *ent.Client, in categoryInput) (*ent.EsportsProductCategory, error) {
	in.Code = strings.TrimSpace(in.Code)
	in, err := validateCategoryFields(in)
	if err != nil || in.Code == "" || len(in.Code) > 80 {
		return nil, errInvalidInput
	}
	return db.EsportsProductCategory.Create().SetCode(in.Code).SetName(in.Name).
		SetGame(in.Game).SetSortOrder(in.SortOrder).
		SetStatus(esportsproductcategory.Status(in.Status)).Save(ctx)
}

func updateCategory(ctx context.Context, db *ent.Client, id int, in categoryInput) (*ent.EsportsProductCategory, error) {
	in, err := validateCategoryFields(in)
	if err != nil {
		return nil, err
	}
	changed, err := db.EsportsProductCategory.Update().Where(esportsproductcategory.IDEQ(id)).
		SetName(in.Name).SetGame(in.Game).SetSortOrder(in.SortOrder).
		SetStatus(esportsproductcategory.Status(in.Status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, &ent.NotFoundError{}
	}
	return db.EsportsProductCategory.Get(ctx, id)
}

// 分类被任一商品引用时禁止删除，避免商品失去归属。
func deleteCategory(ctx context.Context, db *ent.Client, id int) error {
	used, err := db.EsportsSku.Query().Where(esportssku.CategoryIDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if used {
		return errConflict
	}
	changed, err := db.EsportsProductCategory.Delete().Where(esportsproductcategory.IDEQ(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return &ent.NotFoundError{}
	}
	return nil
}

// updateSku 编辑商品。历史订单已快照价格与规格，改价只对新订单生效。
// ponytail: 变更操作人/时间依赖管理端操作日志中间件，未再建独立审计表。
func updateSku(ctx context.Context, db *ent.Client, id int, in skuUpdateInput) (*ent.EsportsSku, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.ServiceUnitName = strings.TrimSpace(in.ServiceUnitName)
	in.Description = strings.TrimSpace(in.Description)
	if in.MinUnitPriceCents == 0 {
		in.MinUnitPriceCents = in.UnitPriceCents
	}
	if in.AdjustmentApprovalBp == 0 {
		in.AdjustmentApprovalBp = 2000
	}
	if in.Title == "" || len([]rune(in.Title)) > 160 || in.ServiceUnitName == "" ||
		len([]rune(in.ServiceUnitName)) > 32 || len([]rune(in.Description)) > 500 ||
		in.UnitPriceCents <= 0 || in.MinUnitPriceCents < 1 || in.MinUnitPriceCents > in.UnitPriceCents ||
		in.AdjustmentApprovalBp < 1 || in.AdjustmentApprovalBp > 10000 || in.ServiceUnits <= 0 || in.RequiredSeats < 1 ||
		in.PerSeatServiceUnits < 0 ||
		in.ServiceDurationMinutes < 0 || in.ServiceDurationMinutes > 24*60 ||
		in.QuotaTotal < -1 ||
		(in.Status != "draft" && in.Status != "on_sale" && in.Status != "off_sale") {
		return nil, errInvalidInput
	}
	if err := validateSkuServiceType(in.ServiceType); err != nil {
		return nil, err
	}
	if err := validateSkuCostCents(in.CostCents); err != nil {
		return nil, err
	}
	if in.QuotaTotal != -1 && in.QuotaTotal < int64(in.RequiredSeats) {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := tx.EsportsSku.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// 限量名额不得调到已占用/已售数量以下。
	if in.QuotaTotal != -1 && in.QuotaTotal < current.QuotaReserved+current.QuotaSold {
		return nil, errConflict
	}
	if in.CategoryID != nil {
		exists, err := tx.EsportsProductCategory.Query().Where(
			esportsproductcategory.IDEQ(*in.CategoryID),
			esportsproductcategory.StatusEQ(esportsproductcategory.StatusEnabled),
		).Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errInvalidInput
		}
	}
	if err := validateSkuCloseRuleCode(ctx, tx.Client(), in.CloseRuleCode); err != nil {
		return nil, err
	}
	update := tx.EsportsSku.UpdateOne(current).SetTitle(in.Title).
		SetServiceType(esportssku.ServiceType(in.ServiceType)).SetDescription(in.Description).
		SetUnitPriceCents(in.UnitPriceCents).SetMinUnitPriceCents(in.MinUnitPriceCents).
		SetAdjustmentApprovalBp(in.AdjustmentApprovalBp).SetServiceUnits(in.ServiceUnits).
		SetServiceUnitName(in.ServiceUnitName).SetRequiredSeats(in.RequiredSeats).
		SetPerSeatServiceUnits(in.PerSeatServiceUnits).
		SetServiceDurationMinutes(in.ServiceDurationMinutes).
		SetCloseRuleCode(strings.TrimSpace(in.CloseRuleCode)).
		SetQuotaTotal(in.QuotaTotal).SetSortOrder(in.SortOrder).
		SetStatus(esportssku.Status(in.Status)).AddVersion(1)
	// §15.8-03：成本价可空；显式传 null 表示清空已登记的成本。
	if in.CostCents == nil {
		update.ClearCostCents()
	} else {
		update.SetCostCents(*in.CostCents)
	}
	if in.CategoryID == nil {
		update.ClearCategoryID()
	} else {
		update.SetCategoryID(*in.CategoryID)
	}
	if _, err = update.Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSku.Get(ctx, id)
}

func paymentDeadlineMinutes(ctx context.Context, db *ent.Client) (int, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ("app.esports.payment_deadline_minutes")).Only(ctx)
	if ent.IsNotFound(err) {
		return 0, errNotConfigured
	}
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(item.Value)
	if err != nil || minutes < 1 || minutes > 24*60 {
		return 0, errNotConfigured
	}
	return minutes, nil
}

func orderRequestHash(in orderInput) string {
	data, _ := json.Marshal(in)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func existingOrder(ctx context.Context, db *ent.Client, principal, key, hash string) (*ent.EsportsOrder, error) {
	record, err := db.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ("create_order"),
		esportsidempotency.PrincipalEQ(principal),
		esportsidempotency.RequestKeyEQ(key),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if record.RequestHash != hash {
		return nil, errConflict
	}
	return db.EsportsOrder.Query().Where(esportsorder.OrderNoEQ(record.ResultRef)).Only(ctx)
}

// placeOrder is an instant-order slice. Appointment and external payment entry
// points remain disabled until their separate source-feature workflows are built.
func placeOrder(ctx context.Context, db *ent.Client, adminID int, in orderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	return placeOrderWithPrincipal(ctx, db, fmt.Sprintf("admin:%d", adminID), adminID, in, key, now)
}

// placeOrderWithPrincipal 按调用方身份（客服下单 / 客户自助下单）落单，幂等键按身份隔离。
func placeOrderWithPrincipal(ctx context.Context, db *ent.Client, principal string, adminID int,
	in orderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	order, err := placeOrderOnce(ctx, db, principal, adminID, in, key, now)
	if errors.Is(err, errConflict) {
		prior, lookupErr := existingOrder(ctx, db, principal, key, orderRequestHash(in))
		if lookupErr == nil && prior != nil {
			return prior, nil
		}
	}
	return order, err
}

// applyOrderManualAdjust 计算下单应付金额并校验收单环节的人工调整（§4.2-02）：
//   - 未填调整金额时只走会员折扣；
//   - 填了调整金额必须同时给出 4–255 字原因；
//   - 调整后的小计不得低于 SKU 最低允许价 × 份数；
//   - 调整绝对值超过 SKU 免审额度（adjustment_approval_bp）时下单环节不予受理：调价单必须挂已有订单，
//     报单时尚无可引用的已审批调价单，须先落单再走 POST /admin/orders/{id}/quote 的审批 → 改价链路。
func applyOrderManualAdjust(subtotal, discount int64, in orderInput, sku *ent.EsportsSku) (int64, error) {
	reason := strings.TrimSpace(in.ManualAdjustReason)
	if in.ManualAdjustCents == 0 {
		if reason != "" {
			return 0, fmt.Errorf("%w：未填写人工调整金额时不应带调整原因", errInvalidInput)
		}
		return subtotal - discount, nil
	}
	if len([]rune(reason)) < 4 || len([]rune(reason)) > 255 {
		return 0, fmt.Errorf("%w：人工调整须填写 4–255 字的调整原因", errInvalidInput)
	}
	if in.ManualAdjustCents <= -subtotal || in.ManualAdjustCents > math.MaxInt64-subtotal {
		return 0, errInvalidInput
	}
	floor := sku.MinUnitPriceCents
	if floor <= 0 {
		floor = sku.UnitPriceCents
	}
	adjusted := subtotal + in.ManualAdjustCents
	if adjusted < floor*in.Quantity {
		return 0, fmt.Errorf("%w：人工议价低于 SKU 最低允许价", errInvalidInput)
	}
	approvalBp := sku.AdjustmentApprovalBp
	if approvalBp <= 0 || approvalBp > 10000 {
		approvalBp = 2000
	}
	threshold := subtotal/10000*int64(approvalBp) + subtotal%10000*int64(approvalBp)/10000
	absAdjust := in.ManualAdjustCents
	if absAdjust < 0 {
		absAdjust = -absAdjust
	}
	if absAdjust > threshold {
		return 0, fmt.Errorf("%w：人工调整 %d 分超过免审额度 %d 分，请先落单后走改价审批",
			errInvalidInput, absAdjust, threshold)
	}
	return adjusted - discount, nil
}

func placeOrderOnce(ctx context.Context, db *ent.Client, principal string, adminID int,
	in orderInput, key string, now time.Time) (*ent.EsportsOrder, error) {
	if in.CustomerID < 1 || in.SkuID < 1 || in.Quantity < 1 || in.Quantity > 1000 || len(key) < 8 || len(key) > 128 {
		return nil, errInvalidInput
	}
	hash := orderRequestHash(in)
	if previous, err := existingOrder(ctx, db, principal, key, hash); err != nil || previous != nil {
		return previous, err
	}
	minutes, err := paymentDeadlineMinutes(ctx, db)
	if err != nil {
		return nil, err
	}
	deadline := now.UTC().Add(time.Duration(minutes) * time.Minute)
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	customer, err := tx.EsportsCustomer.Get(ctx, in.CustomerID)
	if err != nil {
		return nil, err
	}
	// §5.1-02：客户自助下单允许「无归属客服」——此时 adminID=0，订单进公共池（responsible_admin_id 为 NULL）。
	// 客服侧报单仍严格校验归属，避免越权代报。
	selfService := adminID < 1
	if selfService {
		if strings.TrimSpace(principal) != customerSelfPrincipal(customer.ID) {
			return nil, errForbidden
		}
	} else if customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID {
		return nil, errForbidden
	}
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	sku, err := tx.EsportsSku.Get(ctx, in.SkuID)
	if err != nil {
		return nil, err
	}
	if sku.Status != esportssku.StatusOnSale || sku.UnitPriceCents > math.MaxInt64/in.Quantity ||
		sku.ServiceUnits > math.MaxInt64/in.Quantity {
		return nil, errConflict
	}
	// §15.5-01：有规格组时提前读取，用于「每席承诺服务量」与商品/规格名快照（§15.6-03）。
	var specGroup *ent.EsportsSpecGroup
	if sku.SpecGroupID != nil {
		if specGroup, err = tx.EsportsSpecGroup.Get(ctx, *sku.SpecGroupID); err != nil {
			return nil, err
		}
	}
	// §15.3-01 / §15.5-01 / §5.3-05：解析每席承诺服务量；多人单缺每席量即拒绝报单（不静默落 0）。
	perSeatUnits, seatTotalUnits, seatSnapshot, err := resolveSeatPromise(sku, specGroup)
	if err != nil {
		return nil, err
	}
	// 承诺服务量 = Σ 每席承诺量 × 份数（§5.3-05 验收口径），并复核溢出与正值约束。
	if seatTotalUnits > math.MaxInt64/in.Quantity {
		return nil, errInvalidInput
	}
	promisedUnits := seatTotalUnits * in.Quantity
	if promisedUnits <= 0 {
		return nil, fmt.Errorf("%w：承诺服务量必须为正，请检查 SKU 每席承诺服务量配置", errInvalidInput)
	}
	// §4.6 报单校验补充（顺序：先硬拦截后提醒）：预约开始时间合法性 → 客户欠款拦截 → 重复报单提醒。
	// 三项都在本事务内完成，未通过即整单回滚（不占名额、不写幂等记录），不影响既有幂等与报价快照语义。
	if err = validateAppointmentStart(in, now); err != nil {
		return nil, err
	}
	// §4.6-04：欠款默认「有欠款即提醒」，配置阈值后升级为强制拦截。
	debtCents, debtRemind, err := customerDebtTier(ctx, tx.Client(), customer.ID,
		customerDebtLimitCents(ctx, tx.Client()), customerDebtRemindCents(ctx, tx.Client()), now)
	if err != nil {
		return nil, err
	}
	if err = checkDuplicateOrder(ctx, tx.Client(), in, now,
		duplicateOrderWindowMinutes(ctx, tx.Client())); err != nil {
		return nil, err
	}
	// §4.2-02：指定负责客服须为启用中的管理员；留空默认建单人。客户自助下单（无归属客服）落 NULL。
	responsibleAdminID := adminID
	if in.ResponsibleAdminID > 0 && in.ResponsibleAdminID != adminID {
		operator, adminErr := tx.Admin.Query().Where(
			admin.IDEQ(in.ResponsibleAdminID), admin.StatusEQ("active")).Only(ctx)
		if adminErr != nil {
			return nil, fmt.Errorf("%w：指定的负责客服不存在或已停用", errInvalidInput)
		}
		responsibleAdminID = operator.ID
	}
	if responsibleAdminID < 1 && !selfService {
		return nil, errForbidden
	}
	// A conditional UPDATE, rather than a prior SELECT alone, wins the final unit.
	updated, err := tx.EsportsSku.Update().Where(
		esportssku.IDEQ(sku.ID), esportssku.VersionEQ(sku.Version),
		esportssku.StatusEQ(esportssku.StatusOnSale),
		func(s *sql.Selector) {
			s.Where(sql.Or(sql.EQ(esportssku.FieldQuotaTotal, -1),
				sql.ExprP("quota_total - quota_reserved - quota_sold >= ?", in.Quantity)))
		},
	).AddQuotaReserved(in.Quantity).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if updated != 1 {
		return nil, errConflict
	}
	// 优惠顺序（§10.4）：商品规格价 → 会员折扣 → 经授权人工调整；下单即落 manual_adjust_cents，
	// 后续改价在其上覆盖（§4.2-02）。
	subtotal := sku.UnitPriceCents * in.Quantity
	discountBps, err := memberDiscountBps(ctx, tx.Client(), customer.MemberLevelCode)
	if err != nil {
		return nil, err
	}
	// §4.2-06：单笔折扣上限复核（配置缺失表示不限制）。
	if err = ensureOrderDiscountWithinLimit(discountBps, orderDiscountLimitBps(ctx, tx.Client())); err != nil {
		return nil, err
	}
	discount := memberDiscountCents(subtotal, discountBps)
	payable, err := applyOrderManualAdjust(subtotal, discount, in, sku)
	if err != nil {
		return nil, err
	}
	if payable <= 0 {
		// §4.6：普通服务单折后应付为零时拦截。
		return nil, fmt.Errorf("%w：该订单会员折扣后应付金额为零，无法报单", errInvalidInput)
	}
	// §4.6-02：订单价明显偏离 SKU 标准价时的提醒 / 原因必填。
	deviation, err := evaluatePriceDeviation(ctx, tx.Client(), subtotal, payable, in.PriceDeviationReason)
	if err != nil {
		return nil, err
	}
	// §4.2-06：显式选择站内收款方式时复核资金可用余额，避免下单成功而收款才失败。
	if err = ensureOrderFundsAvailable(ctx, tx.Client(), customer.ID, payable, in.PaymentChannel); err != nil {
		return nil, err
	}
	// §4.6 报单校验：指定陪玩须存在且资质合格（未通过考核或已停用不允许选择）。
	if in.PreferredPlayerID > 0 {
		player, playerErr := tx.EsportsPlayer.Get(ctx, in.PreferredPlayerID)
		if playerErr != nil {
			return nil, playerErr
		}
		if player.Qualification != esportsplayer.QualificationQualified {
			return nil, fmt.Errorf("%w：指定陪玩未通过考核或已停用，不可指定", errInvalidInput)
		}
		// §4.6 预约冲突与资源复核：带预约开始时间时按派单同一套容量口径复核该陪玩能否占位。
		if err = checkAppointmentCapacity(ctx, tx, player, sku, in.RequestedStartAt, now); err != nil {
			return nil, err
		}
	}
	orderNo := uuid.NewString()
	entryPoint := esportsorder.EntryPointCsPc
	if strings.HasPrefix(principal, "customer:") {
		entryPoint = esportsorder.EntryPointCustomerSelf
	} else {
		// 客服侧（后台 / 移动客服 / 企微侧边栏）可由报单入口显式区分，非法值回落 cs_pc。
		entryPoint = normalizeEntryPoint(in.EntryPoint, principal)
	}
	// 下单时固定适用结单表单（包括无模板）；后续发布新版本不改变旧单结单要求。
	// §3.4-07：探针带上订单类型字段，保证 scope=order_type 的模板在报单环节即可命中。
	closeProbe := &ent.EsportsOrder{SkuID: sku.ID, PayableCents: payable,
		AppointmentStatus: esportsorder.AppointmentStatusInstant}
	if in.RequestedStartAt != nil {
		closeProbe.AppointmentStatus = esportsorder.AppointmentStatusRequest
	}
	closeTemplate, err := resolveCloseFormTemplate(ctx, tx.Client(), closeProbe)
	if err != nil {
		return nil, err
	}
	// §3.4-01：仅在启用内置结单模板时兜底锁定它，默认不改变存量「无模板」口径。
	if closeTemplate == nil && closeFormRequireServiceResult(ctx, tx.Client()) {
		if closeTemplate, err = ensureBuiltinCloseFormTemplate(ctx, tx.Client()); err != nil {
			return nil, err
		}
	}
	creator := tx.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customer.ID).
		SetSkuID(sku.ID).
		// §15.6-03：下单即固定商品 / 规格名快照，之后改 SKU 标题或规格名不追溯历史单。
		SetSkuCode(sku.Code).SetSkuName(sku.Title).
		SetChannelSource(esportsorder.ChannelSource(customer.Source.String())).SetEntryPoint(entryPoint).
		SetQuantity(in.Quantity).SetUnitPriceCents(sku.UnitPriceCents).
		SetMinUnitPriceCents(sku.MinUnitPriceCents).SetAdjustmentApprovalBp(sku.AdjustmentApprovalBp).
		SetDiscountCents(discount).SetManualAdjustCents(in.ManualAdjustCents).SetPayableCents(payable).
		// §10.4-08：会员等级与折扣基点快照，事后改折扣规则不追溯。
		SetMemberLevelCode(customer.MemberLevelCode).SetMemberDiscountBps(discountBps).
		// §15.3-01 / §15.5-01 / §5.3-05：承诺量按「Σ 每席承诺量 × 份数」落库。
		SetPromisedServiceUnits(promisedUnits).SetPerSeatServiceUnits(perSeatUnits).
		SetRequiredSeats(sku.RequiredSeats).SetRequiredLevel(esportsorder.RequiredLevel(sku.RequiredLevel)).
		SetServiceDurationMinutes(sku.ServiceDurationMinutes).
		SetPaymentDeadline(deadline).SetCloseFormTemplateLockedAt(now.UTC())
	// §5.1-02：客户自助下单且客户无归属客服时负责客服为 NULL，订单进公共池；建单人同样留空。
	if adminID > 0 {
		creator = creator.SetCreatedByAdminID(adminID)
	}
	if responsibleAdminID > 0 {
		creator = creator.SetResponsibleAdminID(responsibleAdminID)
	}
	if closeTemplate != nil {
		creator = creator.SetCloseFormTemplateID(closeTemplate.ID)
	}
	// §15.5-02：报单时把规格组席位定义（含逐席承诺量）快照进订单 seat_roles（后续改规格不影响历史单）。
	if specGroup != nil {
		creator = creator.SetSpecName(specGroup.Name)
	}
	if len(seatSnapshot) > 0 {
		creator = creator.SetSeatRoles(seatSnapshot)
	}
	if in.PreferredPlayerID > 0 {
		creator = creator.SetPreferredPlayerID(in.PreferredPlayerID)
	}
	// 带预约开始时间即落为预约单（§3.2 / §5.5）：写档期提案与确认时限（缺省 24 小时且不晚于开始时间），
	// 档期确认前不得派单；与 rescheduleOrder 的提案字段口径一致。
	if in.RequestedStartAt != nil {
		startAt := in.RequestedStartAt.UTC()
		confirmDeadline := now.UTC().Add(time.Duration(appointmentConfirmHours(ctx, tx.Client())) * time.Hour)
		if confirmDeadline.After(startAt) {
			confirmDeadline = startAt
		}
		creator = creator.SetRequestedStartAt(startAt).
			SetAppointmentStatus(esportsorder.AppointmentStatusRequest).
			SetAppointmentConfirmationDeadline(confirmDeadline)
	}
	order, err := creator.Save(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsQuotaHold.Create().SetOrderID(order.ID).SetSkuID(sku.ID).
		SetQuantity(in.Quantity).SetStatus("reserved").SetExpiresAt(deadline).Save(ctx); err != nil {
		return nil, err
	}
	details := map[string]interface{}{"sku_code": sku.Code, "quantity": in.Quantity}
	// 留痕：重复报单是被客服显式确认后放行的；预约单记下档期提案时间点。
	if in.ConfirmDuplicate {
		details["duplicate_confirmed"] = true
	}
	if in.RequestedStartAt != nil {
		details["requested_start_at"] = in.RequestedStartAt.UTC().Format(time.RFC3339)
	}
	// §4.2-02：人工调整的原因与指定负责客服留痕。
	if in.ManualAdjustCents != 0 {
		details["manual_adjust_cents"] = in.ManualAdjustCents
		details["manual_adjust_reason"] = strings.TrimSpace(in.ManualAdjustReason)
	}
	if responsibleAdminID != adminID {
		details["responsible_admin_id"] = responsibleAdminID
	}
	// §4.6-02：明显偏离 SKU 标准价时把偏离度与原因写进订单事件（提醒 + 留痕）。
	if deviation != nil {
		details["price_deviation_bp"] = deviation.DeviationBp
		details["price_deviation_standard_cents"] = deviation.StandardCents
		details["price_deviation_reason"] = deviation.Reason
	}
	// §4.6-04：有未结清欠款时记录提醒金额。
	if debtRemind {
		details["debt_remind_cents"] = debtCents
	}
	// §5.1-02：客户自助下单的订单事件归到客户身份（无客服主体），其余仍记客服操作人。
	actorType, actorID := "admin", strconv.Itoa(adminID)
	if adminID < 1 {
		actorType, actorID = "customer", strconv.Itoa(customer.ID)
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).SetOrderVersion(1).
		SetEventType("order.created").SetActorType(actorType).SetActorID(actorID).
		SetDetails(details).Save(ctx); err != nil {
		return nil, err
	}
	// §4.6-04 提醒档：欠款提醒写一条事务外盒，供客服工作台催收；不阻断下单。
	if debtRemind {
		if _, err = tx.EsportsOutbox.Create().SetEventKey("order.debt_remind:" + orderNo).
			SetEventType("order.debt_remind").SetAggregateRef(orderNo).
			SetPayload(map[string]interface{}{
				"order_no": orderNo, "customer_id": customer.ID, "debt_cents": debtCents,
			}).Save(ctx); err != nil {
			return nil, err
		}
	}
	// §4.6-02 提醒档：偏离标准价的独立提醒外盒（关闭强制填写时也会落；订单事件已按版本唯一，
	// 故提醒走事务外盒而非第二条订单事件）。
	if deviation != nil {
		if _, err = tx.EsportsOutbox.Create().SetEventKey("order.price_deviation_flagged:" + orderNo).
			SetEventType("order.price_deviation_flagged").SetAggregateRef(orderNo).
			SetPayload(map[string]interface{}{
				"order_no": orderNo, "deviation_bp": deviation.DeviationBp,
				"standard_cents": deviation.StandardCents, "payable_cents": deviation.PayableCents,
				"reason": deviation.Reason,
			}).Save(ctx); err != nil {
			return nil, err
		}
	}
	if _, err = tx.EsportsIdempotency.Create().SetOperation("create_order").SetPrincipal(principal).
		SetRequestKey(key).SetRequestHash(hash).SetResultRef(orderNo).Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.created:" + orderNo).
		SetEventType("order.created").SetAggregateRef(orderNo).
		SetPayload(map[string]interface{}{"order_no": orderNo}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return order, nil
}

// expireOrder releases an unpaid order's SKU units in the same transaction as
// its state transition. A payment callback must use the same version predicate.
func expireOrder(ctx context.Context, db *ent.Client, orderID int, now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return false, err
	}
	// 先看本订单是否还有待核验的外部到账凭证：有待核验凭证说明客户可能已实际转账、
	// 只是财务还没核验，超时任务不得抢先取消订单并释放名额；本轮直接交还给核验流程，
	// 订单保持待付款，等财务核验后再走收款核销或由驳回后的下一轮超时扫描处理。
	hasPendingProof, err := tx.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.OrderIDEQ(orderID),
		esportsofflinepaymentproof.StatusEQ(esportsofflinepaymentproof.StatusPendingVerification),
	).Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("查询订单待核验到账凭证失败: %w", err)
	}
	if hasPendingProof {
		return false, nil
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || order.PaymentDeadline.After(now.UTC()) {
		return false, nil
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		esportsorder.PaymentDeadlineLTE(now.UTC()),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, errConflict
	}
	if err = releaseReservedQuota(ctx, tx, orderID); err != nil {
		return false, err
	}
	if _, err = tx.EsportsPaymentIntent.Update().Where(
		esportspaymentintent.OrderIDEQ(orderID),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).SetStatus(esportspaymentintent.StatusExpired).Save(ctx); err != nil {
		return false, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.payment_expired").
		SetActorType("system").SetActorID("expiry").Save(ctx); err != nil {
		return false, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.payment_expired:" + order.OrderNo).
		SetEventType("order.payment_expired").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo}).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func expireDueOrders(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	// 有待核验到账凭证的订单本轮不取消（客户可能已实际付款，见 expireOrder）。
	// 在扫描阶段就排除它们：这类订单会一直满足「已过期」条件且按 payment_deadline 排在最前，
	// 若只靠逐单守卫跳过，累计占满 LIMIT 窗口后每轮都扫不到真正可取消的订单，超时任务会停摆。
	pendingOrderIDs, err := pendingVerificationProofOrderIDs(ctx, db)
	if err != nil {
		return 0, err
	}
	query := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		esportsorder.PaymentDeadlineLTE(now.UTC()),
	)
	if len(pendingOrderIDs) > 0 {
		query = query.Where(esportsorder.IDNotIn(pendingOrderIDs...))
	}
	ids, err := query.Order(ent.Asc(esportsorder.FieldPaymentDeadline)).Limit(limit).IDs(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		expired, err := expireOrder(ctx, db, id, now)
		if errors.Is(err, errConflict) {
			continue // a payment or another worker won the version race
		}
		if err != nil {
			return count, err
		}
		if expired {
			count++
		}
	}
	return count, nil
}

// pendingVerificationProofOrderIDs 返回仍有待核验到账凭证的订单 ID（去重，升序无关）。
// 供超时取消的扫描窗口排除这些订单，避免它们长期占用 LIMIT 名额。
func pendingVerificationProofOrderIDs(ctx context.Context, db *ent.Client) ([]int, error) {
	orderIDs, err := db.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.StatusEQ(esportsofflinepaymentproof.StatusPendingVerification),
	).Select(esportsofflinepaymentproof.FieldOrderID).Ints(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]struct{}, len(orderIDs))
	out := make([]int, 0, len(orderIDs))
	for _, id := range orderIDs {
		if id < 1 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}
