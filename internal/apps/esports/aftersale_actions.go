package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsorder"
)

// 多动作售后方案与逐项执行（§13.3 / §13.6）。
//
// 设计要点：
//   - 动作项（EsportsAfterSaleAction）是方案的增量真相，售后单上的 plan_action / refund_cents /
//     makeup_* 退化为「聚合摘要」，保证既有审批预占额度、既有单动作读取方无感兼容；
//   - 逐项执行：每个动作项各自开事务、各自落账与留痕，单项失败不中断后续项（部分成功）；
//   - 按项幂等：动作项 idem_key = aftersale:<id>:action:<seq>:<source> 唯一兜底；下游账本幂等键由
//     按项来源号（<after_sale_no>:a<seq>）派生，与既有 (source_type, source_id, entry_type) 等
//     唯一索引兼容，互不覆盖、不双记。

const (
	// 动作类型：与既有执行器一一对应（refund→退原核销款、compensate→补偿权益、
	// makeup→补钟、replace→换人）。
	afterSaleActionRefund     = "refund"
	afterSaleActionCompensate = "compensate"
	afterSaleActionMakeup     = "makeup"
	afterSaleActionReplace    = "replace"
	// maxAfterSaleActions 单张售后单最多挂的动作项数，避免方案被滥用成批量脚本。
	maxAfterSaleActions = 20
	// 单动作模式下售后单 plan_action 的聚合映射值（沿用既有枚举，仅作摘要）。
	actionPlanSummaryRefund     = "refund_original"
	actionPlanSummaryCompensate = "compensate"
	actionPlanSummaryMakeup     = "makeup_service"
)

// afterSaleActionInput 方案动作项入参（§13.3 / §13.6 多动作方案）。
type afterSaleActionInput struct {
	ActionType string `json:"action_type"`
	// 资金类动作金额（分）；补履约类动作须为 0。
	AmountCents int64 `json:"amount_cents"`
	// 补履约类动作参数：承接陪玩 / 席位 / 补足服务量 / 公司承担报酬。
	PlayerID        int   `json:"player_id"`
	SeatIndex       int   `json:"seat_index"`
	Units           int64 `json:"units"`
	CompanyPayCents int64 `json:"company_pay_cents"`
	// 退款类动作定向冲回的争议陪玩（§13.3 部分退款成员明细冲回）。
	DisputePlayerID int `json:"dispute_player_id"`
	// 原核销明细引用（构成按项幂等键的 <source> 段）；留空按来源单据兜底。
	SourceRef string `json:"source_ref"`
	Note      string `json:"note"`
}

func (in *afterSaleActionInput) normalize() {
	in.ActionType = strings.TrimSpace(in.ActionType)
	in.SourceRef = strings.TrimSpace(in.SourceRef)
	in.Note = strings.TrimSpace(in.Note)
}

func validAfterSaleActionType(actionType string) bool {
	switch actionType {
	case afterSaleActionRefund, afterSaleActionCompensate, afterSaleActionMakeup, afterSaleActionReplace:
		return true
	}
	return false
}

// afterSaleActionIsFunding 判断是否资金类动作项：需占用退款额度、写钱包流水。
func afterSaleActionIsFunding(actionType string) bool {
	return actionType == afterSaleActionRefund || actionType == afterSaleActionCompensate
}

// afterSaleActionMakeupKind 把动作类型映射到既有补履约任务类型。
func afterSaleActionMakeupKind(actionType string) string {
	if actionType == afterSaleActionReplace {
		return string(esportsaftersale.MakeupKindReplacePlayer)
	}
	return string(esportsaftersale.MakeupKindMakeupTime)
}

// validAfterSaleActionStatus 校验动作项状态。
func validAfterSaleActionStatus(status string) bool {
	switch status {
	case "pending", "executing", "succeeded", "failed", "skipped":
		return true
	}
	return false
}

// afterSaleActionIdemKey 按项幂等键：售后单 + 动作项序号 + 原核销明细。
func afterSaleActionIdemKey(afterSaleID, seq int, source string) string {
	return fmt.Sprintf("aftersale:%d:action:%d:%s", afterSaleID, seq, source)
}

