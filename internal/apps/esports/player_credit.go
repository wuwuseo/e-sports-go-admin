package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayercreditevent"
)

// 信用复核动作：确认责任并暂停接单 / 判定不构成责任。
const (
	creditReviewPenalty = "penalty"
	creditReviewClear   = "clear"
)

type playerCreditReviewInput struct {
	Action string `json:"action"`
	Note   string `json:"note"`
}

func playerCreditEventNo() string { return "PC" + uuid.NewString() }

// recordPlayerFaultCredit 售后执行完成后按责任方自动记载陪玩信用（§13.5）：
// 仅 fault_party=player 且能定位到陪玩时记载，同一售后单只记载一次（幂等）。
// 记载本身只增不改接单资格，是否处罚由复核入口决定，避免自动处罚误伤。
func recordPlayerFaultCredit(ctx context.Context, tx *ent.Tx, afterSaleID int) error {
	item, err := tx.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return err
	}
	if item.FaultParty != esportsaftersale.FaultPartyPlayer {
		return nil
	}
	playerID := 0
	if item.RecoveryPlayerID != nil {
		playerID = *item.RecoveryPlayerID
	} else if item.PlayerID != nil {
		playerID = *item.PlayerID
	}
	if playerID < 1 {
		return nil
	}
	exists, err := tx.EsportsPlayerCreditEvent.Query().Where(
		esportsplayercreditevent.AfterSaleIDEQ(afterSaleID),
		esportsplayercreditevent.KindEQ(esportsplayercreditevent.KindFaultAdmitted),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	event, err := tx.EsportsPlayerCreditEvent.Create().
		SetEventNo(playerCreditEventNo()).
		SetPlayerID(playerID).SetOrderID(item.SourceID).SetAfterSaleID(afterSaleID).
		SetKind(esportsplayercreditevent.KindFaultAdmitted).
		SetAmountCents(item.RecoveryCents).
		SetNote("售后 " + item.AfterSaleNo + " 责任方判定为陪玩").
		SetStatus(esportsplayercreditevent.StatusRecorded).Save(ctx)
	if err != nil {
		return err
	}
	_, err = tx.EsportsOutbox.Create().
		SetEventKey("player.credit_recorded:" + event.EventNo).
		SetEventType("player.credit_recorded").
		SetAggregateRef(strconv.Itoa(playerID)).
		SetPayload(map[string]interface{}{
			"event_no": event.EventNo, "player_id": playerID,
			"after_sale_no": item.AfterSaleNo, "amount_cents": item.RecoveryCents,
		}).Save(ctx)
	return err
}

// reviewPlayerCreditEvent 复核陪玩信用记载（§13.5）：
// penalty 确认责任并把接单资格置为 suspended；clear 判定不构成责任只清记录。
// 恢复接单资格走既有的陪玩档案编辑入口，不在此处自动恢复。
func reviewPlayerCreditEvent(ctx context.Context, db *ent.Client, eventID, adminID int,
	in playerCreditReviewInput, now time.Time) (*ent.EsportsPlayerCreditEvent, error) {
	in.Action = strings.TrimSpace(in.Action)
	in.Note = strings.TrimSpace(in.Note)
	if eventID < 1 || adminID < 1 || len([]rune(in.Note)) > 255 ||
		(in.Action != creditReviewPenalty && in.Action != creditReviewClear) ||
		(in.Action == creditReviewPenalty && in.Note == "") {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	event, err := tx.EsportsPlayerCreditEvent.Get(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if event.Status != esportsplayercreditevent.StatusRecorded {
		return nil, errConflict
	}
	status := esportsplayercreditevent.StatusCleared
	if in.Action == creditReviewPenalty {
		status = esportsplayercreditevent.StatusPenaltyApplied
	}
	changed, err := tx.EsportsPlayerCreditEvent.Update().Where(
		esportsplayercreditevent.IDEQ(eventID),
		esportsplayercreditevent.StatusEQ(esportsplayercreditevent.StatusRecorded),
		esportsplayercreditevent.VersionEQ(event.Version),
	).SetStatus(status).SetReviewedByAdminID(adminID).SetReviewedAt(now.UTC()).
		SetReviewNote(in.Note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if status == esportsplayercreditevent.StatusPenaltyApplied {
		player, err := tx.EsportsPlayer.Get(ctx, event.PlayerID)
		if err != nil {
			return nil, err
		}
		if player.Qualification != esportsplayer.QualificationSuspended {
			if _, err = tx.EsportsPlayer.Update().Where(
				esportsplayer.IDEQ(event.PlayerID), esportsplayer.VersionEQ(player.Version),
			).SetQualification(esportsplayer.QualificationSuspended).AddVersion(1).Save(ctx); err != nil {
				return nil, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayerCreditEvent.Get(ctx, eventID)
}

// listPlayerCreditEvents 查询陪玩信用记载（§13.5）。playerID 为 0 表示不按陪玩过滤，
// status 为空表示不按状态过滤。
func listPlayerCreditEvents(ctx context.Context, db *ent.Client, playerID int, status string,
	limit int) ([]*ent.EsportsPlayerCreditEvent, error) {
	if playerID < 0 {
		return nil, errInvalidInput
	}
	status = strings.TrimSpace(status)
	switch status {
	case "", esportsplayercreditevent.StatusRecorded.String(),
		esportsplayercreditevent.StatusPenaltyApplied.String(),
		esportsplayercreditevent.StatusCleared.String():
	default:
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := db.EsportsPlayerCreditEvent.Query()
	if playerID > 0 {
		query = query.Where(esportsplayercreditevent.PlayerIDEQ(playerID))
	}
	if status != "" {
		query = query.Where(esportsplayercreditevent.StatusEQ(esportsplayercreditevent.Status(status)))
	}
	return query.Order(ent.Desc(esportsplayercreditevent.FieldID)).Limit(limit).All(ctx)
}
