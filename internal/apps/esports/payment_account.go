package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspaymentaccount"
	"go-admin/internal/ent/systemconfig"
)

// 收款账户与收款方式启停（§4.3）：
// 1. 收款账户必须为工作室主体名下，经财务审核后才可启用；待审核 / 驳回 / 停用账户不可用于核销；
// 2. 客服可提交账户资料但不可自行启用（启用需财务权限，路由层以独立权限码约束）；
// 3. 各类收款方式可在后台按渠道启停，停用后不接受新的线下到账凭证。

// externalPaymentChannelValues 是需走收款账户与到账凭证的外部渠道。
var externalPaymentChannelValues = []string{"wechat_work", "merchant_qr", "corporate_transfer"}

// internalPaymentChannelValues 是站内扣款方式（余额）：无收款账户，但也须纳入启停
// 与核销开关口径（§4.3-08），停用后不再接受以该方式收款。
// 储值卡（stored_value_card）已随该功能下线（2026-10-20），不再作为可启停的站内方式。
var internalPaymentChannelValues = []string{"customer_balance"}

// paymentChannelValues 是可启停的收款方式全集（外部 + 站内）。
var paymentChannelValues = []string{
	"wechat_work", "merchant_qr", "corporate_transfer", "customer_balance",
}

// validPaymentChannel 判断是否为需收款账户的外部渠道。
func validPaymentChannel(channel string) bool {
	for _, value := range externalPaymentChannelValues {
		if value == channel {
			return true
		}
	}
	return false
}

// toggleablePaymentChannel 判断该收款方式是否可被后台启停（含站内的余额，§4.3-08）。
func toggleablePaymentChannel(channel string) bool {
	for _, value := range paymentChannelValues {
		if value == channel {
			return true
		}
	}
	return false
}

// paymentChannelConfigKey 收款方式启停配置项键：值 "enabled" / "disabled"，缺省视为启用。
func paymentChannelConfigKey(channel string) string {
	return "app.esports.payment_channel." + channel
}

// paymentChannelEnabled 查询收款方式是否启用（缺省启用，缺失或读取异常都按启用处理以免阻断收款）。
func paymentChannelEnabled(ctx context.Context, db *ent.Client, channel string) bool {
	item, err := db.SystemConfig.Query().
		Where(systemconfig.KeyEQ(paymentChannelConfigKey(channel))).Only(ctx)
	if err != nil {
		return true
	}
	return strings.TrimSpace(item.Value) != "disabled"
}

// setPaymentChannelEnabled 启停收款方式（写入系统配置项，供后台维护）。
func setPaymentChannelEnabled(ctx context.Context, db *ent.Client, channel string, enabled bool) error {
	if !toggleablePaymentChannel(channel) {
		return errInvalidInput
	}
	value := "disabled"
	if enabled {
		value = "enabled"
	}
	key := paymentChannelConfigKey(channel)
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		_, err = db.SystemConfig.Create().SetKey(key).
			SetName("收款方式启停：" + channel).SetValue(value).
			SetType("string").SetGroup("esports").SetStatus(1).Save(ctx)
		return err
	}
	_, err = db.SystemConfig.UpdateOneID(item.ID).SetValue(value).Save(ctx)
	return err
}

// paymentChannelView 收款方式启停状态视图。
type paymentChannelView struct {
	Channel string `json:"channel"`
	Enabled bool   `json:"enabled"`
	// 启用中的收款账户数量，便于后台判断是否可直接收款。
	ActiveAccounts int `json:"active_accounts"`
}

// listPaymentChannels 列出三类收款方式的启停状态与启用账户数。
func listPaymentChannels(ctx context.Context, db *ent.Client) ([]paymentChannelView, error) {
	counts, err := db.EsportsPaymentAccount.Query().
		Where(esportspaymentaccount.StatusEQ(esportspaymentaccount.StatusActive)).
		Select(esportspaymentaccount.FieldChannel).All(ctx)
	if err != nil {
		return nil, err
	}
	active := map[string]int{}
	for _, row := range counts {
		active[row.Channel.String()]++
	}
	views := make([]paymentChannelView, 0, len(paymentChannelValues))
	for _, channel := range paymentChannelValues {
		views = append(views, paymentChannelView{
			Channel: channel, Enabled: paymentChannelEnabled(ctx, db, channel),
			ActiveAccounts: active[channel],
		})
	}
	return views, nil
}

type paymentAccountInput struct {
	Channel     string `json:"channel"`
	SubjectName string `json:"subject_name"`
	AccountNo   string `json:"account_no"`
	QrURL       string `json:"qr_url"`
	BankName    string `json:"bank_name"`
	SubmitNote  string `json:"submit_note"`
}

