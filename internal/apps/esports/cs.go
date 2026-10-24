package esports

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportscustomerfollowup"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsprioritybookingintent"
	"go-admin/internal/ent/esportsscript"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/global"
)

// 客服端常量：标签数量 / 长度上限与话术状态。
const (
	csMaxTags   = 20
	csMaxTagLen = 30
)

// customerProfileInput 客户档案可编辑字段。跟进状态可选传，空表示不改。
// Tags 为增量语义：字段缺省 / null（nil）表示不改动既有标签，显式传（含空数组）才整体覆盖。
// ExpectedVersion 为乐观锁：>0 时按 customer.version 条件更新，版本不符返回冲突；
// 缺省 0 保持既有「最后写入者胜」行为，兼容尚未接入版本号的前端。
type customerProfileInput struct {
	Phone                 string   `json:"phone"`
	Birthday              string   `json:"birthday"`
	ConsumptionPreference string   `json:"consumption_preference"`
	Tags                  []string `json:"tags"`
	FollowStatus          string   `json:"follow_status"`
	// SelfOrderEnabled 工作室是否允许该客户自助下单；不传保持原值。
	SelfOrderEnabled *bool `json:"self_order_enabled"`
	ExpectedVersion  int   `json:"expected_version"`
}

// followUpInput 跟进记录输入。
type followUpInput struct {
	Channel      string `json:"channel"`
	Summary      string `json:"summary"`
	NextFollowAt string `json:"next_follow_at"`
}

// scriptInput 话术库输入。
type scriptInput struct {
	Title           string `json:"title"`
	Category        string `json:"category"`
	Content         string `json:"content"`
	SortOrder       int    `json:"sort_order"`
	Status          string `json:"status"`
	ExpectedVersion int    `json:"expected_version"`
}

// wecomIntakeInput 企微侧边栏自动建档输入。
type wecomIntakeInput struct {
	CorpID      string `json:"corp_id"`
	ExternalID  string `json:"external_id"`
	DisplayName string `json:"display_name"`
	Phone       string `json:"phone"`
}

func validFollowStatus(v string) bool {
	switch esportscustomer.FollowStatus(v) {
	case esportscustomer.FollowStatusNew, esportscustomer.FollowStatusFollowing,
		esportscustomer.FollowStatusPending, esportscustomer.FollowStatusDormant,
		esportscustomer.FollowStatusClosed:
		return true
	}
	return false
}

func validFollowChannel(v string) bool {
	switch esportscustomerfollowup.Channel(v) {
	case esportscustomerfollowup.ChannelPc, esportscustomerfollowup.ChannelMobile,
		esportscustomerfollowup.ChannelWecom, esportscustomerfollowup.ChannelPhone,
		esportscustomerfollowup.ChannelOther:
		return true
	}
	return false
}

// loadOwnedCustomer 取客户并校验归属：仅归属客服本人或超级管理员可见（按归属隔离）。
func loadOwnedCustomer(ctx context.Context, db *ent.Client, adminID int, isSuper bool, customerID int) (*ent.EsportsCustomer, error) {
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if !isSuper && (customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID) {
		return nil, errForbidden
	}
	return customer, nil
}