// afterSaleActionSourceNo 按项来源号：作为下游账本（钱包 / 分成冲回 / 成长值 / 储值卡批次）
// 的 source_id，使同一售后单的多条动作项在各自唯一索引下互不覆盖。
func afterSaleActionSourceNo(item *ent.EsportsAfterSale, seq int) string {
	return fmt.Sprintf("%s:a%d", item.AfterSaleNo, seq)
}

// afterSaleActionSourceRef 解析动作项的「原核销明细」引用：优先用方案显式给出，
// 否则回落到来源单据（订单用订单号，其余用类型:ID）。
func afterSaleActionSourceRef(item *ent.EsportsAfterSale, order *ent.EsportsOrder, raw string) string {
	if raw != "" {
		return raw
	}
	if order != nil {
		return "order:" + order.OrderNo
	}
	return item.SourceType.String() + ":" + strconv.Itoa(item.SourceID)
}

// listAfterSaleActions 列出某售后单的动作项，按序号升序。
func listAfterSaleActions(ctx context.Context, db *ent.Client, afterSaleID, limit int) ([]*ent.EsportsAfterSaleAction, error) {
	if afterSaleID < 1 {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(afterSaleID),
	).Order(ent.Asc(esportsaftersaleaction.FieldSeq)).Limit(limit).All(ctx)
}