// paymentSubjectWhitelistKey 是允许作为收款主体的主体名白名单（逗号分隔，§4.3-07）。
// 未配置（缺行 / 留空 / 全空白）时**一律拒绝**：默认拒绝是 §4.3-07 的落地口径，
// 否则任何主体名都能提交、再经财务审核被启用为收款账户，等于该控制默认关闭。
const paymentSubjectWhitelistKey = "app.esports.payment_subject_whitelist"

// corporateTransferThresholdKey 是「大额订单必须走对公转账」的金额阈值（分，§4.3-05）。
// 缺省 0 表示不限制渠道；配置为正整数后，金额达到阈值的订单只能用对公转账收款。
const corporateTransferThresholdKey = "app.esports.corporate_transfer_threshold_cents"

// paymentSubjectWhitelist 读取收款主体白名单（已去空白与去空项）。未配置返回 nil。
func paymentSubjectWhitelist(ctx context.Context, db *ent.Client) []string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(paymentSubjectWhitelistKey)).Only(ctx)
	if err != nil {
		return nil
	}
	return splitConfigList(item.Value)
}

// splitConfigList 按逗号切分配置项文本，去空白与空项。
func splitConfigList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ensurePaymentSubjectAllowed 校验收款主体落在白名单内（§4.3-07），fail-closed：
// 白名单未配置（缺行 / 留空 / 全空白）时直接拒绝并提示配置键，避免任意主体名经财务审核被启用为收款账户；
// 白名单非空时主体名须与名单项完全匹配。提交（createPaymentAccount）与批准（reviewPaymentAccount）都须走本校验。
func ensurePaymentSubjectAllowed(ctx context.Context, db *ent.Client, subject string) error {
	allowed := paymentSubjectWhitelist(ctx, db)
	if len(allowed) == 0 {
		return fmt.Errorf("%w：未配置收款主体白名单（app.esports.payment_subject_whitelist），请先在系统配置中登记工作室主体后再提交或启用收款账户", errInvalidInput)
	}
	subject = strings.TrimSpace(subject)
	for _, name := range allowed {
		if name == subject {
			return nil
		}
	}
	return fmt.Errorf("%w：收款主体「%s」不在工作室主体白名单内", errInvalidInput, subject)
}

// corporateTransferThresholdCents 读取大额走对公的阈值（分，§4.3-05）。未配置或非法返回 0（不限制）。
func corporateTransferThresholdCents(ctx context.Context, db *ent.Client) int64 {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(corporateTransferThresholdKey)).Only(ctx)
	if err != nil {
		return 0
	}
	value, convErr := strconv.ParseInt(strings.TrimSpace(item.Value), 10, 64)
	if convErr != nil || value < 0 {
		return 0
	}
	return value
}

// ensureCorporateChannelForAmount 大额订单渠道约束（§4.3-05）：金额达到阈值时只允许对公转账，
// 避免大额资金走企微 / 商户码等小额渠道。阈值未配置时不限制。
func ensureCorporateChannelForAmount(ctx context.Context, db *ent.Client, channel string, amountCents int64) error {
	threshold := corporateTransferThresholdCents(ctx, db)
	if threshold <= 0 || amountCents < threshold || channel == "corporate_transfer" {
		return nil
	}
	return fmt.Errorf("%w：金额达到 %d 分的订单须走对公转账（当前收款方式 %s）",
		errInvalidInput, threshold, customerPaymentMethodName[channel])
}

// createPaymentAccount 提交收款账户资料（§4.3）：统一进入待财务审核，不允许直接启用。
func createPaymentAccount(ctx context.Context, db *ent.Client, in paymentAccountInput,
	adminID int, now time.Time) (*ent.EsportsPaymentAccount, error) {
	in.Channel = strings.TrimSpace(in.Channel)
	in.SubjectName = strings.TrimSpace(in.SubjectName)
	in.AccountNo = strings.TrimSpace(in.AccountNo)
	in.QrURL = strings.TrimSpace(in.QrURL)
	in.BankName = strings.TrimSpace(in.BankName)
	in.SubmitNote = strings.TrimSpace(in.SubmitNote)
	if adminID < 1 || !validPaymentChannel(in.Channel) || in.SubjectName == "" ||
		len([]rune(in.SubjectName)) > 120 || len([]rune(in.AccountNo)) > 128 ||
		len([]rune(in.QrURL)) > 255 || len([]rune(in.BankName)) > 120 ||
		len([]rune(in.SubmitNote)) > 255 {
		return nil, errInvalidInput
	}
	if in.Channel == "corporate_transfer" && in.AccountNo == "" {
		return nil, errInvalidInput
	}
	if (in.Channel == "merchant_qr" || in.Channel == "wechat_work") && in.QrURL == "" && in.AccountNo == "" {
		return nil, errInvalidInput
	}
	// §4.3-07：收款主体须为工作室主体之一（fail-closed：白名单未配置时不接受任何主体）。
	if err := ensurePaymentSubjectAllowed(ctx, db, in.SubjectName); err != nil {
		return nil, err
	}
	item, err := db.EsportsPaymentAccount.Create().
		SetChannel(esportspaymentaccount.Channel(in.Channel)).
		SetSubjectName(in.SubjectName).SetAccountNo(in.AccountNo).SetQrURL(in.QrURL).
		SetBankName(in.BankName).SetSubmitNote(in.SubmitNote).
		SetStatus(esportspaymentaccount.StatusPendingReview).
		SetSubmittedByAdminID(adminID).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return item, nil
}

