package esports

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscloseapproval"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// closeOrderInput 结单表单（备注 + 结单条件校验 + 模板驱动的字段值 + 客服代提交）。
type closeOrderInput struct {
	Note string `json:"note"`
	// FormValues 是结单表单模板驱动的字段值（key = 字段库 key）；订单无适用模板时忽略。
	FormValues map[string]any `json:"form_values"`
	// SubstituteReports 是客服代未上报成员补填的服务段上报；仅生效提交规则允许代提交时接受。
	SubstituteReports []closeSubstituteReport `json:"substitute_reports"`
	// ViewedEvidenceAttachmentIDs 是客服声明「已查看」的服务段凭证附件 ID（§3.4-03）：
	// 必须覆盖该订单全部服务段 evidence_ref 里的图片附件 ID，否则结单返回参数错误。
	ViewedEvidenceAttachmentIDs []int `json:"viewed_evidence_attachment_ids"`
}

// ensureCloseEvidenceViewed 校验「结单前已查看陪玩上传的凭证图片」（§3.4-03）：
// 返回本次确认覆盖的附件 ID（按需查看集合裁剪，避免把无关或伪造 ID 写进留痕）；
// 存在必须查看的凭证而请求未完整覆盖时返回参数错误。
func ensureCloseEvidenceViewed(ctx context.Context, db *ent.Client, orderID int, viewed []int) ([]int, error) {
	required, err := serviceEvidenceAttachmentIDs(ctx, db, orderID)
	if err != nil {
		return nil, err
	}
	if len(required) == 0 {
		return nil, nil
	}
	seen := map[int]bool{}
	for _, id := range viewed {
		if id > 0 {
			seen[id] = true
		}
	}
	missing := make([]int, 0, len(required))
	for _, id := range required {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w：结单前须逐张查看陪玩上传的服务凭证图片，尚未确认查看的附件 ID：%v",
			errInvalidInput, missing)
	}
	return required, nil
}