// submitAfterSaleActionsPlan 提交多动作方案（§13.3 / §13.6）：
// 在同一事务内清空上一轮动作项、写入本轮动作项，并把售后单聚合字段更新为摘要后进入「待客户确认」。
// 既有单动作路径（未传 actions）不受影响。
func submitAfterSaleActionsPlan(ctx context.Context, db *ent.Client, id, adminID int,
	in afterSalePlanInput, now time.Time) (*ent.EsportsAfterSale, error) {
	in.Resolution = strings.TrimSpace(in.Resolution)
	if id < 1 || adminID < 1 || !validFaultParty(in.FaultParty) || len([]rune(in.Resolution)) > 255 ||
		len(in.Actions) == 0 || len(in.Actions) > maxAfterSaleActions ||
		in.RefundCents < 0 || in.RecoveryCents < 0 || in.DisputePlayerID < 0 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsaftersale.StatusProcessing {
		return nil, errConflict
	}
	var order *ent.EsportsOrder
	if item.SourceType == esportsaftersale.SourceTypeOrder {
		if order, err = tx.EsportsOrder.Get(ctx, item.SourceID); err != nil {
			return nil, err
		}
	}
	// 先归一并校验全部动作项，任一不合法整单不落。
	type plannedAction struct {
		seq     int
		in      afterSaleActionInput
		kind    esportsaftersale.MakeupKind
		source  string
		summary string
	}
	planned := make([]plannedAction, 0, len(in.Actions))
	var totalRefund int64
	makeupActions := 0
	hasRefund, hasCompensate := false, false
	for i, raw := range in.Actions {
		raw.normalize()
		seq := i + 1
		if !validAfterSaleActionType(raw.ActionType) {
			return nil, errInvalidInput
		}
		if raw.DisputePlayerID < 0 {
			return nil, errInvalidInput
		}
		switch {
		case afterSaleActionIsFunding(raw.ActionType):
			// 资金类动作只对订单来源开放，与既有统一执行器口径一致。
			if item.SourceType != esportsaftersale.SourceTypeOrder {
				return nil, fmt.Errorf("%w：非订单来源暂不支持资金类动作项", errInvalidInput)
			}
			if raw.AmountCents <= 0 {
				return nil, fmt.Errorf("%w：资金类动作项金额须大于零", errInvalidInput)
			}
			totalRefund += raw.AmountCents
			if raw.ActionType == afterSaleActionRefund {
				hasRefund = true
			} else {
				hasCompensate = true
			}
		default:
			// 补履约类动作项不产生退款金额。
			if raw.AmountCents != 0 {
				return nil, fmt.Errorf("%w：补履约类动作项不产生退款金额", errInvalidInput)
			}
			if order == nil {
				return nil, fmt.Errorf("%w：补履约类动作项仅支持订单来源", errInvalidInput)
			}
			makeupActions++
			// 补履约任务对 after_sale_id 唯一，一张售后单最多一条补履约动作项。
			if makeupActions > 1 {
				return nil, fmt.Errorf("%w：一张售后单最多一条补履约动作项", errInvalidInput)
			}
			if err = validateMakeupPlan(ctx, tx.Client(), order, afterSalePlanInput{
				MakeupKind:            afterSaleActionMakeupKind(raw.ActionType),
				MakeupPlayerID:        raw.PlayerID,
				MakeupSeatIndex:       raw.SeatIndex,
				MakeupUnits:           raw.Units,
				MakeupCompanyPayCents: raw.CompanyPayCents,
			}); err != nil {
				return nil, err
			}
		}
		if raw.DisputePlayerID > 0 {
			served, err := playerServedOrder(ctx, tx.Client(), item.SourceID, raw.DisputePlayerID)
			if err != nil {
				return nil, err
			}
			if !served {
				return nil, fmt.Errorf("%w：争议陪玩须为该订单实际服务成员", errInvalidInput)
			}
		}
		planned = append(planned, plannedAction{
			seq: seq, in: raw, kind: esportsaftersale.MakeupKind(afterSaleActionMakeupKind(raw.ActionType)),
			source: afterSaleActionSourceRef(item, order, raw.SourceRef),
		})
	}
	// §13.4：追偿须判责陪玩并指定承接陪玩，且不得超过本次赔付总额。
	if in.RecoveryCents > 0 && (in.FaultParty != "player" || in.RecoveryPlayerID < 1) {
		return nil, fmt.Errorf("%w：向陪玩追偿时须把责任方判为陪玩并指定承接陪玩", errInvalidInput)
	}
	if in.RecoveryCents > totalRefund {
		return nil, fmt.Errorf("%w：追偿金额不得超过本次赔付金额", errInvalidInput)
	}
	// 重建动作项：清空上一轮，再按序号写入本轮。
	if _, err = tx.EsportsAfterSaleAction.Delete().Where(
		esportsaftersaleaction.AfterSaleIDEQ(id),
	).Exec(ctx); err != nil {
		return nil, err
	}
	for _, pa := range planned {
		create := tx.EsportsAfterSaleAction.Create().
			SetAfterSaleID(id).SetOrderID(item.SourceID).SetSeq(pa.seq).
			SetActionType(esportsaftersaleaction.ActionType(pa.in.ActionType)).
			SetAmountCents(pa.in.AmountCents).SetUnits(pa.in.Units).
			SetCompanyPayCents(pa.in.CompanyPayCents).
			SetSourceRef(pa.source).
			SetIdemKey(afterSaleActionIdemKey(id, pa.seq, pa.source)).
			SetStatus(esportsaftersaleaction.StatusPending).SetNote(pa.in.Note)
		if pa.in.PlayerID > 0 {
			create.SetPlayerID(pa.in.PlayerID)
		}
		if pa.in.SeatIndex > 0 {
			create.SetSeatIndex(pa.in.SeatIndex)
		}
		if pa.in.DisputePlayerID > 0 {
			create.SetDisputePlayerID(pa.in.DisputePlayerID)
		}
		if _, err = create.Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return nil, errConflict
			}
			return nil, err
		}
	}
	// 聚合摘要：refund_cents = 资金类动作金额之和（沿用既有审批预占口径）；
	// plan_action 取主导类型；单条补履约动作参数同时镜像到售后单 makeup_* 字段。
	planAction := actionPlanSummaryMakeup
	switch {
	case hasRefund:
		planAction = actionPlanSummaryRefund
	case hasCompensate:
		planAction = actionPlanSummaryCompensate
	}
	makeupKind := esportsaftersale.MakeupKindNone
	var makeupPlayerID, makeupSeatIndex *int
	var makeupUnits, makeupCompanyPay int64
	if len(planned) == 1 && !afterSaleActionIsFunding(planned[0].in.ActionType) {
		makeupKind = planned[0].kind
		playerID, seatIndex := planned[0].in.PlayerID, planned[0].in.SeatIndex
		makeupPlayerID, makeupSeatIndex = &playerID, &seatIndex
		makeupUnits, makeupCompanyPay = planned[0].in.Units, planned[0].in.CompanyPayCents
	}
	update := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusEQ(esportsaftersale.StatusProcessing),
	).SetFaultParty(esportsaftersale.FaultParty(in.FaultParty)).
		SetPlanAction(esportsaftersale.PlanAction(planAction)).
		SetRefundCents(totalRefund).
		SetRecoveryCents(in.RecoveryCents).
		SetMakeupKind(makeupKind).
		SetMakeupUnits(makeupUnits).
		SetMakeupCompanyPayCents(makeupCompanyPay).
		SetResolution(in.Resolution).
		SetPlannedAt(now.UTC()).
		SetStatus(esportsaftersale.StatusPendingCustomerConfirm).AddVersion(1).
		ClearCustomerConfirmedAt().ClearCustomerConfirmedByAdminID().ClearCustomerDeclinedAt().
		ClearApprovedAt().
		SetCustomerConfirmChannel(esportsaftersale.CustomerConfirmChannelNone).
		SetCustomerDeclineReason("")
	if makeupPlayerID != nil && makeupSeatIndex != nil {
		update = update.SetMakeupPlayerID(*makeupPlayerID).SetMakeupSeatIndex(*makeupSeatIndex)
	} else {
		update = update.ClearMakeupPlayerID().ClearMakeupSeatIndex()
	}
	if in.RecoveryCents > 0 {
		update = update.SetRecoveryPlayerID(in.RecoveryPlayerID)
	} else {
		update = update.ClearRecoveryPlayerID()
	}
	if in.DisputePlayerID > 0 {
		update = update.SetPlayerID(in.DisputePlayerID)
	} else {
		update = update.ClearPlayerID()
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// executeAfterSaleActions 逐项执行动作项（§13.6）：逐项开事务、逐项更新进度，允许部分成功。
// 允许从 pending_execution 首发，或从 execution_failed 续跑尚未完成的动作项（人工处理后重试）。
func executeAfterSaleActions(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsAfterSale, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsaftersale.StatusPendingExecution && item.Status != esportsaftersale.StatusExecutionFailed {
		return nil, errConflict
	}
	if err := ensureAfterSaleSourceOrderNotFrozen(ctx, db, item); err != nil {
		return nil, err
	}
	actions, err := listAfterSaleActions(ctx, db, id, 200)
	if err != nil {
		return nil, err
	}
	if len(actions) == 0 {
		return nil, errInvalidInput
	}
	for _, action := range actions {
		if action.Status == esportsaftersaleaction.StatusSucceeded ||
			action.Status == esportsaftersaleaction.StatusSkipped {
			continue
		}
		// 单项失败不中断：现场已落库（动作项 failed + 订单事件），继续执行后续动作项。
		runAfterSaleAction(ctx, db, item, action, adminID, now)
	}
	return finalizeAfterSaleActions(ctx, db, id, adminID, now)
}

// executeAfterSaleActionSeq 执行指定序号的单个动作项并刷新总状态，供人工逐项重试（§13.6）。
func executeAfterSaleActionSeq(ctx context.Context, db *ent.Client, afterSaleID, seq, adminID int,
	now time.Time) (*ent.EsportsAfterSaleAction, error) {
	if afterSaleID < 1 || seq < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return nil, err
	}
	if err := ensureAfterSaleSourceOrderNotFrozen(ctx, db, item); err != nil {
		return nil, err
	}
	if item.Status != esportsaftersale.StatusPendingExecution && item.Status != esportsaftersale.StatusExecutionFailed {
		return nil, errConflict
	}
	action, err := db.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(afterSaleID),
		esportsaftersaleaction.SeqEQ(seq),
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	if action.Status == esportsaftersaleaction.StatusSucceeded || action.Status == esportsaftersaleaction.StatusSkipped {
		return nil, errConflict
	}
	runAfterSaleAction(ctx, db, item, action, adminID, now)
	if _, err = finalizeAfterSaleActions(ctx, db, afterSaleID, adminID, now); err != nil {
		return nil, err
	}
	return db.EsportsAfterSaleAction.Get(ctx, action.ID)
}

