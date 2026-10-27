package esports

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsrealnameattempt"
	"go-admin/internal/ent/esportsrealnameverification"
	"go-admin/internal/global"
)

// 统一实名认证：客户与陪玩共用一套档案（EsportsRealNameVerification）与流水
// （EsportsRealNameAttempt）。
//
// 状态机：无 →（提交）→ pending →（同步核验 / 回调 / 轮询 / 人工审核）→ verified / rejected；
// pending 超时（非 manual）→ expired。rejected / expired 可原地回到 pending。
// 业务前置守卫（下单 / 充值 / 提现）默认关闭，开关开启后才拦截。

const (
	// 配置键必须为字面量，config_declaration_test 依赖此形式识别读取项。
	realnameRequiredOrderKey        = "app.esports.realname_required_order"
	realnameRequiredRechargeKey     = "app.esports.realname_required_recharge"
	realnameRequiredWithdrawalKey   = "app.esports.realname_required_withdrawal"
	realnameDailyAttemptLimitKey    = "app.esports.realname_daily_attempt_limit"
	realnamePendingTimeoutMinKey    = "app.esports.realname_pending_timeout_minutes"
	realnameDefaultDailyAttempts    = 5
	realnameDefaultPendingTimeout   = 30
	realnameExpireScanBatchSize     = 200
	realnameSubjectTypeCustomer     = "customer"
	realnameSubjectTypePlayer       = "player"
	realNameScopeOrder              = "order"
	realNameScopeRecharge           = "recharge"
	realNameScopeWithdrawal         = "withdrawal"
)

// realNameRequiredError 业务前置未实名：handler 映射为 403 + error_code=realname_required + scope。
type realNameRequiredError struct{ Scope string }

func (e *realNameRequiredError) Error() string { return errRealNameRequired.Error() }
func (e *realNameRequiredError) Unwrap() error { return errRealNameRequired }

// realnameRequiredFor 读取某业务场景的实名开关：配置缺失或非法一律按关闭处理（不阻断交易）。
func realnameRequiredFor(ctx context.Context, db *ent.Client, scope string) bool {
	switch scope {
	case realNameScopeOrder:
		return orderGuardConfigBool(ctx, db, realnameRequiredOrderKey, false)
	case realNameScopeRecharge:
		return orderGuardConfigBool(ctx, db, realnameRequiredRechargeKey, false)
	case realNameScopeWithdrawal:
		return orderGuardConfigBool(ctx, db, realnameRequiredWithdrawalKey, false)
	default:
		return false
	}
}

// realnameDailyAttemptLimit 每日提交次数上限：0 表示不限。
func realnameDailyAttemptLimit(ctx context.Context, db *ent.Client) int {
	return assessmentConfigIntAllowZero(ctx, db, realnameDailyAttemptLimitKey, realnameDefaultDailyAttempts)
}

// realnamePendingTimeoutMinutes 待审有效期（分钟）。
func realnamePendingTimeoutMinutes(ctx context.Context, db *ent.Client) int {
	minutes := assessmentConfigInt(ctx, db, realnamePendingTimeoutMinKey, realnameDefaultPendingTimeout)
	if minutes < 1 {
		return realnameDefaultPendingTimeout
	}
	return minutes
}

// validRealNameSubjectType 主体类型只允许客户 / 陪玩，由路由决定、不由请求参数决定。
func validRealNameSubjectType(subjectType string) bool {
	return subjectType == realnameSubjectTypeCustomer || subjectType == realnameSubjectTypePlayer
}

