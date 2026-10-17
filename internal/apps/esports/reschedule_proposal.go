package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsrescheduleconfirmation"
	"go-admin/internal/ent/esportsrescheduleproposal"
	"go-admin/internal/ent/systemconfig"
)

// §3.3 限时改期提案闭环（已付款订单）。
//
// 已付款订单的档期是「已占用并被派单席位引用」的资源，不能由客服单方面改写，因此改期改为：
//
//	提案 proposeReschedule（客服，不改订单）
//	  → 客户确认 + 订单上已接单成员逐人确认（deny 即否决）
//	  → 全部确认齐备时在**一个事务内**切换订单档期并复核 / 占用目标档期资源（applyRescheduleProposal）
//	  → 超时由定时任务 expireRescheduleProposals 收敛为 expired
//
// 未付款订单仍走既有 §3.2 的直接改期（PUT /admin/orders/{id}/appointment），本文件不改变它的口径。
const (
	// rescheduleProposalHoursKey 改期提案的确认时限（小时，§3.3）。
	rescheduleProposalHoursKey = "app.esports.reschedule_proposal_hours"
	// defaultRescheduleProposalHours 提案确认时限缺省值（小时）。
	defaultRescheduleProposalHours = 24
	// rescheduleProposalListLimit 单订单提案历史返回上限。
	rescheduleProposalListLimit = 20
	// maxRescheduleDurationMinutes 目标服务时长上限（分钟，与 SKU 的 0–1440 口径一致）。
	maxRescheduleDurationMinutes = 24 * 60
)

var (
	// errRescheduleProposalRequired 已付款订单改期必须走限时改期提案（§3.3）：
	// 由 HTTP 入口映射为明确中文提示，指向改期提案接口。
	errRescheduleProposalRequired = errors.New("已付款订单改期须走限时改期提案")
)

// rescheduleConfirmer 标识一名确认人：客户（已绑定身份）或订单上的已接单成员。
type rescheduleConfirmer struct {
	Type esportsrescheduleconfirmation.ConfirmerType
	ID   int
}

func rescheduleProposalNo() string { return "RP" + uuid.NewString() }

// rescheduleProposalInput 发起改期提案的入参。ServiceDurationMinutes 为 0 时沿用订单上的服务时长快照。
type rescheduleProposalInput struct {
	ExpectedVersion        int       `json:"expected_version"`
	StartAt                time.Time `json:"start_at"`
	ServiceDurationMinutes int       `json:"service_duration_minutes"`
	Reason                 string    `json:"reason"`
}

// rescheduleProposalDecisionInput 确认 / 拒绝提案的入参。
type rescheduleProposalDecisionInput struct {
	Accept bool   `json:"accept"`
	Note   string `json:"note"`
}

// rescheduleProposalHours 返回改期提案确认时限（小时，§3.3），缺省 24。
// 配置缺失或非法时回落缺省值，不因配置问题阻断改期。
func rescheduleProposalHours(ctx context.Context, db *ent.Client) int {
	item, err := db.SystemConfig.Query().Where(
		systemconfig.KeyEQ(rescheduleProposalHoursKey)).Only(ctx)
	if err != nil {
		return defaultRescheduleProposalHours
	}
	hours, convErr := strconv.Atoi(strings.TrimSpace(item.Value))
	if convErr != nil || hours < 1 || hours > 24*30 {
		return defaultRescheduleProposalHours
	}
	return hours
}