// runAfterSaleAction 执行单个动作项；失败时在独立事务内把动作项落为 failed 并写订单事件留痕，
// 退款类动作项失败时另把订单退款状态落 failed 并留原因（§3.2-03）。
func runAfterSaleAction(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale,
	action *ent.EsportsAfterSaleAction, adminID int, now time.Time) {
	if err := executeAfterSaleActionOnce(ctx, db, item, action, adminID, now); err != nil {
		markAfterSaleActionFailed(ctx, db, item, action, adminID, err, now)
		if item.SourceType == esportsaftersale.SourceTypeOrder &&
			action.ActionType == esportsaftersaleaction.ActionTypeRefund &&
			shouldMarkOrderRefundFailed(err) {
			// 失败留痕尽力而为：动作项事件已记录主因，这里只补订单级退款失败态，不覆盖主错。
			_ = markOrderRefundFailed(ctx, db, item.SourceID, adminID, err.Error(), now)
		}
	}
}

// executeAfterSaleActionOnce 在独立事务内执行一个动作项：标记 executing → 复用既有执行逻辑 → 标记 succeeded。
// 任一环节失败整体回滚（动作项状态不被改为 executing），由调用方另开事务落 failed。
func executeAfterSaleActionOnce(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale,
	action *ent.EsportsAfterSaleAction, adminID int, now time.Time) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureAfterSaleSourceOrderNotFrozen(ctx, tx.Client(), item); err != nil {
		return err
	}
	fresh, err := tx.EsportsAfterSaleAction.Get(ctx, action.ID)
	if err != nil {
		return err
	}
	if fresh.Status == esportsaftersaleaction.StatusSucceeded ||
		fresh.Status == esportsaftersaleaction.StatusSkipped {
		return errConflict
	}
	changed, err := tx.EsportsAfterSaleAction.Update().Where(
		esportsaftersaleaction.IDEQ(fresh.ID),
		esportsaftersaleaction.VersionEQ(fresh.Version),
		esportsaftersaleaction.StatusIn(esportsaftersaleaction.StatusPending, esportsaftersaleaction.StatusFailed),
	).SetStatus(esportsaftersaleaction.StatusExecuting).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return err
	}
	var summary string
	var newVersion int
	switch fresh.ActionType {
	case esportsaftersaleaction.ActionTypeRefund, esportsaftersaleaction.ActionTypeCompensate:
		if item.SourceType != esportsaftersale.SourceTypeOrder {
			return fmt.Errorf("%w：非订单来源暂不支持资金类动作项", errInvalidInput)
		}
		summary, newVersion, err = executeFundingAction(ctx, tx, item, order, fresh, adminID, now)
	case esportsaftersaleaction.ActionTypeMakeup, esportsaftersaleaction.ActionTypeReplace:
		summary, newVersion, err = executeMakeupAction(ctx, tx, item, order, fresh, adminID, now)
	default:
		return errInvalidInput
	}
	if err != nil {
		return err
	}
	changed, err = tx.EsportsAfterSaleAction.Update().Where(
		esportsaftersaleaction.IDEQ(fresh.ID),
		esportsaftersaleaction.VersionEQ(fresh.Version+1),
		esportsaftersaleaction.StatusEQ(esportsaftersaleaction.StatusExecuting),
	).SetStatus(esportsaftersaleaction.StatusSucceeded).
		SetResultSummary(summary).SetExecutorAdminID(adminID).SetExecutedAt(now.UTC()).
		AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.after_sale_action_executed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "seq": fresh.Seq,
			"action_type": fresh.ActionType.String(), "amount_cents": fresh.AmountCents,
			"result": summary, "idem_key": fresh.IdemKey,
		}).Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