// realNameVerificationFor 取主体实名档案；无记录返回 nil。
func realNameVerificationFor(ctx context.Context, db *ent.Client, subjectType string, subjectID int) (*ent.EsportsRealNameVerification, error) {
	record, err := db.EsportsRealNameVerification.Query().Where(
		esportsrealnameverification.SubjectTypeEQ(esportsrealnameverification.SubjectType(subjectType)),
		esportsrealnameverification.SubjectIDEQ(subjectID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

// isRealNameVerified 判断主体是否已实名。
func isRealNameVerified(ctx context.Context, db *ent.Client, subjectType string, subjectID int) (bool, error) {
	exists, err := db.EsportsRealNameVerification.Query().Where(
		esportsrealnameverification.SubjectTypeEQ(esportsrealnameverification.SubjectType(subjectType)),
		esportsrealnameverification.SubjectIDEQ(subjectID),
		esportsrealnameverification.StatusEQ(esportsrealnameverification.StatusVerified),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// ensureRealNameVerified 业务前置守卫：开关关闭直接放行；开启且未实名返回 realNameRequiredError。
func ensureRealNameVerified(ctx context.Context, db *ent.Client, subjectType string, subjectID int, scope string) error {
	if !realnameRequiredFor(ctx, db, scope) {
		return nil
	}
	verified, err := isRealNameVerified(ctx, db, subjectType, subjectID)
	if err != nil {
		return err
	}
	if verified {
		return nil
	}
	return &realNameRequiredError{Scope: scope}
}

// realNameSubmitInput 用户端 / 管理端代提共用的提交入参。证件号只在请求生命周期内使用。
type realNameSubmitInput struct {
	RealName string `json:"real_name"`
	IDNumber string `json:"id_number"`
	Channel  string `json:"channel"`
	Method   string `json:"method"`
}

// realNameSubmitView 提交结果：异步核验时带跳转地址 / 二维码与轮询流水号。
type realNameSubmitView struct {
	ID          int    `json:"id"`
	Status      string `json:"status"`
	Channel     string `json:"channel"`
	Method      string `json:"method"`
	AttemptRef  string `json:"attempt_ref,omitempty"`
	RedirectURL string `json:"redirect_url,omitempty"`
	QRContent   string `json:"qr_content,omitempty"`
	Message     string `json:"message,omitempty"`
}

// realNameReviewInput 人工审核入参。
type realNameReviewInput struct {
	Approved     bool   `json:"approved"`
	RejectReason string `json:"reject_reason"`
}

// realNameStatusView 状态查询出参：含可选渠道，前端据此渲染。
type realNameStatusView struct {
	SubjectType    string                  `json:"subject_type"`
	SubjectID      int                     `json:"subject_id"`
	Status         string                  `json:"status"`
	RealName       string                  `json:"real_name"`
	IDNumberMasked string                  `json:"id_number_masked"`
	Channel        string                  `json:"channel"`
	Method         string                  `json:"method"`
	FailReason     string                  `json:"fail_reason"`
	VerifiedAt     *time.Time              `json:"verified_at"`
	AttemptRef     string                  `json:"attempt_ref,omitempty"`
	Channels       []realNameChannelOption `json:"channels"`
}

// realNameStatusFor 组装主体实名状态；未提交时 Status 为空串。
func realNameStatusFor(ctx context.Context, db *ent.Client, subjectType string, subjectID int) (*realNameStatusView, error) {
	view := &realNameStatusView{
		SubjectType: subjectType, SubjectID: subjectID,
		Channels: realNameProvidersFor(ctx, db).options,
	}
	record, err := realNameVerificationFor(ctx, db, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return view, nil
	}
	view.Status = string(record.Status)
	view.RealName = record.RealName
	view.IDNumberMasked = record.IDNumberMasked
	view.Channel = string(record.Channel)
	view.Method = string(record.Method)
	view.FailReason = record.FailReason
	view.VerifiedAt = record.VerifiedAt
	if record.Status == esportsrealnameverification.StatusPending {
		attempt, err := db.EsportsRealNameAttempt.Query().Where(
			esportsrealnameattempt.SubjectTypeEQ(esportsrealnameattempt.SubjectType(subjectType)),
			esportsrealnameattempt.SubjectIDEQ(subjectID),
			esportsrealnameattempt.StatusEQ(esportsrealnameattempt.StatusPending),
		).Order(ent.Desc(esportsrealnameattempt.FieldID)).First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if attempt != nil && attempt.ProviderRef != nil {
			view.AttemptRef = *attempt.ProviderRef
		}
	}
	return view, nil
}

// submitRealName 提交实名认证：校验 → 调用渠道 → 单事务落档案与流水。
// 已实名 / 审核中重复提交返回 409；渠道失败落 rejected 并返回 400；
// 异步核验保持 pending 并返回跳转地址。
func submitRealName(ctx context.Context, db *ent.Client, subjectType string, subjectID, requestedBy int, in realNameSubmitInput, now time.Time) (*realNameSubmitView, error) {
	in.RealName = strings.TrimSpace(in.RealName)
	in.IDNumber = strings.TrimSpace(in.IDNumber)
	in.Channel = strings.TrimSpace(in.Channel)
	in.Method = strings.TrimSpace(in.Method)
	if !validRealNameSubjectType(subjectType) || subjectID < 1 || in.RealName == "" ||
		len([]rune(in.RealName)) > 120 || !validIDNumber(in.IDNumber) || in.Channel == "" || in.Method == "" {
		return nil, errInvalidInput
	}
	existing, err := realNameVerificationFor(ctx, db, subjectType, subjectID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		switch existing.Status {
		case esportsrealnameverification.StatusVerified:
			return nil, fmt.Errorf("%w：已完成实名认证，如需变更请联系客服", errConflict)
		case esportsrealnameverification.StatusPending:
			return nil, fmt.Errorf("%w：认证审核中，请勿重复提交", errConflict)
		}
	}
	// 每日次数上限：按 UTC+8 自然日统计流水条数，0 表示不限。
	limit := realnameDailyAttemptLimit(ctx, db)
	if limit > 0 {
		count, err := db.EsportsRealNameAttempt.Query().Where(
			esportsrealnameattempt.SubjectTypeEQ(esportsrealnameattempt.SubjectType(subjectType)),
			esportsrealnameattempt.SubjectIDEQ(subjectID),
			esportsrealnameattempt.CreatedAtGTE(businessDayStart(now)),
		).Count(ctx)
		if err != nil {
			return nil, err
		}
		if count >= limit {
			return nil, fmt.Errorf("%w：今日实名提交次数已达上限，请明天再试", errConflict)
		}
	}
	provider, err := realNameProvidersFor(ctx, db).get(in.Channel, in.Method)
	if err != nil {
		return nil, err
	}
	result, err := provider.Start(ctx, realNameProviderInput{
		SubjectType: subjectType, SubjectID: subjectID,
		RealName: in.RealName, IDNumber: in.IDNumber, Method: in.Method,
	})
	if err != nil {
		return nil, err
	}
	status := esportsrealnameverification.StatusPending
	attemptStatus := esportsrealnameattempt.StatusPending
	failReason := ""
	switch result.Outcome {
	case realNameOutcomePassed:
		status = esportsrealnameverification.StatusVerified
		attemptStatus = esportsrealnameattempt.StatusPassed
	case realNameOutcomeFailed:
		status = esportsrealnameverification.StatusRejected
		attemptStatus = esportsrealnameattempt.StatusFailed
		failReason = result.Message
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	recordID, err := writeRealNameSubmission(ctx, tx, subjectType, subjectID, requestedBy, in, status, failReason, result, now)
	if err != nil {
		return nil, err
	}
	createAttempt := tx.EsportsRealNameAttempt.Create().
		SetSubjectType(esportsrealnameattempt.SubjectType(subjectType)).
		SetSubjectID(subjectID).
		SetChannel(esportsrealnameattempt.Channel(in.Channel)).
		SetMethod(esportsrealnameattempt.Method(in.Method)).
		SetStatus(attemptStatus).
		SetFailReason(failReason).
		SetRequestedBy(requestedBy)
	if result.ProviderRef != "" {
		createAttempt = createAttempt.SetProviderRef(result.ProviderRef)
	}
	if _, err := createAttempt.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if status == esportsrealnameverification.StatusRejected {
		return nil, fmt.Errorf("%w：%s", errRealNameRejected, failReason)
	}
	return &realNameSubmitView{
		ID: recordID, Status: string(status), Channel: in.Channel, Method: in.Method,
		AttemptRef: result.ProviderRef, RedirectURL: result.RedirectURL,
		QRContent: result.QRContent, Message: result.Message,
	}, nil
}

// writeRealNameSubmission 在事务内写档案：无记录则新建，rejected / expired 原地回写，
// 用 status CAS 防止并发提交互相覆盖。
func writeRealNameSubmission(ctx context.Context, tx *ent.Tx, subjectType string, subjectID, requestedBy int,
	in realNameSubmitInput, status esportsrealnameverification.Status, failReason string,
	result *realNameProviderResult, now time.Time) (int, error) {
	existing, err := tx.EsportsRealNameVerification.Query().Where(
		esportsrealnameverification.SubjectTypeEQ(esportsrealnameverification.SubjectType(subjectType)),
		esportsrealnameverification.SubjectIDEQ(subjectID),
	).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return 0, err
	}
	if existing != nil {
		if existing.Status == esportsrealnameverification.StatusVerified ||
			existing.Status == esportsrealnameverification.StatusPending {
			return 0, errConflict
		}
		upd := tx.EsportsRealNameVerification.Update().
			Where(
				esportsrealnameverification.IDEQ(existing.ID),
				esportsrealnameverification.StatusIn(
					esportsrealnameverification.StatusRejected,
					esportsrealnameverification.StatusExpired,
				),
			).
			SetRealName(in.RealName).
			SetIDNumberMasked(maskIDNumber(in.IDNumber)).
			SetChannel(esportsrealnameverification.Channel(in.Channel)).
			SetMethod(esportsrealnameverification.Method(in.Method)).
			SetStatus(status).
			SetFailReason(failReason).
			ClearReviewedByAdminID()
		if result.ProviderRef != "" {
			upd = upd.SetProviderRef(result.ProviderRef)
		} else {
			upd = upd.ClearProviderRef()
		}
		if status == esportsrealnameverification.StatusVerified {
			upd = upd.SetVerifiedAt(now.UTC())
		} else {
			upd = upd.ClearVerifiedAt()
		}
		changed, err := upd.Save(ctx)
		if err != nil {
			return 0, err
		}
		if changed != 1 {
			return 0, errConflict
		}
		return existing.ID, nil
	}
	create := tx.EsportsRealNameVerification.Create().
		SetSubjectType(esportsrealnameverification.SubjectType(subjectType)).
		SetSubjectID(subjectID).
		SetRealName(in.RealName).
		SetIDNumberMasked(maskIDNumber(in.IDNumber)).
		SetChannel(esportsrealnameverification.Channel(in.Channel)).
		SetMethod(esportsrealnameverification.Method(in.Method)).
		SetStatus(status).
		SetFailReason(failReason).
		SetRequestedBy(requestedBy)
	if result.ProviderRef != "" {
		create = create.SetProviderRef(result.ProviderRef)
	}
	if status == esportsrealnameverification.StatusVerified {
		create = create.SetVerifiedAt(now.UTC())
	}
	record, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return 0, errConflict
		}
		return 0, err
	}
	return record.ID, nil
}

// reviewRealName 管理员人工审核：通过置 verified，驳回置 rejected。
// 同时把该主体所有 pending 流水置 abandoned，防止后续回调覆盖人工结论。
func reviewRealName(ctx context.Context, db *ent.Client, reviewerID, id int, in realNameReviewInput, now time.Time) (*ent.EsportsRealNameVerification, error) {
	in.RejectReason = strings.TrimSpace(in.RejectReason)
	if reviewerID < 1 || id < 1 || (!in.Approved && in.RejectReason == "") || len([]rune(in.RejectReason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := tx.EsportsRealNameVerification.Query().Where(
		esportsrealnameverification.IDEQ(id),
		esportsrealnameverification.StatusEQ(esportsrealnameverification.StatusPending),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	status := esportsrealnameverification.StatusRejected
	failReason := in.RejectReason
	if in.Approved {
		status = esportsrealnameverification.StatusVerified
		failReason = ""
	}
	upd := tx.EsportsRealNameVerification.Update().
		Where(
			esportsrealnameverification.IDEQ(record.ID),
			esportsrealnameverification.StatusEQ(esportsrealnameverification.StatusPending),
		).
		SetStatus(status).
		SetFailReason(failReason).
		SetReviewedByAdminID(reviewerID)
	if in.Approved {
		upd = upd.SetVerifiedAt(now.UTC())
	} else {
		upd = upd.ClearVerifiedAt()
	}
	changed, err := upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err := tx.EsportsRealNameAttempt.Update().
		Where(
			esportsrealnameattempt.SubjectTypeEQ(esportsrealnameattempt.SubjectType(string(record.SubjectType))),
			esportsrealnameattempt.SubjectIDEQ(record.SubjectID),
			esportsrealnameattempt.StatusEQ(esportsrealnameattempt.StatusPending),
		).
		SetStatus(esportsrealnameattempt.StatusAbandoned).
		Save(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsRealNameVerification.Get(ctx, record.ID)
}

// settleRealNameAttempt 回调 / 轮询落定：按 (channel, provider_ref) 找 pending 流水，
// 幂等更新流水与档案。重复回调或档案已落定均直接返回成功。
func settleRealNameAttempt(ctx context.Context, db *ent.Client, channel, ref string, passed bool, message string, now time.Time) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errInvalidInput
	}
	attempt, err := db.EsportsRealNameAttempt.Query().Where(
		esportsrealnameattempt.ChannelEQ(esportsrealnameattempt.Channel(channel)),
		esportsrealnameattempt.ProviderRefEQ(ref),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return errInvalidInput
	}
	if err != nil {
		return err
	}
	if attempt.Status != esportsrealnameattempt.StatusPending {
		return nil
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	attemptStatus := esportsrealnameattempt.StatusFailed
	status := esportsrealnameverification.StatusRejected
	failReason := message
	if passed {
		attemptStatus = esportsrealnameattempt.StatusPassed
		status = esportsrealnameverification.StatusVerified
		failReason = ""
	}
	changed, err := tx.EsportsRealNameAttempt.Update().
		Where(
			esportsrealnameattempt.IDEQ(attempt.ID),
			esportsrealnameattempt.StatusEQ(esportsrealnameattempt.StatusPending),
		).
		SetStatus(attemptStatus).
		SetFailReason(failReason).
		Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		// 并发回调已被处理：按幂等成功返回。
		return nil
	}
	upd := tx.EsportsRealNameVerification.Update().
		Where(
			esportsrealnameverification.SubjectTypeEQ(esportsrealnameverification.SubjectType(string(attempt.SubjectType))),
			esportsrealnameverification.SubjectIDEQ(attempt.SubjectID),
			esportsrealnameverification.StatusEQ(esportsrealnameverification.StatusPending),
			esportsrealnameverification.ProviderRefEQ(ref),
		).
		SetStatus(status).
		SetFailReason(failReason)
	if passed {
		upd = upd.SetVerifiedAt(now.UTC())
	}
	if _, err := upd.Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// verifyRealNameCallbackSignature 校验渠道回调签名：时间戳偏移 ≤5 分钟，
// X-Signature = hex(HMAC-SHA256(app_secret, app_id\ntimestamp\nnonce\nbody))。
func verifyRealNameCallbackSignature(ctx context.Context, db *ent.Client, channel, appID, timestamp, nonce, signature string, body []byte, now time.Time) error {
	configs := realNameProviderConfigs(ctx, db)
	cfg, ok := configs[strings.TrimSpace(channel)]
	if !ok || !cfg.Enabled || strings.TrimSpace(cfg.AppSecret) == "" {
		return fmt.Errorf("%w：回调渠道未配置", errRealNameProviderUnavailable)
	}
	ts, err := parseRealNameCallbackTimestamp(timestamp)
	if err != nil {
		return errInvalidInput
	}
	if diff := now.Sub(ts); diff > 5*time.Minute || diff < -5*time.Minute {
		return fmt.Errorf("%w：回调时间戳已过期", errInvalidInput)
	}
	expected := realNameHMACHex(cfg.AppSecret, realNameSignatureCanonical(appID, timestamp, nonce, string(body)))
	if !realNameSignatureEqual(expected, signature) {
		return fmt.Errorf("%w：回调签名校验失败", errForbidden)
	}
	return nil
}

// expireRealNamePendingVerifications 把超时的 pending（非 manual）置 expired，
// 并把对应流水置 abandoned；manual 永不超时。返回更新条数。
func expireRealNamePendingVerifications(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = realnameExpireScanBatchSize
	}
	deadline := now.UTC().Add(-time.Duration(realnamePendingTimeoutMinutes(ctx, db)) * time.Minute)
	rows, err := db.EsportsRealNameVerification.Query().Where(
		esportsrealnameverification.StatusEQ(esportsrealnameverification.StatusPending),
		esportsrealnameverification.ChannelNEQ(esportsrealnameverification.ChannelManual),
		esportsrealnameverification.UpdatedAtLT(deadline),
	).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, row := range rows {
		changed, err := db.EsportsRealNameVerification.Update().
			Where(
				esportsrealnameverification.IDEQ(row.ID),
				esportsrealnameverification.StatusEQ(esportsrealnameverification.StatusPending),
			).
			SetStatus(esportsrealnameverification.StatusExpired).
			SetFailReason("认证已超时失效，请重新提交").
			Save(ctx)
		if err != nil {
			return expired, err
		}
		if changed != 1 {
			continue
		}
		expired++
		attempts := db.EsportsRealNameAttempt.Update().
			Where(
				esportsrealnameattempt.SubjectTypeEQ(esportsrealnameattempt.SubjectType(string(row.SubjectType))),
				esportsrealnameattempt.SubjectIDEQ(row.SubjectID),
				esportsrealnameattempt.StatusEQ(esportsrealnameattempt.StatusPending),
			).
			SetStatus(esportsrealnameattempt.StatusAbandoned)
		if row.ProviderRef != nil {
			attempts = attempts.Where(esportsrealnameattempt.ProviderRefEQ(*row.ProviderRef))
		} else {
			attempts = attempts.Where(esportsrealnameattempt.ChannelEQ(esportsrealnameattempt.Channel(string(row.Channel))))
		}
		if _, err := attempts.Save(ctx); err != nil {
			return expired, err
		}
	}
	return expired, nil
}

// expireRealNamePendingTask 定时任务入口：超时实名待审扫描。
func expireRealNamePendingTask(ctx context.Context, logger io.Writer) error {
	count, err := expireRealNamePendingVerifications(ctx, global.ORM, time.Now(), realnameExpireScanBatchSize)
	if err == nil {
		_, _ = fmt.Fprintf(logger, "过期实名待审记录：%d\n", count)
	}
	return err
}