// proposeReschedule 客服对**已付款在途订单**发起限时改期提案（§3.3）。
// 提案本身**不改订单**：只落一张 pending 提案（含原档期快照、目标档期、确认时限、提案人与备注），
// 并写 `order.reschedule_proposed` 外盒留痕。确认时限取 `app.esports.reschedule_proposal_hours`
// （缺省 24 小时）且不晚于目标开始时间。
// 「同一订单同一时刻只允许一张进行中的提案」在事务内查重，并由 active_order_id 唯一索引兜底。
func proposeReschedule(ctx context.Context, db *ent.Client, orderID, adminID int,
	in rescheduleProposalInput, now time.Time) (*ent.EsportsRescheduleProposal, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if orderID < 1 || adminID < 1 || in.ExpectedVersion < 1 || in.Reason == "" ||
		len([]rune(in.Reason)) > 255 || in.StartAt.IsZero() || !in.StartAt.After(now.UTC()) ||
		in.ServiceDurationMinutes < 0 || in.ServiceDurationMinutes > maxRescheduleDurationMinutes {
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
	// 仅已付款在途订单走提案：未付款仍走 §3.2 直接改期，终态订单不可改期。
	if order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled ||
		order.Version != in.ExpectedVersion {
		return nil, errConflict
	}
	duration := in.ServiceDurationMinutes
	if duration == 0 {
		duration = order.ServiceDurationMinutes
	}
	if duration <= 0 {
		return nil, fmt.Errorf("%w：请指定目标服务时长，才能按 §5.5 时段口径复核目标档期", errInvalidInput)
	}
	start := in.StartAt.UTC()
	deadline := now.UTC().Add(time.Duration(rescheduleProposalHours(ctx, tx.Client())) * time.Hour)
	if deadline.After(start) {
		deadline = start
	}
	duplicated, err := tx.EsportsRescheduleProposal.Query().Where(
		esportsrescheduleproposal.OrderIDEQ(orderID),
		esportsrescheduleproposal.StatusEQ(esportsrescheduleproposal.StatusPending),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, errConflict
	}
	create := tx.EsportsRescheduleProposal.Create().
		SetProposalNo(rescheduleProposalNo()).
		SetOrderID(orderID).SetOrderVersion(order.Version).
		SetFromServiceDurationMinutes(order.ServiceDurationMinutes).
		SetToStartAt(start).SetToServiceDurationMinutes(duration).
		SetExpiresAt(deadline).SetProposedByAdminID(adminID).
		SetReason(in.Reason).SetActiveOrderID(orderID)
	if order.RequestedStartAt != nil {
		create.SetFromStartAt(order.RequestedStartAt.UTC())
	}
	item, err := create.Save(ctx)
	if err != nil {
		// active_order_id 唯一索引兜底：并发下另一张进行中的提案已先落库。
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.reschedule_proposed:" + item.ProposalNo).
		SetEventType("order.reschedule_proposed").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "proposal_no": item.ProposalNo,
			"to_start_at":                 start.Format(time.RFC3339),
			"to_service_duration_minutes": duration,
			"expires_at":                  deadline.Format(time.RFC3339),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsRescheduleProposal.Get(ctx, item.ID)
}

// confirmRescheduleProposal 记录一次提案确认（§3.3）。客户与每名已接单成员各自调用一次；
// accept=false 表示拒绝，提案立即终结为 rejected（订单不变）。
//
// 确认与生效分成两个事务：确认先落库（唯一索引保证同一人只留一条、重复确认幂等），
// 确认齐备时再尝试在独立事务里生效。这样并发确认时后到的一方不会因生效事务回滚而丢掉自己的确认记录。
// 目标档期不可用属硬失败，整单回滚（订单与资源都不落库），由客服改约或撤回后重新提案。
func confirmRescheduleProposal(ctx context.Context, db *ent.Client, proposalID int,
	confirmer rescheduleConfirmer, accept bool, note string, now time.Time) (*ent.EsportsRescheduleProposal, error) {
	note = strings.TrimSpace(note)
	if proposalID < 1 || confirmer.ID < 1 || len([]rune(note)) > 255 ||
		(confirmer.Type != esportsrescheduleconfirmation.ConfirmerTypeCustomer &&
			confirmer.Type != esportsrescheduleconfirmation.ConfirmerTypePlayer) {
		return nil, errInvalidInput
	}
	if err := recordRescheduleConfirmation(ctx, db, proposalID, confirmer, accept, note, now); err != nil {
		return nil, err
	}
	if accept {
		if _, err := applyRescheduleProposal(ctx, db, proposalID, now); err != nil {
			if errors.Is(err, errAppointmentResource) {
				return nil, err
			}
			// 并发下提案已被他人推进 / 订单已被其它流程改动：吞掉竞争，返回当前状态由调用方收敛。
			if !errors.Is(err, errConflict) {
				return nil, err
			}
		}
	}
	return db.EsportsRescheduleProposal.Get(ctx, proposalID)
}

// confirmRescheduleProposalByCustomer 客户自助确认 / 拒绝改期提案（§3.3）：按登录用户解析
// 「已绑定身份的客户」（customerForVerifiedUser 要求 User 为 active 且存在 active 身份绑定），
// 非本人订单返回 403。**不提供客服代客户确认的入口**，避免绕过客户同意。
func confirmRescheduleProposalByCustomer(ctx context.Context, db *ent.Client, uid string,
	proposalID int, accept bool, note string, now time.Time) (*ent.EsportsRescheduleProposal, error) {
	customer, err := customerForVerifiedUser(ctx, db, uid)
	if err != nil {
		return nil, err
	}
	return confirmRescheduleProposal(ctx, db, proposalID, rescheduleConfirmer{
		Type: esportsrescheduleconfirmation.ConfirmerTypeCustomer, ID: customer.ID,
	}, accept, note, now)
}

// confirmRescheduleProposalByPlayer 订单上已接单成员自助确认 / 拒绝改期提案（§3.3）：
// 按登录用户解析陪玩档案，本人未在该订单持有 accepted 席位时由服务层返回 403。
func confirmRescheduleProposalByPlayer(ctx context.Context, db *ent.Client, uid string,
	proposalID int, accept bool, note string, now time.Time) (*ent.EsportsRescheduleProposal, error) {
	player, err := currentPlayer(ctx, db, uid)
	if err != nil {
		return nil, err
	}
	return confirmRescheduleProposal(ctx, db, proposalID, rescheduleConfirmer{
		Type: esportsrescheduleconfirmation.ConfirmerTypePlayer, ID: player.ID,
	}, accept, note, now)
}

// recordRescheduleConfirmation 在事务内校验确认人资格并落一条确认明细（§3.3）：
// 客户须为订单客户（「已绑定身份」由入口 customerForVerifiedUser 保证），
// 成员须在本单持有 accepted 席位。同一人重复提交同一结论幂等，改动结论拒绝。
func recordRescheduleConfirmation(ctx context.Context, db *ent.Client, proposalID int,
	confirmer rescheduleConfirmer, accept bool, note string, now time.Time) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	item, err := tx.EsportsRescheduleProposal.Get(ctx, proposalID)
	if err != nil {
		return err
	}
	// 已终结 / 已超时 / 目标开始时间已过的提案不再接受确认。
	if item.Status != esportsrescheduleproposal.StatusPending ||
		!item.ExpiresAt.After(now.UTC()) || !item.ToStartAt.After(now.UTC()) {
		return errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, item.OrderID)
	if err != nil {
		return err
	}
	seatIndex := 0
	switch confirmer.Type {
	case esportsrescheduleconfirmation.ConfirmerTypeCustomer:
		if order.CustomerID != confirmer.ID {
			return errForbidden
		}
	case esportsrescheduleconfirmation.ConfirmerTypePlayer:
		seat, seatErr := tx.EsportsDispatchSeat.Query().Where(
			esportsdispatchseat.OrderIDEQ(item.OrderID),
			esportsdispatchseat.PlayerIDEQ(confirmer.ID),
			esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
		).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).First(ctx)
		if ent.IsNotFound(seatErr) {
			return fmt.Errorf("%w：仅本单已接单成员可确认改期提案", errForbidden)
		}
		if seatErr != nil {
			return seatErr
		}
		seatIndex = seat.SeatIndex
	}
	decision := esportsrescheduleconfirmation.DecisionConfirmed
	if !accept {
		decision = esportsrescheduleconfirmation.DecisionRejected
	}
	prior, err := tx.EsportsRescheduleConfirmation.Query().Where(
		esportsrescheduleconfirmation.ProposalIDEQ(proposalID),
		esportsrescheduleconfirmation.ConfirmerTypeEQ(confirmer.Type),
		esportsrescheduleconfirmation.ConfirmerIDEQ(confirmer.ID),
	).Only(ctx)
	switch {
	case err == nil:
		if prior.Decision != decision {
			return errConflict // 已表态过：改结论须撤回后重新提案
		}
	case ent.IsNotFound(err):
		if _, err = tx.EsportsRescheduleConfirmation.Create().
			SetProposalID(proposalID).SetOrderID(item.OrderID).
			SetConfirmerType(confirmer.Type).SetConfirmerID(confirmer.ID).
			SetSeatIndex(seatIndex).SetDecision(decision).SetNote(note).
			SetDecidedAt(now.UTC()).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return errConflict
			}
			return err
		}
	default:
		return err
	}
	if !accept {
		// 任一确认人拒绝即否决提案：订单档期保持不变。
		changed, err := tx.EsportsRescheduleProposal.Update().Where(
			esportsrescheduleproposal.IDEQ(proposalID),
			esportsrescheduleproposal.StatusEQ(esportsrescheduleproposal.StatusPending),
		).SetStatus(esportsrescheduleproposal.StatusRejected).
			ClearActiveOrderID().SetDecidedAt(now.UTC()).AddVersion(1).Save(ctx)
		if err != nil {
			return err
		}
		if changed != 1 {
			return errConflict
		}
		if _, err = tx.EsportsOutbox.Create().
			SetEventKey("order.reschedule_rejected:" + item.ProposalNo).
			SetEventType("order.reschedule_rejected").SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{
				"order_no": order.OrderNo, "proposal_no": item.ProposalNo,
				"confirmer_type": confirmer.Type.String(), "confirmer_id": confirmer.ID,
				"note": note,
			}).Save(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// rescheduleConfirmationsComplete 判断提案是否已凑齐全部确认（§3.3）：
// 订单客户一条 confirmed，且订单上每名已接单成员各一条 confirmed。
// 成员集合按**确认时点**订单上的 accepted 席位实时计算：提案建立后又接单的成员同样必须确认。
func rescheduleConfirmationsComplete(ctx context.Context, tx *ent.Tx,
	item *ent.EsportsRescheduleProposal, order *ent.EsportsOrder) (bool, error) {
	rows, err := tx.EsportsRescheduleConfirmation.Query().Where(
		esportsrescheduleconfirmation.ProposalIDEQ(item.ID),
		esportsrescheduleconfirmation.DecisionEQ(esportsrescheduleconfirmation.DecisionConfirmed),
	).All(ctx)
	if err != nil {
		return false, err
	}
	customerConfirmed := false
	playerConfirmed := map[int]bool{}
	for _, row := range rows {
		switch row.ConfirmerType {
		case esportsrescheduleconfirmation.ConfirmerTypeCustomer:
			if row.ConfirmerID == order.CustomerID {
				customerConfirmed = true
			}
		case esportsrescheduleconfirmation.ConfirmerTypePlayer:
			playerConfirmed[row.ConfirmerID] = true
		}
	}
	if !customerConfirmed {
		return false, nil
	}
	members, err := acceptedRescheduleMembers(ctx, tx, item.OrderID)
	if err != nil {
		return false, err
	}
	for _, seat := range members {
		if !playerConfirmed[*seat.PlayerID] {
			return false, nil
		}
	}
	return true, nil
}

// acceptedRescheduleMembers 返回订单上已接单且带陪玩归属的席位（按席位序号升序）。
// 单一事实来源：确认齐备判定与目标档期资源复核都用它，保证「要谁确认」与「给谁占位」一致。
func acceptedRescheduleMembers(ctx context.Context, tx *ent.Tx, orderID int) ([]*ent.EsportsDispatchSeat, error) {
	return tx.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
		esportsdispatchseat.PlayerIDNotNil(),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
}

// targetRescheduleOrder 把目标档期做成内存订单快照，供 checkPlayerDispatchLimit 做
// 「时段重叠 + 服务开始自然日」判定（§5.5），不落库、不替代订单实体。
func targetRescheduleOrder(order *ent.EsportsOrder, item *ent.EsportsRescheduleProposal) *ent.EsportsOrder {
	start := item.ToStartAt.UTC()
	return &ent.EsportsOrder{
		ID: order.ID, SkuID: order.SkuID, RequiredSeats: order.RequiredSeats,
		RequestedStartAt:       &start,
		ServiceDurationMinutes: item.ToServiceDurationMinutes,
	}
}

// sameRescheduleStart 比较两份档期快照是否一致（都为空也算一致）。
func sameRescheduleStart(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UTC().Equal(b.UTC())
}

// applyRescheduleProposal 让「确认已齐」的提案生效（§3.3）。返回 (nil, nil) 表示确认尚未凑齐，本轮不生效。
// 在**同一个事务**内完成：
//
//  1. 复核提案仍进行中、未超时、目标开始时间未过，订单仍处于提案发起时的版本与原档期（防旧提案覆盖新档期）；
//  2. 按既有 §5.5 并发口径复核并**占用**目标档期资源：逐名已接单成员复用
//     reservePlayerDispatchCapacity（陪玩版本条件更新串行化并发占位）+ checkPlayerDispatchLimit
//     的时段重叠 / 服务开始自然日判定，并排除该成员在本单正被改期的席位，避免自己与自己冲突；
//  3. 以 order_version 条件更新把订单 requested_start_at / service_duration_minutes 切到目标档期，
//     appointment_status 置 confirmed 并清空旧确认时限；
//  4. 提案转 confirmed（清 active_order_id）并写订单事件与外盒。
//
// 目标档期任一成员资源不可用时整单回滚：订单档期、提案状态与已占位资源都不落库，
// 错误以 errAppointmentResource 包裹，HTTP 层给出 409 与明确原因。
func applyRescheduleProposal(ctx context.Context, db *ent.Client, proposalID int,
	now time.Time) (*ent.EsportsOrder, error) {
	if proposalID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsRescheduleProposal.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsrescheduleproposal.StatusPending {
		return nil, errConflict
	}
	if !item.ExpiresAt.After(now.UTC()) || !item.ToStartAt.After(now.UTC()) {
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, item.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.Version != item.OrderVersion ||
		order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled ||
		!sameRescheduleStart(order.RequestedStartAt, item.FromStartAt) ||
		order.ServiceDurationMinutes != item.FromServiceDurationMinutes {
		return nil, errConflict
	}
	confirmed, err := rescheduleConfirmationsComplete(ctx, tx, item, order)
	if err != nil {
		return nil, err
	}
	if !confirmed {
		return nil, nil
	}
	target := targetRescheduleOrder(order, item)
	members, err := acceptedRescheduleMembers(ctx, tx, item.OrderID)
	if err != nil {
		return nil, err
	}
	players := map[int]*ent.EsportsPlayer{}
	for _, seat := range members {
		playerID := *seat.PlayerID
		player := players[playerID]
		if player == nil {
			if player, err = tx.EsportsPlayer.Get(ctx, playerID); err != nil {
				return nil, err
			}
			players[playerID] = player
		}
		// 排除该成员在本单的席位：本单占位正是被改期的那一笔，不应与目标档期自相冲突。
		// ponytail: 同一陪玩在同一订单持多个席位时只排除首个席位，未配置接单上限的陪玩不受影响
		// （checkPlayerDispatchLimit 直接放行），该极端场景按保守口径可能误判冲突。
		if err = reservePlayerDispatchCapacity(ctx, tx, player, target, now, seat.ID); err != nil {
			return nil, fmt.Errorf("%w：目标档期不可用（陪玩 %d）：%w", errAppointmentResource, playerID, err)
		}
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(item.OrderVersion),
	).SetRequestedStartAt(item.ToStartAt.UTC()).
		SetServiceDurationMinutes(item.ToServiceDurationMinutes).
		SetAppointmentStatus(esportsorder.AppointmentStatusConfirmed).
		ClearAppointmentConfirmationDeadline().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	newVersion := order.Version + 1
	changed, err = tx.EsportsRescheduleProposal.Update().Where(
		esportsrescheduleproposal.IDEQ(item.ID),
		esportsrescheduleproposal.StatusEQ(esportsrescheduleproposal.StatusPending),
	).SetStatus(esportsrescheduleproposal.StatusConfirmed).
		ClearActiveOrderID().SetDecidedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(newVersion).SetEventType("order.reschedule_proposal_applied").
		SetActorType("system").SetActorID("reschedule_proposal").
		SetDetails(map[string]interface{}{
			"proposal_no":              item.ProposalNo,
			"reason":                   item.Reason,
			"requested_start_at":       item.ToStartAt.UTC().Format(time.RFC3339),
			"service_duration_minutes": item.ToServiceDurationMinutes,
			"confirmed_member_count":   len(players),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.reschedule_applied:" + item.ProposalNo).
		SetEventType("order.reschedule_applied").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "proposal_no": item.ProposalNo,
			"requested_start_at":       item.ToStartAt.UTC().Format(time.RFC3339),
			"service_duration_minutes": item.ToServiceDurationMinutes,
			"confirmed_member_count":   len(players),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, order.ID)
}

// withdrawRescheduleProposal 客服撤回进行中的改期提案（§3.3）：提案转 withdrawn，订单不变。
func withdrawRescheduleProposal(ctx context.Context, db *ent.Client, proposalID, adminID int,
	now time.Time) (*ent.EsportsRescheduleProposal, error) {
	if proposalID < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsRescheduleProposal.Get(ctx, proposalID)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsrescheduleproposal.StatusPending {
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, item.OrderID)
	if err != nil {
		return nil, err
	}
	changed, err := tx.EsportsRescheduleProposal.Update().Where(
		esportsrescheduleproposal.IDEQ(proposalID),
		esportsrescheduleproposal.StatusEQ(esportsrescheduleproposal.StatusPending),
	).SetStatus(esportsrescheduleproposal.StatusWithdrawn).
		ClearActiveOrderID().SetDecidedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.reschedule_withdrawn:" + item.ProposalNo).
		SetEventType("order.reschedule_withdrawn").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "proposal_no": item.ProposalNo,
			"admin_id": adminID,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsRescheduleProposal.Get(ctx, proposalID)
}

// expireRescheduleProposal 过期单张提案：仅 pending 且已过 expires_at 时转 expired，订单不变。
func expireRescheduleProposal(ctx context.Context, db *ent.Client, proposalID int,
	now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsRescheduleProposal.Get(ctx, proposalID)
	if err != nil {
		return false, err
	}
	if item.Status != esportsrescheduleproposal.StatusPending || item.ExpiresAt.After(now.UTC()) {
		return false, nil
	}
	order, err := tx.EsportsOrder.Get(ctx, item.OrderID)
	if err != nil {
		return false, err
	}
	changed, err := tx.EsportsRescheduleProposal.Update().Where(
		esportsrescheduleproposal.IDEQ(proposalID),
		esportsrescheduleproposal.StatusEQ(esportsrescheduleproposal.StatusPending),
		esportsrescheduleproposal.ExpiresAtLTE(now.UTC()),
	).SetStatus(esportsrescheduleproposal.StatusExpired).
		ClearActiveOrderID().SetDecidedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.reschedule_expired:" + item.ProposalNo).
		SetEventType("order.reschedule_expired").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "proposal_no": item.ProposalNo,
			"expires_at": item.ExpiresAt.UTC().Format(time.RFC3339),
		}).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// expireRescheduleProposals 批量过期改期提案（§3.3），仿 expireDispatchInvites：逐个在独立事务内处理，
// 版本竞争失败（errConflict）时跳过，交由下一轮或并发方收敛。返回本轮实际过期的条数。
// 由定时任务 `esports_expire_reschedule_proposals` 周期调用。
func expireRescheduleProposals(ctx context.Context, db *ent.Client, now time.Time,
	limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	ids, err := db.EsportsRescheduleProposal.Query().Where(
		esportsrescheduleproposal.StatusEQ(esportsrescheduleproposal.StatusPending),
		esportsrescheduleproposal.ExpiresAtLTE(now.UTC()),
	).Order(ent.Asc(esportsrescheduleproposal.FieldExpiresAt)).Limit(limit).IDs(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		expired, err := expireRescheduleProposal(ctx, db, id, now)
		if errors.Is(err, errConflict) {
			continue
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

// reschedulePendingConfirmer 尚未表态的确认人。
type reschedulePendingConfirmer struct {
	Type      string `json:"type"`
	ID        int    `json:"id"`
	SeatIndex int    `json:"seat_index"`
}

// rescheduleProposalView 提案 + 确认进度（§3.3）：供客服看到「还差谁的确认」。
type rescheduleProposalView struct {
	*ent.EsportsRescheduleProposal
	Confirmations     []*ent.EsportsRescheduleConfirmation `json:"confirmations"`
	PendingConfirmers []reschedulePendingConfirmer         `json:"pending_confirmers"`
}

// listRescheduleProposals 返回订单的改期提案历史（倒序，最多 20 条）与进行中提案的确认进度。
// 只有 pending 的提案需要确认进度（同一订单至多一张，由 active_order_id 唯一索引兜底），
// 因此这里固定 3 次查询，不随提案条数增长。
func listRescheduleProposals(ctx context.Context, db *ent.Client,
	orderID int) ([]rescheduleProposalView, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	items, err := db.EsportsRescheduleProposal.Query().Where(
		esportsrescheduleproposal.OrderIDEQ(orderID),
	).Order(ent.Desc(esportsrescheduleproposal.FieldID)).
		Limit(rescheduleProposalListLimit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]rescheduleProposalView, 0, len(items))
	if len(items) == 0 {
		return views, nil
	}
	pendingIDs := make([]int, 0, 1)
	for _, item := range items {
		if item.Status == esportsrescheduleproposal.StatusPending {
			pendingIDs = append(pendingIDs, item.ID)
		}
	}
	byProposal := map[int][]*ent.EsportsRescheduleConfirmation{}
	var order *ent.EsportsOrder
	var members []*ent.EsportsDispatchSeat
	if len(pendingIDs) > 0 {
		rows, err := db.EsportsRescheduleConfirmation.Query().Where(
			esportsrescheduleconfirmation.ProposalIDIn(pendingIDs...),
		).Order(ent.Asc(esportsrescheduleconfirmation.FieldID)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			byProposal[row.ProposalID] = append(byProposal[row.ProposalID], row)
		}
		if order, err = db.EsportsOrder.Get(ctx, orderID); err != nil {
			return nil, err
		}
		if members, err = db.EsportsDispatchSeat.Query().Where(
			esportsdispatchseat.OrderIDEQ(orderID),
			esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
			esportsdispatchseat.PlayerIDNotNil(),
		).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx); err != nil {
			return nil, err
		}
	}
	for _, item := range items {
		view := rescheduleProposalView{EsportsRescheduleProposal: item}
		if item.Status != esportsrescheduleproposal.StatusPending {
			views = append(views, view)
			continue
		}
		view.Confirmations = byProposal[item.ID]
		answered := map[rescheduleConfirmer]bool{}
		for _, row := range view.Confirmations {
			answered[rescheduleConfirmer{Type: row.ConfirmerType, ID: row.ConfirmerID}] = true
		}
		if !answered[rescheduleConfirmer{
			Type: esportsrescheduleconfirmation.ConfirmerTypeCustomer, ID: order.CustomerID}] {
			view.PendingConfirmers = append(view.PendingConfirmers, reschedulePendingConfirmer{
				Type: esportsrescheduleconfirmation.ConfirmerTypeCustomer.String(), ID: order.CustomerID,
			})
		}
		for _, seat := range members {
			key := rescheduleConfirmer{
				Type: esportsrescheduleconfirmation.ConfirmerTypePlayer, ID: *seat.PlayerID}
			if answered[key] {
				continue
			}
			view.PendingConfirmers = append(view.PendingConfirmers, reschedulePendingConfirmer{
				Type: esportsrescheduleconfirmation.ConfirmerTypePlayer.String(),
				ID:   *seat.PlayerID, SeatIndex: seat.SeatIndex,
			})
		}
		views = append(views, view)
	}
	return views, nil
}