// closeOrder 客服手动结单：事务内校验结单条件，把 pending_close 订单条件更新为 completed 并留痕。
// 结单条件（不满足返回带中文说明的 errInvalidInput，订单保持 pending_close）：
//  1. 至少一条服务段的 evidence_ref 非空（服务凭证不少于 1 张）；
//  2. 所有服务段 system_seconds 之和 > 0 或 reported_units 之和 > 0（服务时长不为零）。
//
// 结单条件由结单规则（close_rule.go）配置，结单表单按下单时模板版本校验，
// 审批流可配置串行节点、角色与超时策略。自动结单 / 客户确认结单属于后续版本。
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		return nil, errConflict
	}
	// §3.4：解析生效结单规则（SKU 结单要求 > sku 规则 > game > global），
	// 通用 AND/OR 条件树优先，未配置时折叠结构化默认项（凭证 ≥N、时长不为零、无未处理加钟 / 售后）。
	rule, err := resolveCloseRule(ctx, tx.Client(), order)
	if err != nil {
		return nil, err
	}
	conds := effectiveCloseConditions(rule)
	status, err := checkCloseConditions(ctx, tx.Client(), order, rule)
	if err != nil {
		return nil, err
	}
	if !status.Satisfied {
		return nil, fmt.Errorf("%w：结单条件未满足：%s", errInvalidInput,
			strings.Join(status.Reasons, "；"))
	}
	// §4.6：储值卡标记 / 关联储值卡 / 核销流水不一致时禁止结单（硬性，不随规则变化）。
	if err = storedValueOrderConsistent(ctx, tx.Client(), order); err != nil {
		// §4.6-06：拦截同时把不一致落进异常队列（可查询 / 指派 / 处置）。
		// 必须先回滚本事务——队列写入要独立提交，否则会被 defer tx.Rollback() 一起丢掉，
		// 那正是「只拦不落队列」的老坑。写队列失败时不假装拦截已记档，直接报错。
		_ = tx.Rollback()
		if _, recordErr := enqueueStoredValueOrderException(ctx, db, order, adminID, now); recordErr != nil {
			return nil, fmt.Errorf("储值卡订单被拦下但异常队列写入失败: %w", recordErr)
		}
		return nil, err
	}
	// §3.4-03：结单前必须逐张查看陪玩上传的凭证图片，并落「已查看附件 + 操作人」留痕。
	viewedEvidence, err := ensureCloseEvidenceViewed(ctx, tx.Client(), orderID, in.ViewedEvidenceAttachmentIDs)
	if err != nil {
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
	// §3.4-03：把「已查看凭证」写进结单留痕（复核路径也要带，故随 extra 传入 finalize）。
	evidenceDetails := map[string]interface{}{
		"viewed_evidence_attachment_ids": viewedEvidence,
		"viewed_by_admin_id":             adminID,
	}
	// §3.4 结单审批：命中生效审批流（按金额阈值）或结单规则的复核阈值时，订单进入「待复核」
	// 并建立审批进度（逐节点串行审批，末节点通过才真正结单）。
	approval, err := startCloseApprovalIfNeeded(ctx, tx, order, conds.ReviewThresholdCents)
	if err != nil {
		return nil, err
	}
	if approval != nil {
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
				"flow_id": approval.FlowID, "flow_code": approval.FlowCode,
				"flow_version": approval.FlowVersion, "node_count": len(approval.Steps),
				"current_seq":                    approval.CurrentSeq,
				"viewed_evidence_attachment_ids": viewedEvidence, "viewed_by_admin_id": adminID,
			}).Save(ctx); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return db.EsportsOrder.Get(ctx, orderID)
	}
	if err = finalizeCloseOrderIn(ctx, tx, order, adminID, "admin", in.Note, evidenceDetails, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// finalizeCloseOrderIn 事务内把订单转为 completed：写 closed_at、事件与事务外盒，并同事务计提
// 陪玩分成、客服提成与客户成长值。供「直接结单」与「复核通过」两条路径共用（不负责 commit）。
// extra 为并入 order.closed 事件 details 的补充留痕（如 §3.4-03 的已查看凭证 ID 与操作人）。
func finalizeCloseOrderIn(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, adminID int, actorType, note string, extra map[string]interface{}, now time.Time) error {
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
		SetCloseNetServiceCents(max(0, order.PayableCents-order.RefundedCents)).
		SetClosedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	newVersion := order.Version + 1
	actorID := strconv.Itoa(adminID)
	if actorType == "system" {
		actorID = "system"
	}
	closeDetails := map[string]interface{}{
		"note": note, "evidence_count": evidenceCount,
		"system_seconds": totalSeconds, "reported_units": totalUnits,
	}
	for key, value := range extra {
		closeDetails[key] = value
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.closed").
		SetActorType(actorType).SetActorID(actorID).
		SetDetails(closeDetails).Save(ctx); err != nil {
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
	// 陪玩分成与成长值始终正常计提（含补录单，§12.5 / §10.2）。
	if err = accrueOrderCommission(ctx, tx, updatedOrder, now); err != nil {
		return err
	}
	// §3.3 / §12.5 补录隔离：历史补录单默认不计提客服提成，
	// 写一条客服提成排除外盒留痕，避免后台把补录单推进到结单流程后误把历史流水算进当期业绩。
	if !orderCountsCsCommission(updatedOrder) {
		_, err = tx.EsportsOutbox.Create().SetEventKey("order.cs_commission_excluded:" + updatedOrder.OrderNo).
			SetEventType("order.cs_commission_excluded").SetAggregateRef(updatedOrder.OrderNo).
			SetPayload(map[string]interface{}{
				"order_no": updatedOrder.OrderNo, "reason": "admin_backfill",
			}).Save(ctx)
		return err
	}
	if err = accrueOrderCsCommission(ctx, tx, updatedOrder, now); err != nil {
		return err
	}
	if err = accrueOrderGrowth(ctx, tx, updatedOrder, now); err != nil {
		return err
	}
	// 结单后复购跟进（§14-01）：在同一事务内生成「客户维度复购待办」，不必等定时任务窗口。
	// 该函数在「未配置(repurchase_follow_up_days=0) / 未结单 / 无归属客服」时是明确的 no-op（nil, nil），
	// 因此不会阻断结单主链路；定时任务 esports_schedule_repurchase_follow_ups 仍保留作兜底（幂等）。
	if _, err = createRepurchaseFollowUpForOrder(ctx, tx.Client(), order.ID, now); err != nil {
		return err
	}
	return nil
}

// closeReviewApproval 取出订单的审批进度；无进度（历史数据 / 未建）返回 nil，调用方按内置单节点处理。
func closeReviewApproval(ctx context.Context, tx *ent.Tx, orderID int) (*ent.EsportsCloseApproval, error) {
	approval, err := tx.EsportsCloseApproval.Query().Where(
		esportscloseapproval.OrderIDEQ(orderID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return approval, nil
}

// approveCloseReview 复核通过：推进当前审批节点；末节点通过后订单转 completed 并走完整结单计提。
// 审批人不得为该订单负责客服（不得自审，§3.4）；节点角色为 super_admin 时须超级管理员。
func approveCloseReview(ctx context.Context, db *ent.Client, orderID, adminID int, isSuper bool,
	note string, now time.Time) (*ent.EsportsOrder, error) {
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		return nil, errConflict
	}
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID == adminID {
		return nil, errForbidden
	}
	approval, err := closeReviewApproval(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	if approval == nil {
		// 无审批进度：等价于内置单节点复核，直接完成。
		extra := map[string]interface{}{"viewed_by_admin_id": adminID}
		if reviewed, viewedErr := serviceEvidenceAttachmentIDs(ctx, tx.Client(), orderID); viewedErr != nil {
			return nil, viewedErr
		} else if len(reviewed) > 0 {
			extra["viewed_evidence_attachment_ids"] = reviewed
		}
		if err = finalizeCloseOrderIn(ctx, tx, order, adminID, "admin", note, extra, now); err != nil {
			return nil, err
		}
	} else if err = approveCloseApprovalIn(ctx, tx, order, approval, adminID, isSuper, false, note, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// rejectCloseReview 复核驳回：把当前节点标记驳回并回退到待结单（不 completed、不计提）。
func rejectCloseReview(ctx context.Context, db *ent.Client, orderID, adminID int, isSuper bool,
	reason string, now time.Time) (*ent.EsportsOrder, error) {
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		return nil, errConflict
	}
	if order.ResponsibleAdminID != nil && *order.ResponsibleAdminID == adminID {
		return nil, errForbidden
	}
	approval, err := closeReviewApproval(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	if approval != nil {
		if err = rejectCloseApprovalIn(ctx, tx, order, approval, adminID, isSuper, reason, now); err != nil {
			return nil, err
		}
	} else {
		// 无审批进度：等价于内置单节点驳回。
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
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// ── §2.1-09 结单时限配置化与超时待办 ──

// closeDeadlineHoursKey 结单时限（小时）：订单进入待结单后超过该时长仍未结单即进客服 / 管理员待办。
// 0 或配置非法 = 关闭该待办（不设时限），与「未配置的能力必须显式降级」的口径一致。
const closeDeadlineHoursKey = "app.esports.close_deadline_hours"

// closeDeadlineHours 返回结单时限小时数；配置缺失 / 非法 / 非正数一律回落 0（关闭）。
func closeDeadlineHours(ctx context.Context, db *ent.Client) int {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(closeDeadlineHoursKey)).Only(ctx)
	if err != nil {
		return 0
	}
	hours, convErr := strconv.Atoi(strings.TrimSpace(item.Value))
	if convErr != nil || hours < 1 || hours > 24*60 {
		return 0
	}
	return hours
}

// closeDeadlineOverdueItem 是「超结单时限未结单」待办项（§2.1-09）。
type closeDeadlineOverdueItem struct {
	OrderID            int       `json:"order_id"`
	OrderNo            string    `json:"order_no"`
	ResponsibleAdminID *int      `json:"responsible_admin_id"`
	EnteredPendingAt   time.Time `json:"entered_pending_at"`
	HoursOverdue       int       `json:"hours_overdue"`
	FulfillmentStatus  string    `json:"fulfillment_status"`
}

// listCloseDeadlineOverdue 列出超过结单时限仍未结单的订单（§2.1-09）：
// 口径 = 履约状态为待结单 / 待结单复核，且最近一次状态变更（updated_at）已超过 close_deadline_hours。
// 时限未配置（0）时返回空列表——待办整体关闭。
func listCloseDeadlineOverdue(ctx context.Context, db *ent.Client, now time.Time, limit int) ([]closeDeadlineOverdueItem, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	hours := closeDeadlineHours(ctx, db)
	if hours <= 0 {
		return []closeDeadlineOverdueItem{}, nil
	}
	cutoff := now.UTC().Add(-time.Duration(hours) * time.Hour)
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusIn(
			esportsorder.FulfillmentStatusPendingClose,
			esportsorder.FulfillmentStatusPendingCloseReview),
		esportsorder.UpdatedAtLTE(cutoff),
	).Order(ent.Asc(esportsorder.FieldUpdatedAt)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]closeDeadlineOverdueItem, 0, len(orders))
	for _, order := range orders {
		overdue := int(now.UTC().Sub(order.UpdatedAt.UTC()).Hours())
		items = append(items, closeDeadlineOverdueItem{
			OrderID: order.ID, OrderNo: order.OrderNo,
			ResponsibleAdminID: order.ResponsibleAdminID,
			EnteredPendingAt:   order.UpdatedAt.UTC(),
			HoursOverdue:       overdue,
			FulfillmentStatus:  order.FulfillmentStatus.String(),
		})
	}
	return items, nil
}

// closeDeadlineOverdueCount 超结单时限未结单的单量（客服待办计数用，§2.1-09）。
func closeDeadlineOverdueCount(ctx context.Context, db *ent.Client, now time.Time) (int, error) {
	items, err := listCloseDeadlineOverdue(ctx, db, now, 1000)
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

// scanCloseDeadlineTask 结单时限扫描任务（§2.1-09）：把超时未结单订单写进客服 / 管理员待办
// （只增的事务外盒事件，按订单 + 业务日去重），供 cs 待办列表与看板查询。
// 时限未配置时直接跳过（0 条）。
func scanCloseDeadlineTask(ctx context.Context, logger io.Writer) error {
	count, err := scanCloseDeadlineOverdue(ctx, global.ORM, time.Now(), 200)
	if err != nil {
		return err
	}
	if logger != nil {
		fmt.Fprintf(logger, "esports close deadline scan: %d overdue orders\n", count)
	}
	return nil
}

// scanCloseDeadlineOverdue 扫描并落「超结单时限」待办（§2.1-09），重复运行幂等。
func scanCloseDeadlineOverdue(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	items, err := listCloseDeadlineOverdue(ctx, db, now, limit)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, item := range items {
		eventKey := fmt.Sprintf("order.close_deadline_overdue:%s:%s",
			item.OrderNo, now.UTC().Format("2006-01-02"))
		if _, err = db.EsportsOutbox.Create().SetEventKey(eventKey).
			SetEventType("order.close_deadline_overdue").SetAggregateRef(item.OrderNo).
			SetPayload(map[string]interface{}{
				"order_no": item.OrderNo, "order_id": item.OrderID,
				"responsible_admin_id": item.ResponsibleAdminID,
				"hours_overdue":        item.HoursOverdue,
				"fulfillment_status":   item.FulfillmentStatus,
			}).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				continue
			}
			return created, err
		}
		created++
	}
	return created, nil
}