// reviewPaymentAccount 财务审核收款账户（§4.3）：待审核 → 启用 / 驳回；提交人不得自审。
// 批准前按**当前**收款主体白名单重新校验（§4.3-07），白名单在提交后被改小 / 清空时不得过审。
func reviewPaymentAccount(ctx context.Context, db *ent.Client, id, adminID int,
	approve bool, resolution string, now time.Time) (*ent.EsportsPaymentAccount, error) {
	resolution = strings.TrimSpace(resolution)
	if id < 1 || adminID < 1 || len([]rune(resolution)) > 255 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsPaymentAccount.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportspaymentaccount.StatusPendingReview {
		return nil, errConflict
	}
	if item.SubmittedByAdminID != nil && *item.SubmittedByAdminID == adminID {
		return nil, errForbidden
	}
	if !approve && resolution == "" {
		return nil, errInvalidInput
	}
	// §4.3-07：批准时必须按当前白名单重新校验主体。提交之后白名单可能被改小或清空，
	// 若只在提交时校验，旧资料仍会被过审启用为收款账户；驳回路径不需要该校验。
	if approve {
		if err = ensurePaymentSubjectAllowed(ctx, db, item.SubjectName); err != nil {
			return nil, err
		}
	}
	status := esportspaymentaccount.StatusActive
	if !approve {
		status = esportspaymentaccount.StatusRejected
	}
	changed, err := db.EsportsPaymentAccount.Update().Where(
		esportspaymentaccount.IDEQ(id),
		esportspaymentaccount.StatusEQ(esportspaymentaccount.StatusPendingReview),
	).SetStatus(status).SetReviewedByAdminID(adminID).SetReviewedAt(now.UTC()).
		SetReviewResolution(resolution).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPaymentAccount.Get(ctx, id)
}

// disablePaymentAccount 停用收款账户：停用后不再接受新的到账核销。
func disablePaymentAccount(ctx context.Context, db *ent.Client, id int, now time.Time) (*ent.EsportsPaymentAccount, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsPaymentAccount.Update().Where(
		esportspaymentaccount.IDEQ(id),
		esportspaymentaccount.StatusEQ(esportspaymentaccount.StatusActive),
	).SetStatus(esportspaymentaccount.StatusDisabled).SetDisabledAt(now.UTC()).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPaymentAccount.Get(ctx, id)
}

// listPaymentAccounts 列出收款账户，可按渠道 / 状态筛选。
func listPaymentAccounts(ctx context.Context, db *ent.Client, channel, status string, limit int) ([]*ent.EsportsPaymentAccount, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	q := db.EsportsPaymentAccount.Query()
	if channel = strings.TrimSpace(channel); channel != "" {
		if !validPaymentChannel(channel) {
			return nil, errInvalidInput
		}
		q.Where(esportspaymentaccount.ChannelEQ(esportspaymentaccount.Channel(channel)))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_review", "active", "rejected", "disabled":
			q.Where(esportspaymentaccount.StatusEQ(esportspaymentaccount.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(ent.Desc(esportspaymentaccount.FieldID)).Limit(limit).All(ctx)
}

// requireActivePaymentAccount 取启用中的收款账户用于核销；渠道不匹配或未启用一律拒绝。
func requireActivePaymentAccount(ctx context.Context, db *ent.Client, id int,
	channel string) (*ent.EsportsPaymentAccount, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsPaymentAccount.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Channel.String() != channel {
		return nil, errInvalidInput
	}
	if item.Status != esportspaymentaccount.StatusActive {
		return nil, errForbidden
	}
	return item, nil
}