// executeFundingAction 复用既有资金动作执行：退原核销款 / 补偿权益 + 订单退款额度落账 + 分成 / 成长值冲回。
// 通过「按项来源号」的临时视图（shim）复用既有函数，使各下游账本幂等键按动作项拆分、互不覆盖。
// 返回执行摘要与订单事件应使用的新版本号。
func executeFundingAction(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale,
	order *ent.EsportsOrder, action *ent.EsportsAfterSaleAction, adminID int,
	now time.Time) (string, int, error) {
	shim := *item
	// 按项来源号：钱包流水 (after_sale, source_id, refund_return)、分成冲回、成长值等
	// 幂等键据此按项拆分，与既有单动作（用原售后单号）互不冲突。
	shim.AfterSaleNo = afterSaleActionSourceNo(item, action.Seq)
	shim.RefundCents = action.AmountCents
	// 追偿在整体成功时统一建单，逐项执行时禁用，避免多次资金动作重复建追偿。
	shim.RecoveryCents = 0
	shim.RecoveryPlayerID = nil
	if action.DisputePlayerID != nil {
		dispute := *action.DisputePlayerID
		shim.PlayerID = &dispute
	} else {
		shim.PlayerID = nil
	}
	switch action.ActionType {
	case esportsaftersaleaction.ActionTypeRefund:
		if err := executeRefundOriginal(ctx, tx, &shim, order, adminID); err != nil {
			return "", 0, err
		}
	case esportsaftersaleaction.ActionTypeCompensate:
		if err := executeCompensate(ctx, tx, &shim, order, adminID); err != nil {
			return "", 0, err
		}
		// 补偿只发权益：不占退款额度、不累加 refunded_cents、不冲回订单分成/提成/成长值。
		return fmt.Sprintf("资金动作执行成功：%s %d 分", action.ActionType.String(), action.AmountCents),
			order.Version, nil
	}
	// 统一额度落账：从预占额度扣减并累加 refunded_cents（与既有单动作同一条件表达式口径）。
	// §12.5-02：可退上界 = 服务应付 + 加钟已收款，并发谓词必须带上同一上界。
	addonReceived, err := addonReceivedCents(ctx, tx.Client(), order.ID)
	if err != nil {
		return "", 0, err
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		func(s *sql.Selector) {
			s.Where(sql.ExprP("refund_reserved_cents >= ?", action.AmountCents))
			s.Where(sql.ExprP("refunded_cents + refund_reserved_cents <= payable_cents + ?", addonReceived))
		},
	).AddRefundReservedCents(-action.AmountCents).AddRefundedCents(action.AmountCents).
		SetRefundStatus(refundStatusFor(order.PayableCents+addonReceived, order.RefundedCents+action.AmountCents)).
		AddVersion(1).Save(ctx)
	if err != nil {
		return "", 0, err
	}
	if changed != 1 {
		return "", 0, errConflict
	}
	refundedOrder, err := tx.EsportsOrder.Get(ctx, order.ID)
	if err != nil {
		return "", 0, err
	}
	if err = reverseOrderCommission(ctx, tx, refundedOrder, &shim, now); err != nil {
		return "", 0, err
	}
	if err = reverseOrderCsCommission(ctx, tx, refundedOrder, &shim, now); err != nil {
		return "", 0, err
	}
	if err = reverseOrderGrowth(ctx, tx, order, &shim, adminID, now); err != nil {
		return "", 0, err
	}
	summary := fmt.Sprintf("资金动作执行成功：%s %d 分", action.ActionType.String(), action.AmountCents)
	return summary, order.Version + 1, nil
}