// updateCustomerProfile 更新客户档案扩展字段：手机号、消费偏好、标签、跟进状态。
func updateCustomerProfile(ctx context.Context, db *ent.Client, adminID int, isSuper bool, customerID int, in customerProfileInput) (*ent.EsportsCustomer, error) {
	if _, err := loadOwnedCustomer(ctx, db, adminID, isSuper, customerID); err != nil {
		return nil, err
	}
	in.Phone = strings.TrimSpace(in.Phone)
	in.Birthday = strings.TrimSpace(in.Birthday)
	in.ConsumptionPreference = strings.TrimSpace(in.ConsumptionPreference)
	if len(in.Phone) > 32 || len([]rune(in.ConsumptionPreference)) > 255 ||
		(in.Birthday != "" && !validBirthday(in.Birthday)) {
		return nil, errInvalidInput
	}
	// 标签去空、去重、限长限数，避免同一标签重复堆积。
	tags := make([]string, 0, len(in.Tags))
	seen := make(map[string]bool, len(in.Tags))
	for _, raw := range in.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || seen[tag] {
			continue
		}
		if len([]rune(tag)) > csMaxTagLen {
			return nil, errInvalidInput
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	if len(tags) > csMaxTags {
		return nil, errInvalidInput
	}
	// P1-12：同一租户内手机号唯一。把手机号改成他人已占用的号码时显式冲突，
	// 避免两个客户档案共用同一联系号码（按手机号认人 / 企微建档去重都会失真）。
	if err := ensureCustomerPhoneUnique(ctx, db, in.Phone, customerID); err != nil {
		return nil, err
	}
	update := db.EsportsCustomer.Update().Where(esportscustomer.IDEQ(customerID)).
		SetPhone(in.Phone).SetBirthday(in.Birthday).
		SetConsumptionPreference(in.ConsumptionPreference).AddVersion(1)
	// 标签增量语义：请求未提供 tags（JSON 中不存在 / null，解码为 nil）时不改动既有标签，
	// 只有显式提供（含空数组）才整体覆盖，避免漏传 tags 的调用方清空其它入口维护的标签。
	if in.Tags != nil {
		update.SetTags(tags)
	}
	if in.SelfOrderEnabled != nil {
		update.SetSelfOrderEnabled(*in.SelfOrderEnabled)
	}
	if in.FollowStatus != "" {
		if !validFollowStatus(in.FollowStatus) {
			return nil, errInvalidInput
		}
		update.SetFollowStatus(esportscustomer.FollowStatus(in.FollowStatus))
	}
	// 乐观锁：显式传 expected_version 时按版本条件更新，避免并发改档互相覆盖。
	if in.ExpectedVersion > 0 {
		update.Where(esportscustomer.VersionEQ(in.ExpectedVersion))
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsCustomer.Get(ctx, customerID)
}

// validBirthday 校验生日格式为 MM-DD（月 01-12、日 01-31）；空串由调用方先排除。
func validBirthday(value string) bool {
	if len(value) != 5 || value[2] != '-' {
		return false
	}
	month, err := strconv.Atoi(value[:2])
	if err != nil || month < 1 || month > 12 {
		return false
	}
	day, err := strconv.Atoi(value[3:])
	if err != nil || day < 1 || day > 31 {
		return false
	}
	return true
}

// customerProfileOverview 汇总客户档案：基本信息、消费 / 充值 / 送礼 / 余额 / 成长值。
// ponytail: 累计值走 SQL SUM 聚合，未分页；需要更细的区间口径时改走上报表接口。
func customerProfileOverview(ctx context.Context, db *ent.Client, adminID int, isSuper bool, customerID int) (map[string]any, error) {
	customer, err := loadOwnedCustomer(ctx, db, adminID, isSuper, customerID)
	if err != nil {
		return nil, err
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customerID)
	if err != nil {
		return nil, err
	}
	recharged, err := sumWalletPrincipal(ctx, db, customerID, esportswallettx.EntryTypeRecharge)
	if err != nil {
		return nil, err
	}
	// 累计消费：已付款订单实付额；累计退款单列，便于客服判断净额。
	consumed, err := sumOrderCents(ctx, db, customerID, esportsorder.FieldPayableCents)
	if err != nil {
		return nil, err
	}
	refunded, err := sumOrderCents(ctx, db, customerID, esportsorder.FieldRefundedCents)
	if err != nil {
		return nil, err
	}
	gifted, err := sumGiftCents(ctx, db, customerID, esportsgiftorder.FieldAmountCents)
	if err != nil {
		return nil, err
	}
	giftRefunded, err := sumGiftCents(ctx, db, customerID, esportsgiftorder.FieldRefundCents)
	if err != nil {
		return nil, err
	}
	growth, err := customerGrowthTotal(ctx, db, customerID)
	if err != nil {
		return nil, err
	}
	orderCount, err := db.EsportsOrder.Query().Where(esportsorder.CustomerIDEQ(customerID)).Count(ctx)
	if err != nil {
		return nil, err
	}
	// 身份标识 + 会员权益快照（P1-11）：客户卡片一次性读出全部权益字段，
	// 让权益页宣称的「已生效」与真实消费点（会员折扣 / 派单置顶 / 优先预约）共用同一判定口径。
	privilege, err := memberPrivilegeFlags(ctx, db, customerID)
	if err != nil {
		return nil, err
	}
	ownerAdminName := ""
	if customer.OwnerAdminID != nil {
		names := adminNames(ctx, db, []int{*customer.OwnerAdminID})
		ownerAdminName = names[*customer.OwnerAdminID]
	}
	return map[string]any{
		"customer":          customer,
		"principal_cents":   balance.PrincipalCents,
		"bonus_cents":       balance.BonusCents,
		"balance_cents":     balance.total(),
		"recharged_cents":   recharged,
		"consumed_cents":    consumed,
		"refunded_cents":    refunded,
		"gifted_cents":      gifted - giftRefunded,
		"growth_total":      growth,
		"order_count":       orderCount,
		"member_level_code": customer.MemberLevelCode,
		"member_level_name": privilege.LevelName,
		"dedicated_cs":      privilege.DedicatedCs,
		"member_privilege":  privilege,
		"owner_admin_name":  ownerAdminName,
		"source":            customer.Source.String(),
		"follow_status":     customer.FollowStatus.String(),
	}, nil
}

// sumWalletPrincipal 求和客户余额账户某类流水的本金增量（分）。
func sumWalletPrincipal(ctx context.Context, db *ent.Client, ownerID int, entryType esportswallettx.EntryType) (int64, error) {
	var rows []struct {
		Sum int64 `json:"sum"`
	}
	if err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeCustomerBalance),
		esportswallettx.OwnerIDEQ(ownerID),
		esportswallettx.EntryTypeEQ(entryType),
	).Aggregate(ent.Sum(esportswallettx.FieldPrincipalDeltaCents)).Scan(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

// sumOrderCents 求和客户已付款订单的指定金额列（分）。
func sumOrderCents(ctx context.Context, db *ent.Client, customerID int, field string) (int64, error) {
	var rows []struct {
		Sum int64 `json:"sum"`
	}
	if err := db.EsportsOrder.Query().Where(
		esportsorder.CustomerIDEQ(customerID),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusPaid),
	).Aggregate(ent.Sum(field)).Scan(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

// sumGiftCents 求和客户礼物单据的指定金额列（分）。
func sumGiftCents(ctx context.Context, db *ent.Client, customerID int, field string) (int64, error) {
	var rows []struct {
		Sum int64 `json:"sum"`
	}
	if err := db.EsportsGiftOrder.Query().Where(
		esportsgiftorder.CustomerIDEQ(customerID),
	).Aggregate(ent.Sum(field)).Scan(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

// addCustomerFollowUp 写跟进记录并在同一事务刷新客户最近联系时间。
func addCustomerFollowUp(ctx context.Context, db *ent.Client, adminID int, isSuper bool, customerID int, in followUpInput, now time.Time) (*ent.EsportsCustomerFollowUp, error) {
	if _, err := loadOwnedCustomer(ctx, db, adminID, isSuper, customerID); err != nil {
		return nil, err
	}
	in.Summary = strings.TrimSpace(in.Summary)
	if in.Summary == "" || len([]rune(in.Summary)) > 500 {
		return nil, errInvalidInput
	}
	channel := strings.TrimSpace(in.Channel)
	if channel == "" {
		channel = string(esportscustomerfollowup.ChannelPc)
	}
	if !validFollowChannel(channel) {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	create := tx.EsportsCustomerFollowUp.Create().SetCustomerID(customerID).SetAdminID(adminID).
		SetChannel(esportscustomerfollowup.Channel(channel)).SetSummary(in.Summary)
	if raw := strings.TrimSpace(in.NextFollowAt); raw != "" {
		at, err := parseReportTime(raw)
		if err != nil {
			return nil, errInvalidInput
		}
		create.SetNextFollowAt(at)
	}
	record, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsCustomer.UpdateOneID(customerID).
		SetLastContactAt(now.UTC()).AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

// listCustomerFollowUps 列出客户跟进记录（倒序），供档案页时间线展示。
func listCustomerFollowUps(ctx context.Context, db *ent.Client, adminID int, isSuper bool, customerID, limit int) ([]*ent.EsportsCustomerFollowUp, error) {
	if _, err := loadOwnedCustomer(ctx, db, adminID, isSuper, customerID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.CustomerIDEQ(customerID),
	).Order(ent.Desc(esportscustomerfollowup.FieldID)).Limit(limit).All(ctx)
}

// wecomIntakeCard 是企微自动建档接口的返回视图。
// 命中本人客户时原样透出整条档案；命中他人客户时只回最小卡片：仅保留识别信息
// （ID / 称呼 / 归属 / 来源），不带手机号、标签、消费偏好等档案字段，并用 owned_by_other
// 与中文 message 告知调用方客户已被他人负责，避免跨客服数据越界。
type wecomIntakeCard struct {
	*ent.EsportsCustomer
	OwnedByOther   bool   `json:"owned_by_other"`
	OwnerAdminName string `json:"owner_admin_name"`
	Message        string `json:"message,omitempty"`
	// MatchedBy 说明本次建档案命中的是哪个去重口径（P1-12）：
	// wecom = 「企业主体 + 外部联系人标识」幂等命中；phone = 同手机号既有客户；
	// created 为 true 表示本次新建（created 字段已表达，此处留空）。
	MatchedBy string `json:"matched_by,omitempty"`
}

// wecomIntakeCardFor 按归属决定返回整条档案还是最小卡片：仅归属当前客服本人才回传完整档案。
// 归属他人或尚未分配时只回最小卡片；本函数不写库。
func wecomIntakeCardFor(ctx context.Context, db *ent.Client, adminID int, existing *ent.EsportsCustomer) *wecomIntakeCard {
	if existing.OwnerAdminID != nil && *existing.OwnerAdminID == adminID {
		return &wecomIntakeCard{EsportsCustomer: existing}
	}
	card := &wecomIntakeCard{
		EsportsCustomer: &ent.EsportsCustomer{
			ID:           existing.ID,
			DisplayName:  existing.DisplayName,
			OwnerAdminID: existing.OwnerAdminID,
			Source:       existing.Source,
		},
		OwnedByOther: true,
	}
	if existing.OwnerAdminID == nil {
		card.Message = "该客户尚未分配给当前客服，如需协作请联系管理员"
		return card
	}
	card.OwnerAdminName = adminNames(ctx, db, []int{*existing.OwnerAdminID})[*existing.OwnerAdminID]
	if card.OwnerAdminName != "" {
		card.Message = "该客户由" + card.OwnerAdminName + "客服负责，如需协作请联系管理员"
	} else {
		card.Message = "该客户已由其他客服负责，如需协作请联系管理员"
	}
	return card
}

// wecomCorpTrustCheck 校验企微上行 corp_id 的可信性（P1-26 / P1-12 第 2 条）。
//   - 已配置 app.esports.wecom_corp_id 且与上行一致：通过，返回空提示；
//   - 已配置但不一致：返回错误（信任边界，拒绝建档）；
//   - 未配置：按既有降级策略继续建档，但返回明确的中文提示（提示调用方补齐配置），
//     与 wecom_sidebar.go 中「留空表示未配置，侧边栏降级为手动建档」的口径一致。
func wecomCorpTrustCheck(ctx context.Context, db *ent.Client, corpID string) (string, error) {
	configured := wecomConfigString(ctx, db, wecomCorpIDKey)
	if configured == "" {
		return "企微企业主体未配置（" + wecomCorpIDKey + "），本次建档按手动建档降级处理；请管理员补齐 corp_id 后重试自动建档", nil
	}
	if configured == corpID {
		return "", nil
	}
	return "", fmt.Errorf("%w：企微上行 corp_id（%s）与系统配置 %s 不一致，拒绝建档",
		errForbidden, corpID, wecomCorpIDKey)
}

// customersByPhone 返回同租户内手机号等于 phone 的客户（P1-12：手机号去重口径）。
// 空手机号不参与去重（未填手机号的档案可以有多条）；excludeID > 0 时排除该客户自身（改档场景）。
func customersByPhone(ctx context.Context, db *ent.Client, phone string, excludeID int) ([]*ent.EsportsCustomer, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return nil, nil
	}
	q := db.EsportsCustomer.Query().Where(esportscustomer.PhoneEQ(phone))
	if excludeID > 0 {
		q.Where(esportscustomer.IDNEQ(excludeID))
	}
	return q.Order(ent.Asc(esportscustomer.FieldID)).Limit(5).All(ctx)
}

// ensureCustomerPhoneUnique 校验「同一租户内手机号唯一」（P1-12）。
// 手机号为空视为不参与去重；命中他人档案时返回中文冲突提示，调用方不得写库。
func ensureCustomerPhoneUnique(ctx context.Context, db *ent.Client, phone string, selfID int) error {
	matches, err := customersByPhone(ctx, db, phone, selfID)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	if len(matches) == 1 {
		return fmt.Errorf("%w：该手机号已存在客户档案（客户 ID %d），请直接使用既有客户，勿重复建档",
			errConflict, matches[0].ID)
	}
	return fmt.Errorf("%w：该手机号存在 %d 条客户档案，请先在客户档案中合并后再建档", errConflict, len(matches))
}

// attachWecomIdentity 给「同手机号命中」的本人客户补齐企微标识（P1-12）：
// 仅当两个企微字段都为空时写入，使下一次自动建档能以「企业主体 + 外部联系人标识」直接幂等命中。
// 幂等键冲突（极端并发下已被其它档案占用）时不改写，返回原档案。
func attachWecomIdentity(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer, corpID, externalID string) (*ent.EsportsCustomer, error) {
	if customer.WecomCorpID != nil || customer.WecomExternalID != nil {
		return customer, nil
	}
	updated, err := db.EsportsCustomer.UpdateOneID(customer.ID).
		SetWecomCorpID(corpID).SetWecomExternalID(externalID).Save(ctx)
	if ent.IsConstraintError(err) {
		return customer, nil
	}
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// withWecomHint 把降级提示附到返回卡片上（已有提示时以「；」拼接，避免覆盖归属提示）。
func withWecomHint(card *wecomIntakeCard, hint string) *wecomIntakeCard {
	if card == nil || hint == "" {
		return card
	}
	if card.Message == "" {
		card.Message = hint
		return card
	}
	card.Message = card.Message + "；" + hint
	return card
}

// wecomUpsertCustomer 企微侧边栏自动建档：以「企业主体 + 外部联系人标识」组合幂等。
// 已存在且归属当前客服则原样返回既有档案（created=false），只补基础字段，不覆盖客服已维护的偏好与标签；
// 已存在但归属他人则只返回最小卡片（见 wecomIntakeCard），不泄露他人客户档案。
//
// P1-12 / P1-26 信任边界与去重：
//   - corp_id 必须与系统配置 app.esports.wecom_corp_id 一致，不一致直接拒绝（403 中文提示）；
//     未配置时降级建档并在 message 中明确提示补齐配置；
//   - 「企业主体 + 外部联系人标识」未命中时按**手机号**在同租户内去重：命中本人客户则返回既有档案
//     （并补齐企微标识，created=false），命中他人客户只回最小卡片，命中多条则显式冲突报错。
func wecomUpsertCustomer(ctx context.Context, db *ent.Client, adminID int, in wecomIntakeInput, now time.Time) (*wecomIntakeCard, bool, error) {
	in.CorpID = strings.TrimSpace(in.CorpID)
	in.ExternalID = strings.TrimSpace(in.ExternalID)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Phone = strings.TrimSpace(in.Phone)
	if in.CorpID == "" || len(in.CorpID) > 120 || in.ExternalID == "" || len(in.ExternalID) > 255 {
		return nil, false, errInvalidInput
	}
	if in.DisplayName == "" {
		in.DisplayName = "企微客户"
	}
	if len([]rune(in.DisplayName)) > 120 || len(in.Phone) > 32 {
		return nil, false, errInvalidInput
	}
	hint, err := wecomCorpTrustCheck(ctx, db, in.CorpID)
	if err != nil {
		return nil, false, err
	}
	find := func() (*ent.EsportsCustomer, error) {
		return db.EsportsCustomer.Query().Where(
			esportscustomer.WecomCorpIDEQ(in.CorpID),
			esportscustomer.WecomExternalIDEQ(in.ExternalID),
		).Only(ctx)
	}
	existing, err := find()
	if err == nil {
		card := wecomIntakeCardFor(ctx, db, adminID, existing)
		card.MatchedBy = "wecom"
		return withWecomHint(card, hint), false, nil
	}
	if !ent.IsNotFound(err) {
		return nil, false, err
	}
	// 手机号去重（P1-12）：同租户内手机号唯一，命中既有客户就不再新建。
	if byPhone, err := customersByPhone(ctx, db, in.Phone, 0); err != nil {
		return nil, false, err
	} else if len(byPhone) == 1 {
		matched := byPhone[0]
		if matched.OwnerAdminID != nil && *matched.OwnerAdminID == adminID {
			linked, err := attachWecomIdentity(ctx, db, matched, in.CorpID, in.ExternalID)
			if err != nil {
				return nil, false, err
			}
			card := wecomIntakeCardFor(ctx, db, adminID, linked)
			card.MatchedBy = "phone"
			card.Message = fmt.Sprintf("该手机号已存在客户档案（客户 ID %d），已返回既有客户，未重复建档", linked.ID)
			return withWecomHint(card, hint), false, nil
		}
		card := wecomIntakeCardFor(ctx, db, adminID, matched)
		card.MatchedBy = "phone"
		return withWecomHint(card, hint), false, nil
	} else if len(byPhone) > 1 {
		return nil, false, fmt.Errorf("%w：该手机号存在 %d 条客户档案，请先在客户档案中合并后再建档",
			errConflict, len(byPhone))
	}
	customer, err := db.EsportsCustomer.Create().SetDisplayName(in.DisplayName).
		SetOwnerAdminID(adminID).SetPhone(in.Phone).
		SetSource(esportscustomer.SourceWecom).
		SetWecomCorpID(in.CorpID).SetWecomExternalID(in.ExternalID).
		SetLastContactAt(now.UTC()).Save(ctx)
	if ent.IsConstraintError(err) {
		// 并发建档由唯一索引兜底，返回先落库的那条；归属检查与常规命中路径一致。
		existing, qerr := find()
		if qerr != nil {
			return nil, false, qerr
		}
		card := wecomIntakeCardFor(ctx, db, adminID, existing)
		card.MatchedBy = "wecom"
		return withWecomHint(card, hint), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	created := &wecomIntakeCard{EsportsCustomer: customer}
	if hint != "" {
		// 未配置 corp_id 的降级提示必须随新建档案一并回传（P1-26：降级要明确提示，不得静默）。
		created.Message = hint
	}
	return created, true, nil
}

// validateScriptInput 校验话术字段；分类可空，正文与标题必填。
func validateScriptInput(in scriptInput) (scriptInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Category = strings.TrimSpace(in.Category)
	in.Content = strings.TrimSpace(in.Content)
	if in.Title == "" || len([]rune(in.Title)) > 120 ||
		len([]rune(in.Category)) > 60 || in.Content == "" || len([]rune(in.Content)) > 2000 {
		return in, errInvalidInput
	}
	if in.Status == "" {
		in.Status = string(esportsscript.StatusEnabled)
	}
	if in.Status != string(esportsscript.StatusEnabled) && in.Status != string(esportsscript.StatusDisabled) {
		return in, errInvalidInput
	}
	return in, nil
}

func createScript(ctx context.Context, db *ent.Client, adminID int, in scriptInput) (*ent.EsportsScript, error) {
	in, err := validateScriptInput(in)
	if err != nil {
		return nil, err
	}
	return db.EsportsScript.Create().SetTitle(in.Title).SetCategory(in.Category).
		SetContent(in.Content).SetSortOrder(in.SortOrder).
		SetStatus(esportsscript.Status(in.Status)).SetCreatedBy(adminID).Save(ctx)
}

// updateScript 编辑话术走 version 乐观锁；版本不一致返回冲突。
func updateScript(ctx context.Context, db *ent.Client, id int, in scriptInput) (*ent.EsportsScript, error) {
	if in.ExpectedVersion < 1 {
		return nil, errInvalidInput
	}
	in, err := validateScriptInput(in)
	if err != nil {
		return nil, err
	}
	changed, err := db.EsportsScript.Update().Where(
		esportsscript.IDEQ(id), esportsscript.VersionEQ(in.ExpectedVersion),
	).SetTitle(in.Title).SetCategory(in.Category).SetContent(in.Content).
		SetSortOrder(in.SortOrder).SetStatus(esportsscript.Status(in.Status)).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsScript.Get(ctx, id)
}

// setScriptStatus 启停话术（启用 / 禁用）。
func setScriptStatus(ctx context.Context, db *ent.Client, id, expectedVersion int, status string) (*ent.EsportsScript, error) {
	if expectedVersion < 1 || (status != string(esportsscript.StatusEnabled) && status != string(esportsscript.StatusDisabled)) {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsScript.Update().Where(
		esportsscript.IDEQ(id), esportsscript.VersionEQ(expectedVersion),
	).SetStatus(esportsscript.Status(status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsScript.Get(ctx, id)
}

// listScripts 列出话术；category 非空时按分类过滤。
func listScripts(ctx context.Context, db *ent.Client, category string, limit int) ([]*ent.EsportsScript, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsScript.Query()
	if category = strings.TrimSpace(category); category != "" {
		q.Where(esportsscript.CategoryEQ(category))
	}
	return q.Order(ent.Asc(esportsscript.FieldSortOrder), ent.Desc(esportsscript.FieldID)).Limit(limit).All(ctx)
}

// csTodoItem 是待办提醒的一项。
type csTodoItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// countOrders 统计某履约状态下的订单数：非超管收敛到「本人负责或被授权协作」的订单，
// 与订单列表 / 详情同一可见性口径，避免协作订单在列表可见、在看板统计中丢失。
func countOrders(ctx context.Context, db *ent.Client, adminID int, isSuper bool, status esportsorder.FulfillmentStatus) (int, error) {
	q := db.EsportsOrder.Query().Where(esportsorder.FulfillmentStatusEQ(status))
	if !isSuper {
		q.Where(visibleOrdersToAdmin(adminID))
	}
	return q.Count(ctx)
}

// cstZone 是业务时区（UTC+8），用于生日礼遇匹配与提现每日次数等自然日口径，避免按 UTC 跨天错配。
var cstZone = time.FixedZone("CST", 8*60*60)

// businessDayStart 返回 now 所在业务时区（UTC+8）自然日的起始时刻（转回 UTC），供按自然日统计。
func businessDayStart(now time.Time) time.Time {
	local := now.In(cstZone)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cstZone).UTC()
}

// countCustomerBirthdaysDue 统计今日生日的归属客户数，用于客服生日礼遇待办。非超管只统计归属本人客户。
func countCustomerBirthdaysDue(ctx context.Context, db *ent.Client, adminID int, isSuper bool, now time.Time) (int, error) {
	today := now.In(cstZone).Format("01-02")
	q := db.EsportsCustomer.Query().Where(esportscustomer.BirthdayEQ(today))
	if !isSuper {
		q.Where(esportscustomer.OwnerAdminIDEQ(adminID))
	}
	return q.Count(ctx)
}

// csWorkbenchTodos 组装客服待办提醒：订单超时、待结单、售后待处理、跟进到期、生日礼遇、
// 会员优先预约、通知失败与结单后复购跟进。订单类计数与列表共用「负责 OR 协作」可见性谓词。
func csWorkbenchTodos(ctx context.Context, db *ent.Client, adminID int, isSuper bool, now time.Time) ([]csTodoItem, error) {
	overdue := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		esportsorder.PaymentDeadlineLTE(now.UTC()),
	)
	if !isSuper {
		overdue.Where(visibleOrdersToAdmin(adminID))
	}
	overdueCount, err := overdue.Count(ctx)
	if err != nil {
		return nil, err
	}
	closeCount, err := countOrders(ctx, db, adminID, isSuper, esportsorder.FulfillmentStatusPendingClose)
	if err != nil {
		return nil, err
	}
	aftersaleCount, err := db.EsportsAfterSale.Query().Where(
		esportsaftersale.HandlerAdminIDEQ(adminID),
		esportsaftersale.StatusNotIn(
			esportsaftersale.StatusCompleted, esportsaftersale.StatusRejected, esportsaftersale.StatusWithdrawn),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	// 复购跟进待办单列，因此「跟进已到期」排除复购标记记录，避免同一件事计两次。
	followCount, err := db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.AdminIDEQ(adminID),
		esportscustomerfollowup.NextFollowAtNotNil(),
		esportscustomerfollowup.NextFollowAtLTE(now.UTC()),
		esportscustomerfollowup.Not(esportscustomerfollowup.SummaryHasPrefix(repurchaseFollowUpPrefix)),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	// 生日礼遇提醒：今日生日的归属客户，提醒客服跟进。
	birthdayCount, err := countCustomerBirthdaysDue(ctx, db, adminID, isSuper, now)
	if err != nil {
		return nil, err
	}
	// 会员优先预约提醒（P1-11）：先做**过期消解**（读取时惰性判定），
	// 保证「已过期」的意向不再计入待办——不依赖定时任务注册也能自愈。
	if _, err = expirePriorityBookingIntents(ctx, db, now, 200); err != nil {
		return nil, err
	}
	priorityBookingCount, err := countPriorityBookingsPending(ctx, db, adminID, isSuper)
	if err != nil {
		return nil, err
	}
	// 通知发送失败待办：outbox status=dead 的事件，供客服人工处理重发。
	notificationFailCount, err := countFailedNotifications(ctx, db)
	if err != nil {
		return nil, err
	}
	// 结单后复购跟进待办：结单 N 天后到期、且尚未被回访的客户维度待办。
	repurchaseCount, err := countRepurchaseTodosDue(ctx, db, adminID, isSuper, now)
	if err != nil {
		return nil, err
	}
	// 结单时限超时（，T14.3 的「管理员待办」那一半）：时限未配置（close_deadline_hours=0）时计数为 0，待办整体关闭。
	// ⚠️ 口径说明：与既有的「通知发送失败」一致，该计数是**全站口径**（未按客服可见性收敛）——
	// 只暴露数量、不暴露单据；若要按客服收敛，需给 listCloseDeadlineOverdue 叠加 visibleOrdersToAdmin 谓词。
	closeDeadlineCount, err := closeDeadlineOverdueCount(ctx, db, now)
	if err != nil {
		return nil, err
	}
	// 档期确认过期待处置：已付款但档期确认超时，需客服/管理员处置（全额退还或改期）。
	appointmentPendingCount, err := appointmentPendingDisposalCount(ctx, db)
	if err != nil {
		return nil, err
	}
	return []csTodoItem{
		{Key: "overdue_payment", Label: "待付款已超时订单", Count: overdueCount},
		{Key: "pending_close", Label: "待结单", Count: closeCount},
		{Key: "pending_aftersale", Label: "售后待处理", Count: aftersaleCount},
		{Key: "due_follow_up", Label: "跟进已到期", Count: followCount},
		{Key: "customer_birthday", Label: "今日生日客户", Count: birthdayCount},
		{Key: "member_priority_booking", Label: "会员优先预约", Count: priorityBookingCount},
		{Key: "notification_failed", Label: "通知发送失败", Count: notificationFailCount},
		{Key: "repurchase_due", Label: "复购跟进待办", Count: repurchaseCount},
		{Key: "close_deadline_overdue", Label: "结单超时待处理", Count: closeDeadlineCount},
		{Key: "appointment_pending_confirm", Label: "档期待确认待处置", Count: appointmentPendingCount},
	}, nil
}

// ── 会员优先预约意向的处理与过期消解（P1-11） ──
//
// 修复前的缺口：意向实体只有 status 字段，代码里没有任何「处理 / 过期」的写入路径，
// 导致客服工作台「会员优先预约」待办只增不减。此处补齐：
//   - resolvePriorityBookingIntent：客服显式处理（已处理 / 已取消），条件更新 pending → 终态；
//   - expirePriorityBookingIntents：期望时段已过（或未填期望时段且超宽限期）仍 pending 的意向自动消解；
//   - 工作台待办在计数前调用过期消解，计数只统计 pending，因此处理后待办必然下降。

// priorityBookingExpiryGraceDays 是无期望时段意向的保留期：久未处理仍 pending 即按过期消解。
const priorityBookingExpiryGraceDays = 30

// resolvePriorityBookingIntent 客服处理会员优先预约意向（P1-11）。
// 只允许 pending → processed / cancelled 的单向条件更新：已处理 / 已取消的意向再次处理返回冲突，
// 并发处理只有一次生效。非超管只能处理归属本人客户的意向（归属隔离）。
func resolvePriorityBookingIntent(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	intentID int, status string, now time.Time) (*ent.EsportsPriorityBookingIntent, error) {
	if intentID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	var target esportsprioritybookingintent.Status
	switch strings.TrimSpace(status) {
	case string(esportsprioritybookingintent.StatusProcessed):
		target = esportsprioritybookingintent.StatusProcessed
	case string(esportsprioritybookingintent.StatusCancelled):
		target = esportsprioritybookingintent.StatusCancelled
	default:
		return nil, fmt.Errorf("%w：处理结果只能是 processed（已处理）或 cancelled（已取消）", errInvalidInput)
	}
	intent, err := db.EsportsPriorityBookingIntent.Get(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if !isSuper {
		customer, err := db.EsportsCustomer.Get(ctx, intent.CustomerID)
		if err != nil {
			return nil, err
		}
		if customer.OwnerAdminID == nil || *customer.OwnerAdminID != adminID {
			return nil, errForbidden
		}
	}
	changed, err := db.EsportsPriorityBookingIntent.Update().Where(
		esportsprioritybookingintent.IDEQ(intentID),
		esportsprioritybookingintent.StatusEQ(esportsprioritybookingintent.StatusPending),
	).SetStatus(target).SetProcessedByAdminID(adminID).SetProcessedAt(now.UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, fmt.Errorf("%w：该预约意向已处理或已取消，请刷新后重试", errConflict)
	}
	return db.EsportsPriorityBookingIntent.Get(ctx, intentID)
}

// expirePriorityBookingIntents 过期消解优先预约意向（P1-11）：
// 期望时段已过、或未填期望时段且提交超过宽限期的 pending 意向，条件更新为 cancelled
// 并记录处理时间（processed_at），使其不再计入客服待办。逐条条件更新，重复执行安全。
func expirePriorityBookingIntents(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	rows, err := db.EsportsPriorityBookingIntent.Query().Where(
		esportsprioritybookingintent.StatusEQ(esportsprioritybookingintent.StatusPending),
		esportsprioritybookingintent.Or(
			esportsprioritybookingintent.RequestedStartAtLTE(now.UTC()),
			esportsprioritybookingintent.And(
				esportsprioritybookingintent.RequestedStartAtIsNil(),
				esportsprioritybookingintent.CreatedAtLTE(now.UTC().AddDate(0, 0, -priorityBookingExpiryGraceDays)),
			),
		),
	).Order(ent.Asc(esportsprioritybookingintent.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		changed, err := db.EsportsPriorityBookingIntent.Update().Where(
			esportsprioritybookingintent.IDEQ(row.ID),
			esportsprioritybookingintent.StatusEQ(esportsprioritybookingintent.StatusPending),
		).SetStatus(esportsprioritybookingintent.StatusCancelled).
			SetProcessedAt(now.UTC()).Save(ctx)
		if err != nil {
			return count, err
		}
		if changed == 1 {
			count++
		}
	}
	return count, nil
}

// expirePriorityBookingIntentsTask 定时任务入口（P1-11）：批量过期消解预约意向。
//
// ⚠️ 需由 app.go 注册（本文件不持有注册权）：
//
//	tasks.Register("esports_expire_priority_booking_intents", expirePriorityBookingIntentsTask)
//
// 并在 cron_seeds.go 增加 CronSeed（建议每小时一次，例如 "0 5 * * * *"）。
// 未注册时过期消解仍生效——客服工作台待办在计数前会做同样的惰性判定。
func expirePriorityBookingIntentsTask(ctx context.Context, logger io.Writer) error {
	count, err := expirePriorityBookingIntents(ctx, global.ORM, time.Now(), 200)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "处理过期会员优先预约意向：%d\n", count)
	}
	return err
}
