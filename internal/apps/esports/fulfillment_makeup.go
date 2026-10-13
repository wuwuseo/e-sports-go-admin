package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsfulfillmentmakeup"
	"go-admin/internal/ent/esportsplayer"
)

// 补履约任务（§13.6）：售后方案 `makeup_service` 的执行动作。
//
// 方案在提交时给出补履约参数（补钟 / 换人、承接陪玩、席位、补足服务量、公司承担报酬），
// 审批通过后由执行动作创建任务；任务本身自带服务段与客服验收，**不增加原订单实收与成交额**，
// 也不改写原订单服务段（原订单服务段对 (order_id, player_id) 唯一，同一陪玩不能重复开段）。

// makeupNo 生成补履约任务号，前缀 MK。
func makeupNo() string { return "MK" + uuid.NewString() }

// validMakeupKind 校验补履约动作类型。
func validMakeupKind(kind string) bool {
	return kind == "makeup_time" || kind == "replace_player"
}

// validMakeupStatus 校验补履约任务状态。
func validMakeupStatus(status string) bool {
	switch status {
	case "pending", "servicing", "completed", "cancelled":
		return true
	}
	return false
}

// validateMakeupPlan 校验补履约方案参数（§13.6）：补履约不产生退款金额，须选补钟或换人、
// 指定资质合格的承接陪玩、席位在订单范围内；补钟须给出大于零的补足服务量。
func validateMakeupPlan(ctx context.Context, db *ent.Client, order *ent.EsportsOrder, in afterSalePlanInput) error {
	if in.RefundCents != 0 {
		return fmt.Errorf("%w：补履约方案不产生退款金额，退款请改用「退原核销款」", errInvalidInput)
	}
	if !validMakeupKind(in.MakeupKind) {
		return fmt.Errorf("%w：补履约须选择「补钟」或「换人」", errInvalidInput)
	}
	if in.MakeupPlayerID < 1 {
		return fmt.Errorf("%w：补履约须指定承接陪玩", errInvalidInput)
	}
	if in.MakeupSeatIndex < 1 || in.MakeupSeatIndex > order.RequiredSeats {
		return fmt.Errorf("%w：补履约席位须在订单席位范围内", errInvalidInput)
	}
	if in.MakeupUnits < 0 || in.MakeupCompanyPayCents < 0 {
		return errInvalidInput
	}
	if in.MakeupKind == "makeup_time" && in.MakeupUnits < 1 {
		return fmt.Errorf("%w：补钟须给出大于零的补足服务量", errInvalidInput)
	}
	player, err := db.EsportsPlayer.Get(ctx, in.MakeupPlayerID)
	if err != nil {
		return err
	}
	if player.Qualification != esportsplayer.QualificationQualified {
		return fmt.Errorf("%w：承接陪玩未通过考核或已停用，不可承接补履约", errInvalidInput)
	}
	return nil
}

// createFulfillmentMakeupIn 事务内创建补履约任务：记录席位占位、承接陪玩、补足服务量与公司承担报酬。
// 幂等：一张售后单只允许一个任务，先查后建并靠 after_sale_id 唯一索引兜底，重复执行返回既有任务。
func createFulfillmentMakeupIn(ctx context.Context, tx *ent.Tx, item *ent.EsportsAfterSale,
	order *ent.EsportsOrder, adminID int, now time.Time) (*ent.EsportsFulfillmentMakeup, error) {
	if item.MakeupKind == esportsaftersale.MakeupKindNone ||
		item.MakeupPlayerID == nil || item.MakeupSeatIndex == nil ||
		*item.MakeupPlayerID < 1 || *item.MakeupSeatIndex < 1 {
		return nil, fmt.Errorf("%w：售后单缺少补履约参数，请重新提交方案", errInvalidInput)
	}
	existing, err := tx.EsportsFulfillmentMakeup.Query().Where(
		esportsfulfillmentmakeup.AfterSaleIDEQ(item.ID),
	).Only(ctx)
	if err == nil {
		return existing, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	task, err := tx.EsportsFulfillmentMakeup.Create().
		SetMakeupNo(makeupNo()).SetAfterSaleID(item.ID).SetOrderID(order.ID).
		SetKind(esportsfulfillmentmakeup.Kind(item.MakeupKind.String())).
		SetPlayerID(*item.MakeupPlayerID).SetSeatIndex(*item.MakeupSeatIndex).
		SetUnits(item.MakeupUnits).SetCompanyPayCents(item.MakeupCompanyPayCents).
		SetCreatedByAdminID(adminID).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 并发重复创建：返回既有任务，保证幂等。
			return tx.EsportsFulfillmentMakeup.Query().Where(
				esportsfulfillmentmakeup.AfterSaleIDEQ(item.ID)).Only(ctx)
		}
		return nil, err
	}
	return task, nil
}

// listFulfillmentMakeups 按状态列出补履约任务（§13.6）。
func listFulfillmentMakeups(ctx context.Context, db *ent.Client, status string, limit int) ([]*ent.EsportsFulfillmentMakeup, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	query := db.EsportsFulfillmentMakeup.Query()
	if status != "" {
		if !validMakeupStatus(status) {
			return nil, errInvalidInput
		}
		query.Where(esportsfulfillmentmakeup.StatusEQ(esportsfulfillmentmakeup.Status(status)))
	}
	return query.Order(ent.Desc(esportsfulfillmentmakeup.FieldID)).Limit(limit).All(ctx)
}

// getFulfillmentMakeup 读取单个补履约任务。
func getFulfillmentMakeup(ctx context.Context, db *ent.Client, id int) (*ent.EsportsFulfillmentMakeup, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsFulfillmentMakeup.Get(ctx, id)
}

// startFulfillmentMakeup 开始补履约服务：pending → servicing，记录服务段起点。
func startFulfillmentMakeup(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsFulfillmentMakeup, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsFulfillmentMakeup.Update().Where(
		esportsfulfillmentmakeup.IDEQ(id),
		esportsfulfillmentmakeup.StatusEQ(esportsfulfillmentmakeup.StatusPending),
	).SetStatus(esportsfulfillmentmakeup.StatusServicing).
		SetStartedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsFulfillmentMakeup.Get(ctx, id)
}

// completeFulfillmentMakeup 结束补履约服务并由客服验收：servicing → completed。
// 记录系统服务秒数与验收人；补履约不改原订单实收与成交额。
func completeFulfillmentMakeup(ctx context.Context, db *ent.Client, id, adminID int, note string, now time.Time) (*ent.EsportsFulfillmentMakeup, error) {
	note = strings.TrimSpace(note)
	if id < 1 || adminID < 1 || len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	task, err := db.EsportsFulfillmentMakeup.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Status != esportsfulfillmentmakeup.StatusServicing {
		return nil, errConflict
	}
	seconds := int64(0)
	if task.StartedAt != nil {
		if diff := int64(now.UTC().Sub(task.StartedAt.UTC()).Seconds()); diff > 0 {
			seconds = diff
		}
	}
	changed, err := db.EsportsFulfillmentMakeup.Update().Where(
		esportsfulfillmentmakeup.IDEQ(id),
		esportsfulfillmentmakeup.VersionEQ(task.Version),
		esportsfulfillmentmakeup.StatusEQ(esportsfulfillmentmakeup.StatusServicing),
	).SetStatus(esportsfulfillmentmakeup.StatusCompleted).
		SetFinishedAt(now.UTC()).SetSystemSeconds(seconds).
		SetAcceptedByAdminID(adminID).SetAcceptedAt(now.UTC()).
		SetNote(note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsFulfillmentMakeup.Get(ctx, id)
}
