package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
)

// closeOrderInput 结单表单（备注 + 结单条件校验 + 模板驱动的字段值 + 客服代提交）。
type closeOrderInput struct {
	Note string `json:"note"`
	// FormValues 是结单表单模板驱动的字段值（key = 字段库 key）；订单无适用模板时忽略。
	FormValues map[string]any `json:"form_values"`
	// SubstituteReports 是客服代未上报成员补填的服务段上报；仅生效提交规则允许代提交时接受。
	SubstituteReports []closeSubstituteReport `json:"substitute_reports"`
}

// closeOrder 客服手动结单：事务内校验结单条件，把 pending_close 订单条件更新为 completed 并留痕。
// 结单条件（不满足返回带中文说明的 errInvalidInput，订单保持 pending_close）：
//  1. 至少一条服务段的 evidence_ref 非空（服务凭证不少于 1 张）；
//  2. 所有服务段 system_seconds 之和 > 0 或 reported_units 之和 > 0（服务时长不为零）。
//
// ponytail: 结单条件已可由结单规则（close_rule.go）配置、结单表单可由表单模板（close_form.go）驱动；
// 审批流节点编排（当前仅「待复核 → 管理员通过 / 驳回」两级）与自动结单 / 客户确认结单未实现。
func closeOrder(ctx context.Context, db *ent.Client, orderID, adminID int, in closeOrderInput, now time.Time) (*ent.EsportsOrder, error) {
	in.Note = strings.TrimSpace(in.Note)
	if orderID < 1 || len([]rune(in.Note)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		return nil, errConflict
	}
	segments, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	var evidenceCount, totalSeconds, totalUnits int64
	for _, segment := range segments {
		if strings.TrimSpace(segment.EvidenceRef) != "" {
			evidenceCount++
		}
		totalSeconds += segment.SystemSeconds
		totalUnits += segment.ReportedUnits
	}
	// §3.4：解析生效结单规则（sku > game > global），按条件逐项判定；无规则回落内置默认。
	rule, err := resolveCloseRule(ctx, tx.Client(), order)
	if err != nil {
		return nil, err
	}
	conds := effectiveCloseConditions(rule)
	if int64(conds.MinEvidence) > evidenceCount {
		return nil, fmt.Errorf("%w：服务凭证不足（需至少 %d 张，当前 %d 张），无法结单",
			errInvalidInput, conds.MinEvidence, evidenceCount)
	}
	if conds.RequireDuration && totalSeconds <= 0 && totalUnits <= 0 {
		return nil, fmt.Errorf("%w：该订单服务时长为零，无法结单", errInvalidInput)
	}
	if conds.RequireNoPendingAddon {
		pendingAddon, err := hasPendingAddon(ctx, tx.Client(), orderID)
		if err != nil {
			return nil, err
		}
		if pendingAddon {
			return nil, fmt.Errorf("%w：该订单还有待陪玩确认的加钟，无法结单", errInvalidInput)
		}
	}
	if conds.RequireNoOpenAftersale {
		openAfterSale, err := hasOpenAfterSale(ctx, tx.Client(), orderID)
		if err != nil {
			return nil, err
		}
		if openAfterSale {
			return nil, fmt.Errorf("%w：该订单还有进行中的售后，无法结单", errInvalidInput)
		}
	}
	// §4.6：储值卡标记 / 关联储值卡 / 核销流水不一致时禁止结单（硬性，不随规则变化）。
	if err = storedValueOrderConsistent(ctx, tx.Client(), order); err != nil {
		return nil, err
	}
	// §3.4 结单表单：按订单解析生效模板，校验必填字段并写快照（无适用模板时跳过）。
	if err = applyCloseForm(ctx, tx, order, adminID, in.FormValues, in.Note, now); err != nil {
		return nil, err
	}
	// §3.4 提交规则：校验陪玩个人服务段上报达标情况，允许代提交时可先补填（无适用规则时跳过）。
	if err = applyCloseSubmitRule(ctx, tx, order, adminID, in.SubstituteReports); err != nil {
		return nil, err
	}
	// §3.4 结单复核：金额达复核阈值时订单进入「待复核」，由非负责客服的管理员复核通过 / 驳回。
	if conds.ReviewThresholdCents > 0 && order.PayableCents >= conds.ReviewThresholdCents {
		changed, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingClose),
		).SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingCloseReview).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
			SetOrderVersion(order.Version + 1).SetEventType("order.close_review_requested").
			SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
			SetDetails(map[string]interface{}{
				"threshold_cents": conds.ReviewThresholdCents, "payable_cents": order.PayableCents,
			}).Save(ctx); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return db.EsportsOrder.Get(ctx, orderID)
	}
	if err = finalizeCloseOrderIn(ctx, tx, order, adminID, in.Note, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// finalizeCloseOrderIn 事务内把订单转为 completed：写 closed_at、事件与事务外盒，并同事务计提
// 陪玩分成、客服提成与客户成长值。供「直接结单」与「复核通过」两条路径共用（不负责 commit）。
func finalizeCloseOrderIn(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, adminID int, note string, now time.Time) error {
	segments, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID),
	).All(ctx)
	if err != nil {
		return err
	}
	var evidenceCount, totalSeconds, totalUnits int64
	for _, segment := range segments {
		if strings.TrimSpace(segment.EvidenceRef) != "" {
			evidenceCount++
		}
		totalSeconds += segment.SystemSeconds
		totalUnits += segment.ReportedUnits
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusIn(
			esportsorder.FulfillmentStatusPendingClose, esportsorder.FulfillmentStatusPendingCloseReview),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).
		SetClosedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	newVersion := order.Version + 1
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.closed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"note": note, "evidence_count": evidenceCount,
			"system_seconds": totalSeconds, "reported_units": totalUnits,
		}).Save(ctx); err != nil {
		return err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.closed:" + order.OrderNo).
		SetEventType("order.closed").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo}).Save(ctx); err != nil {
		return err
	}
	updatedOrder, err := tx.EsportsOrder.Get(ctx, order.ID)
	if err != nil {
		return err
	}
	if err = accrueOrderCommission(ctx, tx, updatedOrder, now); err != nil {
		return err
	}
	if err = accrueOrderCsCommission(ctx, tx, updatedOrder, now); err != nil {
		return err
	}
	if err = accrueOrderGrowth(ctx, tx, updatedOrder, now); err != nil {
		return err
	}
	return nil
}

// approveCloseReview 复核通过：pending_close_review → completed，并走完整结单计提。
// 复核人不得为该订单负责客服（不得自审，§3.4 高额单管理员复核）。
func approveCloseReview(ctx context.Context, db *ent.Client, orderID, adminID int, note string, now time.Time) (*ent.EsportsOrder, error) {
	note = strings.TrimSpace(note)
	if orderID < 1 || adminID < 1 || len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		return nil, errConflict
	}
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID == adminID {
		return nil, errForbidden
	}
	if err = finalizeCloseOrderIn(ctx, tx, order, adminID, note, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// rejectCloseReview 复核驳回：pending_close_review → pending_close（回退，不 completed、不计提）。
func rejectCloseReview(ctx context.Context, db *ent.Client, orderID, adminID int, reason string, now time.Time) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if orderID < 1 || adminID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		return nil, errConflict
	}
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID == adminID {
		return nil, errForbidden
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingCloseReview),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.close_review_rejected").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"reason": reason}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}