// executeMakeupAction 复用既有补履约任务创建：makeup → 补钟、replace → 换人。
// 补履约不退款、不改原订单实收，任务本身自带服务段与客服验收；返回执行摘要与订单事件新版本号。
func executeMakeupAction(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale,
	order *ent.EsportsOrder, action *ent.EsportsAfterSaleAction, adminID int,
	now time.Time) (string, int, error) {
	shim := *item
	shim.MakeupKind = esportsaftersale.MakeupKind(afterSaleActionMakeupKind(action.ActionType.String()))
	if action.PlayerID != nil {
		player := *action.PlayerID
		shim.MakeupPlayerID = &player
	} else {
		shim.MakeupPlayerID = nil
	}
	if action.SeatIndex != nil {
		seat := *action.SeatIndex
		shim.MakeupSeatIndex = &seat
	} else {
		shim.MakeupSeatIndex = nil
	}
	shim.MakeupUnits = action.Units
	shim.MakeupCompanyPayCents = action.CompanyPayCents
	task, err := createFulfillmentMakeupIn(ctx, tx, &shim, order, adminID, now)
	if err != nil {
		return "", 0, err
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return "", 0, err
	}
	summary := fmt.Sprintf("补履约任务已创建：%s（%s）", task.MakeupNo, task.Kind.String())
	return summary, newVersion, nil
}

