package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportscustomerfollowup"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsscript"
	"go-admin/internal/ent/esportswallettx"
)

// 客服端（§8）常量：标签数量 / 长度上限与话术状态。
const (
	csMaxTags   = 20
	csMaxTagLen = 30
)

// customerProfileInput 客户档案可编辑字段（§8.3）。跟进状态可选传，空表示不改。
type customerProfileInput struct {
	Phone                 string   `json:"phone"`
	Birthday              string   `json:"birthday"`
	ConsumptionPreference string   `json:"consumption_preference"`
	Tags                  []string `json:"tags"`
	FollowStatus          string   `json:"follow_status"`
}

// followUpInput 跟进记录输入（§8.3）。
type followUpInput struct {
	Channel      string `json:"channel"`
	Summary      string `json:"summary"`
	NextFollowAt string `json:"next_follow_at"`
}

// scriptInput 话术库输入（§8.2）。
type scriptInput struct {
	Title           string `json:"title"`
	Category        string `json:"category"`
	Content         string `json:"content"`
	SortOrder       int    `json:"sort_order"`
	Status          string `json:"status"`
	ExpectedVersion int    `json:"expected_version"`
}

// wecomIntakeInput 企微侧边栏自动建档输入（§8.4）。
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

// loadOwnedCustomer 取客户并校验归属：仅归属客服本人或超级管理员可见（§8.2 按归属隔离）。
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

// updateCustomerProfile 更新客户档案扩展字段（§8.3）：手机号、消费偏好、标签、跟进状态。
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
	update := db.EsportsCustomer.UpdateOneID(customerID).
		SetPhone(in.Phone).SetBirthday(in.Birthday).
		SetConsumptionPreference(in.ConsumptionPreference).SetTags(tags).AddVersion(1)
	if in.FollowStatus != "" {
		if !validFollowStatus(in.FollowStatus) {
			return nil, errInvalidInput
		}
		update.SetFollowStatus(esportscustomer.FollowStatus(in.FollowStatus))
	}
	if _, err := update.Save(ctx); err != nil {
		return nil, err
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

// customerProfileOverview 汇总客户档案（§8.2）：基本信息、消费 / 充值 / 送礼 / 余额 / 成长值。
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

// addCustomerFollowUp 写跟进记录并在同一事务刷新客户最近联系时间（§8.3）。
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

// wecomUpsertCustomer 企微侧边栏自动建档（§8.4）：以「企业主体 + 外部联系人标识」组合幂等。
// 已存在则返回既有档案（created=false），只补基础字段，不覆盖客服已维护的偏好与标签。
func wecomUpsertCustomer(ctx context.Context, db *ent.Client, adminID int, in wecomIntakeInput, now time.Time) (*ent.EsportsCustomer, bool, error) {
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
	find := func() (*ent.EsportsCustomer, error) {
		return db.EsportsCustomer.Query().Where(
			esportscustomer.WecomCorpIDEQ(in.CorpID),
			esportscustomer.WecomExternalIDEQ(in.ExternalID),
		).Only(ctx)
	}
	existing, err := find()
	if err == nil {
		return existing, false, nil
	}
	if !ent.IsNotFound(err) {
		return nil, false, err
	}
	customer, err := db.EsportsCustomer.Create().SetDisplayName(in.DisplayName).
		SetOwnerAdminID(adminID).SetPhone(in.Phone).
		SetSource(esportscustomer.SourceWecom).
		SetWecomCorpID(in.CorpID).SetWecomExternalID(in.ExternalID).
		SetLastContactAt(now.UTC()).Save(ctx)
	if ent.IsConstraintError(err) {
		// 并发建档由唯一索引兜底，返回先落库的那条。
		existing, qerr := find()
		if qerr != nil {
			return nil, false, qerr
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return customer, true, nil
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

// csTodoItem 是待办提醒的一项（§8.2）。
type csTodoItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// countOrders 统计某履约状态下的订单数，非超管收敛到本人负责的订单。
func countOrders(ctx context.Context, db *ent.Client, adminID int, isSuper bool, status esportsorder.FulfillmentStatus) (int, error) {
	q := db.EsportsOrder.Query().Where(esportsorder.FulfillmentStatusEQ(status))
	if !isSuper {
		q.Where(esportsorder.ResponsibleAdminIDEQ(adminID))
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

// countCustomerBirthdaysDue 统计今日生日的归属客户数，用于客服生日礼遇待办（§10.4）。非超管只统计归属本人客户。
func countCustomerBirthdaysDue(ctx context.Context, db *ent.Client, adminID int, isSuper bool, now time.Time) (int, error) {
	today := now.In(cstZone).Format("01-02")
	q := db.EsportsCustomer.Query().Where(esportscustomer.BirthdayEQ(today))
	if !isSuper {
		q.Where(esportscustomer.OwnerAdminIDEQ(adminID))
	}
	return q.Count(ctx)
}

// csWorkbenchTodos 组装客服待办提醒（§8.2）：订单超时、待结单、售后待处理、跟进到期、储值卡到期、生日礼遇（§10.4）。
func csWorkbenchTodos(ctx context.Context, db *ent.Client, adminID int, isSuper bool, now time.Time) ([]csTodoItem, error) {
	overdue := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		esportsorder.PaymentDeadlineLTE(now.UTC()),
	)
	if !isSuper {
		overdue.Where(esportsorder.ResponsibleAdminIDEQ(adminID))
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
	followCount, err := db.EsportsCustomerFollowUp.Query().Where(
		esportscustomerfollowup.AdminIDEQ(adminID),
		esportscustomerfollowup.NextFollowAtNotNil(),
		esportscustomerfollowup.NextFollowAtLTE(now.UTC()),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	// 储值卡临近到期提醒（§4.4）：让客服在卡过期前跟进客户。
	storedValueDueCount, err := countStoredValueCardsDue(ctx, db, adminID, isSuper, now, storedValueDueDays)
	if err != nil {
		return nil, err
	}
	// 生日礼遇提醒（§10.4）：今日生日的归属客户，提醒客服跟进。
	birthdayCount, err := countCustomerBirthdaysDue(ctx, db, adminID, isSuper, now)
	if err != nil {
		return nil, err
	}
	// 通知发送失败待办（§通知）：outbox status=dead 的事件，供客服人工处理重发。
	notificationFailCount, err := countFailedNotifications(ctx, db)
	if err != nil {
		return nil, err
	}
	return []csTodoItem{
		{Key: "overdue_payment", Label: "待付款已超时订单", Count: overdueCount},
		{Key: "pending_close", Label: "待结单", Count: closeCount},
		{Key: "pending_aftersale", Label: "售后待处理", Count: aftersaleCount},
		{Key: "due_follow_up", Label: "跟进已到期", Count: followCount},
		{Key: "stored_value_due", Label: "储值卡临近到期", Count: storedValueDueCount},
		{Key: "customer_birthday", Label: "今日生日客户", Count: birthdayCount},
		{Key: "notification_failed", Label: "通知发送失败", Count: notificationFailCount},
	}, nil
}
