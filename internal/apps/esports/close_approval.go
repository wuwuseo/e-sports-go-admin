package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscloseapproval"
	"go-admin/internal/ent/esportscloseapprovalflow"
	"go-admin/internal/ent/esportsorder"
)

// 结单审批流（§3.4）：节点串行可增删，每个节点配审批角色（admin 任一管理员 / super_admin 超级管理员）
// 与超时策略；涉及资金 / 争议的节点禁止自动通过。
// 订单进入待复核时建立一条审批进度 `EsportsCloseApproval`，审批逐个节点推进，末节点通过才真正结单。
// 未配置审批流时回落结单规则的金额复核阈值，行为等价于内置的单节点「管理员复核」（flow_id = 0）。

// ── 审批流配置（版本化：新增 / 生效 / 回滚） ──

type closeApprovalFlowInput struct {
	Code     string                     `json:"code"`
	Name     string                     `json:"name"`
	Scope    string                     `json:"scope"`
	ScopeKey string                     `json:"scope_key"`
	MinCents int64                      `json:"min_cents"`
	Nodes    []schema.CloseApprovalNode `json:"nodes"`
	Note     string                     `json:"note"`
}

// validCloseApprovalRole 审批角色：任一管理员 / 超级管理员。
func validCloseApprovalRole(role string) bool {
	return role == "admin" || role == "super_admin"
}

// normalizeCloseApprovalFlowInput 校验审批流定义：节点序号从 1 连续递增、角色合法、资金节点不得自动通过。
func normalizeCloseApprovalFlowInput(in closeApprovalFlowInput) (closeApprovalFlowInput, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.Note = strings.TrimSpace(in.Note)
	if in.Code == "" || len(in.Code) > 48 || in.Name == "" || len([]rune(in.Name)) > 64 ||
		!validCloseFormScope(in.Scope) || len([]rune(in.ScopeKey)) > 64 ||
		in.MinCents < 0 || len([]rune(in.Note)) > 255 {
		return in, errInvalidInput
	}
	if in.Scope != "global" && in.ScopeKey == "" {
		return in, fmt.Errorf("%w：按游戏分区 / SKU 绑定时须填写范围标识", errInvalidInput)
	}
	if len(in.Nodes) == 0 {
		return in, fmt.Errorf("%w：审批流须至少配置一个节点", errInvalidInput)
	}
	nodes := make([]schema.CloseApprovalNode, 0, len(in.Nodes))
	for i, node := range in.Nodes {
		node.Name = strings.TrimSpace(node.Name)
		if node.Seq != i+1 || node.Name == "" || len([]rune(node.Name)) > 64 ||
			!validCloseApprovalRole(node.Role) ||
			node.TimeoutHours < 0 || node.TimeoutHours > 24*30 {
			return in, fmt.Errorf("%w：审批节点须从 1 连续递增，且名称 / 角色 / 超时（0~720 小时）合法", errInvalidInput)
		}
		if node.InvolvesFunds && node.AutoPassOnTimeout {
			return in, fmt.Errorf("%w：涉及资金 / 争议的节点不能自动通过（§3.4）", errInvalidInput)
		}
		nodes = append(nodes, node)
	}
	in.Nodes = nodes
	return in, nil
}