// markAfterSaleActionFailed 独立事务内把动作项落为 failed 并写订单事件，供部分成功后的进度展示与人工处理。
func markAfterSaleActionFailed(ctx context.Context, db *ent.Client, item *ent.EsportsAfterSale,
	action *ent.EsportsAfterSaleAction, adminID int, cause error, now time.Time) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	fresh, err := tx.EsportsAfterSaleAction.Get(ctx, action.ID)
	if err != nil {
		return
	}
	if fresh.Status == esportsaftersaleaction.StatusSucceeded ||
		fresh.Status == esportsaftersaleaction.StatusSkipped {
		return
	}
	changed, err := tx.EsportsAfterSaleAction.Update().Where(
		esportsaftersaleaction.IDEQ(fresh.ID),
		esportsaftersaleaction.VersionEQ(fresh.Version),
		esportsaftersaleaction.StatusIn(esportsaftersaleaction.StatusPending, esportsaftersaleaction.StatusFailed),
	).SetStatus(esportsaftersaleaction.StatusFailed).
		SetResultSummary(truncateRunes(cause.Error(), 500)).
		SetExecutorAdminID(adminID).SetExecutedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil || changed != 1 {
		return
	}
	order, err := tx.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.after_sale_action_failed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "seq": fresh.Seq,
			"action_type": fresh.ActionType.String(), "reason": cause.Error(),
		}).Save(ctx); err != nil {
		return
	}
	_ = tx.Commit()
}

// finalizeAfterSaleActions 汇总逐项执行结果推导售后单总状态（§13.6）：
//   - 全部动作项成功 → completed（既有终态），解冻分成、建追偿、记陪玩信用并写完成事件；
//   - 存在失败 / 未完成项 → execution_failed（既有异常态：部分执行完成，需人工处理），
//     写 order.after_sale_actions_partially_executed 事件与 aftersale.actions_partially_executed 外盒留痕。
//
// 说明：售后单状态枚举不含独立的 partially_completed 值，故「部分成功」沿用既有异常态 execution_failed
// 承载，靠动作项逐项状态 + resolution + 事件 / 外盒明细区分于「全失败」；新增专用枚举值需改动
// schema/esports_aftersale.go，超出本次改动范围。
func finalizeAfterSaleActions(ctx context.Context, db *ent.Client, id, adminID int,
	now time.Time) (*ent.EsportsAfterSale, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsAfterSale.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	actions, err := tx.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(id),
	).Order(ent.Asc(esportsaftersaleaction.FieldSeq)).All(ctx)
	if err != nil {
		return nil, err
	}
	succeeded, unresolved := 0, 0
	var failures []string
	for _, action := range actions {
		switch action.Status {
		case esportsaftersaleaction.StatusSucceeded:
			succeeded++
		case esportsaftersaleaction.StatusSkipped:
			// 跳过项不计入成功，也不阻断终态。
		default:
			unresolved++
			failures = append(failures, fmt.Sprintf("#%d %s：%s",
				action.Seq, action.ActionType.String(), truncateRunes(action.ResultSummary, 80)))
		}
	}
	order, err := tx.EsportsOrder.Get(ctx, item.SourceID)
	if err != nil {
		return nil, err
	}
	if unresolved == 0 {
		changed, err := tx.EsportsAfterSale.Update().Where(
			esportsaftersale.IDEQ(id),
			esportsaftersale.StatusIn(esportsaftersale.StatusPendingExecution, esportsaftersale.StatusExecutionFailed),
		).SetStatus(esportsaftersale.StatusCompleted).SetExecutorAdminID(adminID).
			SetResolvedAt(now.UTC()).
			SetResolution(fmt.Sprintf("多动作方案全部执行完成（共 %d 项）", succeeded)).
			// §13.2-09：落终态释放进行中唯一键。
			ClearActiveDedupKey().AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		// §12.6：执行完成即争议终结，解冻该订单待结算分成明细。
		if item.SourceType == esportsaftersale.SourceTypeOrder {
			if _, err = unfreezeCommissionForOrder(ctx, tx.EsportsCommissionEntry, item.SourceID); err != nil {
				return nil, err
			}
		}
		// §13.4：追偿按售后单统一建单（逐项执行时已禁用，避免重复）。
		if err = createRecoveryFromAfterSale(ctx, tx, item, adminID); err != nil {
			return nil, err
		}
		// §13.5：责任方为陪玩时自动记载信用（同售后单幂等）。
		if err = recordPlayerFaultCredit(ctx, tx, id); err != nil {
			return nil, err
		}
		newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
		if err != nil {
			return nil, err
		}
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
			SetOrderVersion(newVersion).SetEventType("order.after_sale_actions_completed").
			SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
			SetDetails(map[string]interface{}{
				"after_sale_no": item.AfterSaleNo, "total": len(actions), "succeeded": succeeded,
			}).Save(ctx); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return db.EsportsAfterSale.Get(ctx, id)
	}
	resolution := truncateRunes(fmt.Sprintf("部分动作项执行失败，需人工处理（成功 %d / 共 %d）：%s",
		succeeded, len(actions), strings.Join(failures, "；")), 255)
	changed, err := tx.EsportsAfterSale.Update().Where(
		esportsaftersale.IDEQ(id),
		esportsaftersale.StatusIn(esportsaftersale.StatusPendingExecution, esportsaftersale.StatusExecutionFailed),
	).SetStatus(esportsaftersale.StatusExecutionFailed).SetExecutorAdminID(adminID).
		SetResolution(resolution).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.after_sale_actions_partially_executed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "total": len(actions),
			"succeeded": succeeded, "unresolved": unresolved, "failures": failures,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("aftersale.actions_partial:" + item.AfterSaleNo).
		SetEventType("aftersale.actions_partially_executed").SetAggregateRef(item.AfterSaleNo).
		SetPayload(map[string]interface{}{
			"after_sale_no": item.AfterSaleNo, "total": len(actions),
			"succeeded": succeeded, "unresolved": unresolved,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsAfterSale.Get(ctx, id)
}

// afterSaleActionProgressOf 汇总动作项进度（总数 / 成功 / 失败 / 待执行），供视图与事件复用。
func afterSaleActionProgressOf(actions []*ent.EsportsAfterSaleAction) map[string]int {
	progress := map[string]int{"total": len(actions), "succeeded": 0, "failed": 0, "pending": 0}
	for _, action := range actions {
		switch action.Status {
		case esportsaftersaleaction.StatusSucceeded:
			progress["succeeded"]++
		case esportsaftersaleaction.StatusFailed:
			progress["failed"]++
		case esportsaftersaleaction.StatusSkipped:
		default:
			progress["pending"]++
		}
	}
	return progress
}

// truncateRunes 按字符数截断，避免多字节字符被切断。
func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