// createCloseApprovalFlow 新建审批流（draft）。同 code 已存在则版本 = 现有最大 version + 1。
func createCloseApprovalFlow(ctx context.Context, db *ent.Client, in closeApprovalFlowInput, adminID int) (*ent.EsportsCloseApprovalFlow, error) {
	in, err := normalizeCloseApprovalFlowInput(in)
	if err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	version := 1
	latest, err := tx.EsportsCloseApprovalFlow.Query().Where(
		esportscloseapprovalflow.CodeEQ(in.Code),
	).Order(ent.Desc(esportscloseapprovalflow.FieldVersion)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if latest != nil {
		version = latest.Version + 1
	}
	item, err := tx.EsportsCloseApprovalFlow.Create().SetCode(in.Code).SetName(in.Name).
		SetScope(esportscloseapprovalflow.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMinCents(in.MinCents).SetNodes(in.Nodes).
		SetVersion(version).SetCreatedByAdminID(adminID).SetNote(in.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseApprovalFlow.Get(ctx, item.ID)
}

// updateCloseApprovalFlow 编辑草稿（乐观锁，须为 draft）；code 不可改。
func updateCloseApprovalFlow(ctx context.Context, db *ent.Client, id, expectedVersion int, in closeApprovalFlowInput) (*ent.EsportsCloseApprovalFlow, error) {
	in, err := normalizeCloseApprovalFlowInput(in)
	if err != nil {
		return nil, err
	}
	changed, err := db.EsportsCloseApprovalFlow.Update().Where(
		esportscloseapprovalflow.IDEQ(id),
		esportscloseapprovalflow.VersionEQ(expectedVersion),
		esportscloseapprovalflow.StatusEQ(esportscloseapprovalflow.StatusDraft),
	).SetName(in.Name).SetScope(esportscloseapprovalflow.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMinCents(in.MinCents).SetNodes(in.Nodes).SetNote(in.Note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsCloseApprovalFlow.Get(ctx, id)
}

// activateCloseApprovalFlow 生效：同 code 现有 active 转 history，本行（须 draft）转 active。
func activateCloseApprovalFlow(ctx context.Context, db *ent.Client, id int) (*ent.EsportsCloseApprovalFlow, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	target, err := tx.EsportsCloseApprovalFlow.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if target.Status != esportscloseapprovalflow.StatusDraft {
		return nil, errConflict
	}
	if _, err = tx.EsportsCloseApprovalFlow.Update().Where(
		esportscloseapprovalflow.CodeEQ(target.Code),
		esportscloseapprovalflow.StatusEQ(esportscloseapprovalflow.StatusActive),
	).SetStatus(esportscloseapprovalflow.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCloseApprovalFlow.Update().Where(
		esportscloseapprovalflow.IDEQ(id),
		esportscloseapprovalflow.StatusEQ(esportscloseapprovalflow.StatusDraft),
	).SetStatus(esportscloseapprovalflow.StatusActive).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseApprovalFlow.Get(ctx, id)
}

// rollbackCloseApprovalFlow 回滚到指定版本：以该行配置为模板新建 max version+1 的直接 active 行，
// 并把原 active 转 history（原历史行不可变，§12.4）。
func rollbackCloseApprovalFlow(ctx context.Context, db *ent.Client, id, adminID int) (*ent.EsportsCloseApprovalFlow, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := tx.EsportsCloseApprovalFlow.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	latest, err := tx.EsportsCloseApprovalFlow.Query().Where(
		esportscloseapprovalflow.CodeEQ(source.Code),
	).Order(ent.Desc(esportscloseapprovalflow.FieldVersion)).First(ctx)
	if err != nil {
		return nil, err
	}
	created, err := tx.EsportsCloseApprovalFlow.Create().SetCode(source.Code).SetName(source.Name).
		SetScope(source.Scope).SetScopeKey(source.ScopeKey).SetMinCents(source.MinCents).
		SetNodes(source.Nodes).SetVersion(latest.Version + 1).
		SetStatus(esportscloseapprovalflow.StatusActive).
		SetCreatedByAdminID(adminID).SetNote(source.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsCloseApprovalFlow.Update().Where(
		esportscloseapprovalflow.CodeEQ(source.Code),
		esportscloseapprovalflow.StatusEQ(esportscloseapprovalflow.StatusActive),
		esportscloseapprovalflow.IDNEQ(created.ID),
	).SetStatus(esportscloseapprovalflow.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseApprovalFlow.Get(ctx, created.ID)
}

// listCloseApprovalFlows 列出审批流（按 ID 倒序）。
func listCloseApprovalFlows(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsCloseApprovalFlow, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsCloseApprovalFlow.Query().
		Order(ent.Desc(esportscloseapprovalflow.FieldID)).Limit(limit).All(ctx)
}

// resolveCloseApprovalFlow 解析订单适用的生效审批流：sku > game > global，同层取命中金额阈值
// （订单应付 ≥ min_cents）且 version 最大；无命中返回 nil, nil（回落结单规则的复核阈值）。
func resolveCloseApprovalFlow(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (*ent.EsportsCloseApprovalFlow, error) {
	active, err := db.EsportsCloseApprovalFlow.Query().Where(
		esportscloseapprovalflow.StatusEQ(esportscloseapprovalflow.StatusActive),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	pick := func(scope esportscloseapprovalflow.Scope, key string) *ent.EsportsCloseApprovalFlow {
		var best *ent.EsportsCloseApprovalFlow
		for _, item := range active {
			if item.Scope != scope || item.ScopeKey != key || order.PayableCents < item.MinCents {
				continue
			}
			if best == nil || item.Version > best.Version {
				best = item
			}
		}
		return best
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err == nil {
		if item := pick(esportscloseapprovalflow.ScopeSku, sku.Code); item != nil {
			return item, nil
		}
		if sku.CategoryID != nil {
			if category, err := db.EsportsProductCategory.Get(ctx, *sku.CategoryID); err == nil {
				if game := strings.TrimSpace(category.Game); game != "" {
					if item := pick(esportscloseapprovalflow.ScopeGame, game); item != nil {
						return item, nil
					}
				}
			}
		}
	}
	return pick(esportscloseapprovalflow.ScopeGlobal, ""), nil
}

// ── 订单审批进度 ──

// closeApprovalStepsOf 由审批流节点派生初始步骤（全部 pending）；无流时用内置单节点「管理员复核」。
func closeApprovalStepsOf(flow *ent.EsportsCloseApprovalFlow) []schema.CloseApprovalStep {
	if flow == nil || len(flow.Nodes) == 0 {
		return []schema.CloseApprovalStep{{Seq: 1, Name: "管理员复核", Role: "admin", Status: "pending"}}
	}
	steps := make([]schema.CloseApprovalStep, 0, len(flow.Nodes))
	for _, node := range flow.Nodes {
		steps = append(steps, schema.CloseApprovalStep{
			Seq: node.Seq, Name: node.Name, Role: node.Role, Status: "pending",
		})
	}
	return steps
}

// upsertCloseApprovalIn 建立 / 重置订单的审批进度（重复结单时以最新生效流重建）。
func upsertCloseApprovalIn(ctx context.Context, tx *ent.Tx, orderID int,
	flow *ent.EsportsCloseApprovalFlow) (*ent.EsportsCloseApproval, error) {
	if _, err := tx.EsportsCloseApproval.Delete().Where(
		esportscloseapproval.OrderIDEQ(orderID),
	).Exec(ctx); err != nil {
		return nil, err
	}
	create := tx.EsportsCloseApproval.Create().SetOrderID(orderID).
		SetCurrentSeq(1).SetStatus(esportscloseapproval.StatusPending).
		SetSteps(closeApprovalStepsOf(flow))
	if flow != nil {
		create = create.SetFlowID(flow.ID).SetFlowCode(flow.Code).SetFlowVersion(flow.Version)
	}
	return create.Save(ctx)
}

// startCloseApprovalIfNeeded 判断订单是否需要审批：优先按生效审批流（金额阈值命中），
// 否则回落结单规则的金额复核阈值；需要时写入审批进度并返回，否则返回 nil。
func startCloseApprovalIfNeeded(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	reviewThresholdCents int64) (*ent.EsportsCloseApproval, error) {
	flow, err := resolveCloseApprovalFlow(ctx, tx.Client(), order)
	if err != nil {
		return nil, err
	}
	if flow != nil {
		return upsertCloseApprovalIn(ctx, tx, order.ID, flow)
	}
	if reviewThresholdCents > 0 && order.PayableCents >= reviewThresholdCents {
		return upsertCloseApprovalIn(ctx, tx, order.ID, nil)
	}
	return nil, nil
}

// currentCloseApprovalStep 返回当前待审批节点在 steps 中的下标与副本。
func currentCloseApprovalStep(approval *ent.EsportsCloseApproval) (int, *schema.CloseApprovalStep) {
	for i := range approval.Steps {
		if approval.Steps[i].Seq == approval.CurrentSeq && approval.Steps[i].Status == "pending" {
			step := approval.Steps[i]
			return i, &step
		}
	}
	return -1, nil
}

// nextPendingApprovalSeq 返回 currentSeq 之后第一个 pending 节点序号；没有则返回 0。
func nextPendingApprovalSeq(steps []schema.CloseApprovalStep, currentSeq int) int {
	next := 0
	for _, step := range steps {
		if step.Seq > currentSeq && step.Status == "pending" && (next == 0 || step.Seq < next) {
			next = step.Seq
		}
	}
	return next
}

// previousCloseApprovalStepAt 返回 seq 之前最近一个已处理节点的处理时间（用于节点超时计时锚点）。
func previousCloseApprovalStepAt(approval *ent.EsportsCloseApproval, seq int) *time.Time {
	var picked *time.Time
	best := 0
	for _, step := range approval.Steps {
		if step.Seq >= seq || step.At == nil {
			continue
		}
		if step.Seq > best {
			best = step.Seq
			at := *step.At
			picked = &at
		}
	}
	return picked
}

// closeApprovalNodeTimeoutDeadline 计算当前节点的超时截止时间；节点未配超时返回 ok=false。
func closeApprovalNodeTimeoutDeadline(approval *ent.EsportsCloseApproval,
	node *schema.CloseApprovalStep, flow *ent.EsportsCloseApprovalFlow) (time.Time, bool) {
	timeoutHours := closeApprovalNodeTimeoutHours(flow, node.Seq)
	if timeoutHours <= 0 {
		return time.Time{}, false
	}
	anchor := approval.CreatedAt.UTC()
	if prev := previousCloseApprovalStepAt(approval, node.Seq); prev != nil {
		anchor = prev.UTC()
	}
	return anchor.Add(time.Duration(timeoutHours) * time.Hour), true
}

// closeApprovalNodeTimeoutHours 取该节点在生效流中的超时小时数；无流（内置节点）时为 0（不限）。
func closeApprovalNodeTimeoutHours(flow *ent.EsportsCloseApprovalFlow, seq int) int {
	if flow == nil {
		return 0
	}
	for _, node := range flow.Nodes {
		if node.Seq == seq {
			return node.TimeoutHours
		}
	}
	return 0
}

// closeApprovalNodeAutoPass 判断该节点是否允许超时自动通过（无流或资金节点一律不允许）。
func closeApprovalNodeAutoPass(flow *ent.EsportsCloseApprovalFlow, seq int) bool {
	if flow == nil {
		return false
	}
	for _, node := range flow.Nodes {
		if node.Seq == seq {
			return node.AutoPassOnTimeout && !node.InvolvesFunds
		}
	}
	return false
}

// approveCloseApprovalIn 审批通过当前节点：还有后续节点则只推进，否则真正结单。
// 节点角色为 super_admin 时要求审批人是超级管理员；末节点通过不再单独写节点事件，
// 以免与 `order.closed` 争用同一 order_version。
func approveCloseApprovalIn(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	approval *ent.EsportsCloseApproval, adminID int, isSuper, autoPassed bool,
	note string, now time.Time) error {
	index, node := currentCloseApprovalStep(approval)
	if node == nil {
		return errConflict
	}
	if node.Role == "super_admin" && !isSuper && !autoPassed {
		return fmt.Errorf("%w：该节点须超级管理员审批", errForbidden)
	}
	at := now.UTC()
	steps := approval.Steps
	steps[index].Status = "approved"
	steps[index].AdminID = adminID
	steps[index].Reason = note
	steps[index].AutoPassed = autoPassed
	steps[index].At = &at
	next := nextPendingApprovalSeq(steps, approval.CurrentSeq)
	update := tx.EsportsCloseApproval.Update().Where(
		esportscloseapproval.IDEQ(approval.ID),
		esportscloseapproval.VersionEQ(approval.Version),
		esportscloseapproval.StatusEQ(esportscloseapproval.StatusPending),
	).SetSteps(steps).SetCurrentSeq(approval.CurrentSeq).AddVersion(1)
	if next > 0 {
		update = update.SetCurrentSeq(next)
	} else {
		update = update.SetStatus(esportscloseapproval.StatusApproved)
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	if next == 0 {
		actorType := "admin"
		if autoPassed {
			actorType = "system"
		}
		// §3.4-03：复核通过也要在结单留痕里带上「已查看凭证附件 + 复核人」，
		// 凭证集合在结单申请环节已被强制确认过，这里按订单服务段重新汇总落库。
		extra := map[string]interface{}{"viewed_by_admin_id": adminID}
		if reviewed, err := serviceEvidenceAttachmentIDs(ctx, tx.Client(), order.ID); err != nil {
			return err
		} else if len(reviewed) > 0 {
			extra["viewed_evidence_attachment_ids"] = reviewed
		}
		return finalizeCloseOrderIn(ctx, tx, order, adminID, actorType, note, extra, now)
	}
	// 中间节点：先推进订单版本，再按新版本号写事件（事件按 order_version 唯一）。
	newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
	if err != nil {
		return err
	}
	actorType, actorID := "admin", strconv.Itoa(adminID)
	if autoPassed {
		actorType, actorID = "system", "system"
	}
	_, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.close_approval_node_approved").
		SetActorType(actorType).SetActorID(actorID).
		SetDetails(map[string]interface{}{
			"node_seq": node.Seq, "node_name": node.Name, "role": node.Role,
			"auto_passed": autoPassed, "next_seq": next, "reason": note,
		}).Save(ctx)
	return err
}

// rejectCloseApprovalIn 驳回当前节点：审批进度转 rejected，订单退回待结单（可修改后重新结单）。
func rejectCloseApprovalIn(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	approval *ent.EsportsCloseApproval, adminID int, isSuper bool, reason string,
	now time.Time) error {
	index, node := currentCloseApprovalStep(approval)
	if node == nil {
		return errConflict
	}
	if node.Role == "super_admin" && !isSuper {
		return fmt.Errorf("%w：该节点须超级管理员审批", errForbidden)
	}
	at := now.UTC()
	steps := approval.Steps
	steps[index].Status = "rejected"
	steps[index].AdminID = adminID
	steps[index].Reason = reason
	steps[index].At = &at
	changed, err := tx.EsportsCloseApproval.Update().Where(
		esportscloseapproval.IDEQ(approval.ID),
		esportscloseapproval.VersionEQ(approval.Version),
		esportscloseapproval.StatusEQ(esportscloseapproval.StatusPending),
	).SetSteps(steps).SetStatus(esportscloseapproval.StatusRejected).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	changed, err = tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingCloseReview),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	_, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.close_review_rejected").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"node_seq": node.Seq, "node_name": node.Name, "role": node.Role, "reason": reason,
		}).Save(ctx)
	return err
}

// closeApprovalView 是订单的结单审批进度视图（含当前待审批节点）。
type closeApprovalView struct {
	Approval *ent.EsportsCloseApproval `json:"approval"`
	Current  *schema.CloseApprovalStep `json:"current_node"`
}

// orderCloseApproval 返回订单的结单审批进度与当前节点。
func orderCloseApproval(ctx context.Context, db *ent.Client, orderID int) (*closeApprovalView, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	if _, err := db.EsportsOrder.Get(ctx, orderID); err != nil {
		return nil, err
	}
	approval, err := db.EsportsCloseApproval.Query().Where(
		esportscloseapproval.OrderIDEQ(orderID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return &closeApprovalView{}, nil
	}
	if err != nil {
		return nil, err
	}
	_, node := currentCloseApprovalStep(approval)
	return &closeApprovalView{Approval: approval, Current: node}, nil
}

// checkCloseApprovalTimeouts 扫描待复核订单的当前审批节点：配了超时且允许自动通过的节点超时后自动通过
// 并推进 / 结单，否则写 `close.approval_timeout` 事务外盒升级。返回处理条数。
// 幂等：外盒键含订单与节点序号与阈值，同阈值只升级一次；调整阈值后按新阈值再评估。
func checkCloseApprovalTimeouts(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingCloseReview),
	).Order(ent.Asc(esportsorder.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, order := range orders {
		approval, err := db.EsportsCloseApproval.Query().Where(
			esportscloseapproval.OrderIDEQ(order.ID),
			esportscloseapproval.StatusEQ(esportscloseapproval.StatusPending),
		).Only(ctx)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return handled, err
		}
		_, node := currentCloseApprovalStep(approval)
		if node == nil {
			continue
		}
		var flow *ent.EsportsCloseApprovalFlow
		if approval.FlowID > 0 {
			if flow, err = db.EsportsCloseApprovalFlow.Get(ctx, approval.FlowID); err != nil {
				if !ent.IsNotFound(err) {
					return handled, err
				}
				flow = nil
			}
		}
		deadline, ok := closeApprovalNodeTimeoutDeadline(approval, node, flow)
		if !ok || now.UTC().Before(deadline) {
			continue
		}
		if closeApprovalNodeAutoPass(flow, node.Seq) {
			if err = autoPassCloseApproval(ctx, db, order, approval, now); err != nil {
				return handled, err
			}
			handled++
			continue
		}
		if _, err = db.EsportsOutbox.Create().
			SetEventKey(fmt.Sprintf("close_approval_timeout:%s:%d:%d",
				order.OrderNo, node.Seq, closeApprovalNodeTimeoutHours(flow, node.Seq))).
			SetEventType("close.approval_timeout").SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{
				"order_no": order.OrderNo, "node_seq": node.Seq, "node_name": node.Name,
				"role": node.Role, "limit_hours": closeApprovalNodeTimeoutHours(flow, node.Seq),
				"deadline": deadline.Format(time.RFC3339),
			}).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				continue
			}
			return handled, err
		}
		handled++
	}
	return handled, nil
}

// autoPassCloseApproval 事务内自动通过当前节点（超时自动通过）：先复核状态与版本，避免与人工审批并发。
func autoPassCloseApproval(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	approval *ent.EsportsCloseApproval, now time.Time) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	latest, err := tx.EsportsCloseApproval.Get(ctx, approval.ID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return err
	}
	if latest.Status != esportscloseapproval.StatusPending || latest.Version != approval.Version {
		return nil
	}
	currentOrder, err := tx.EsportsOrder.Get(ctx, order.ID)
	if err != nil {
		return err
	}
	// 存单冻结期间不自动通过审批节点，也不推进结单：先恢复存单再由人工或下一轮超时扫描处理。
	if err = ensureOrderNotFrozen(currentOrder); err != nil {
		return err
	}
	if currentOrder.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		return nil
	}
	if err = approveCloseApprovalIn(ctx, tx, currentOrder, latest, 0, true, true, "超时自动通过", now); err != nil {
		return err
	}
	return tx.Commit()
}
