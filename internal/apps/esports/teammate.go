package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsteammate"
)

// 陪玩固排：按游戏分区互加形成的固定队友关系，双方确认后建立，任一方可解除。
// 关系无方向，存为规范化对（player_id < teammate_player_id），仅以 initiator_player_id 记录发起方。
// 首版只落地陪玩端的关系管理；「组队派单时主陪玩从固排邀请队友 / 客服一次成队」另需接入派单侧。

// teammateView 是陪玩端固排 / 待确认邀请列表的一条，teammate_* 字段恒指向「对方」陪玩。
type teammateView struct {
	ID               int       `json:"id"`
	TeammatePlayerID int       `json:"teammate_player_id"`
	TeammateName     string    `json:"teammate_name"`
	TeammateLevel    string    `json:"teammate_level"`
	TeammatePresence string    `json:"teammate_presence"`
	Game             string    `json:"game"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

// requestTeammate 陪玩发起固排邀请。幂等靠 idem_key 唯一索引兜底，重复提交返回既有记录；
// 同一对陪玩在同一游戏分区只允许一条「待确认 / 已建立」关系，重复发起返回 errConflict。
func requestTeammate(ctx context.Context, db *ent.Client, initiatorID, teammateID int,
	game, idemKey string, now time.Time) (*ent.EsportsTeammate, error) {
	game = strings.TrimSpace(game)
	idemKey = strings.TrimSpace(idemKey)
	if initiatorID < 1 || teammateID < 1 || initiatorID == teammateID ||
		game == "" || len([]rune(game)) > 64 || len(idemKey) < 8 || len(idemKey) > 128 {
		return nil, errInvalidInput
	}
	lo, hi := sortPair(initiatorID, teammateID)
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.EsportsPlayer.Get(ctx, teammateID); err != nil {
		return nil, err
	}
	// 幂等重放：同一幂等键重复提交返回既有邀请。
	if replayed, err := tx.EsportsTeammate.Query().Where(
		esportsteammate.IdemKeyEQ(idemKey),
	).Only(ctx); err == nil {
		return replayed, nil
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	duplicated, err := tx.EsportsTeammate.Query().Where(
		esportsteammate.PlayerIDEQ(lo),
		esportsteammate.TeammatePlayerIDEQ(hi),
		esportsteammate.GameEQ(game),
		esportsteammate.StatusIn(esportsteammate.StatusPending, esportsteammate.StatusConfirmed),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, fmt.Errorf("%w：该游戏分区下你们已是固排或已有待确认的邀请", errConflict)
	}
	relation, err := tx.EsportsTeammate.Create().SetPlayerID(lo).SetTeammatePlayerID(hi).
		SetInitiatorPlayerID(initiatorID).SetGame(game).SetStatus(esportsteammate.StatusPending).
		SetIdemKey(idemKey).Save(ctx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return relation, nil
}

// listConfirmedTeammates 返回我的固排列表（status=confirmed）。
func listConfirmedTeammates(ctx context.Context, db *ent.Client, playerID int) ([]teammateView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	records, err := db.EsportsTeammate.Query().Where(
		esportsteammate.StatusEQ(esportsteammate.StatusConfirmed),
		esportsteammate.Or(
			esportsteammate.PlayerIDEQ(playerID),
			esportsteammate.TeammatePlayerIDEQ(playerID),
		),
	).Order(ent.Desc(esportsteammate.FieldConfirmedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	return buildTeammateViews(ctx, db, records, playerID)
}

// listPendingTeammateRequests 返回「待我确认」的固排邀请（我是被邀请方）。
func listPendingTeammateRequests(ctx context.Context, db *ent.Client, playerID int) ([]teammateView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	records, err := db.EsportsTeammate.Query().Where(
		esportsteammate.StatusEQ(esportsteammate.StatusPending),
		esportsteammate.InitiatorPlayerIDNEQ(playerID),
		esportsteammate.Or(
			esportsteammate.PlayerIDEQ(playerID),
			esportsteammate.TeammatePlayerIDEQ(playerID),
		),
	).Order(ent.Asc(esportsteammate.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	return buildTeammateViews(ctx, db, records, playerID)
}

// acceptTeammateRequest 被邀请方确认固排：pending → confirmed。
func acceptTeammateRequest(ctx context.Context, db *ent.Client, playerID, relationID int, now time.Time) (*ent.EsportsTeammate, error) {
	if playerID < 1 || relationID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	relation, err := tx.EsportsTeammate.Get(ctx, relationID)
	if err != nil {
		return nil, err
	}
	if relation.Status != esportsteammate.StatusPending || relation.InitiatorPlayerID == playerID {
		return nil, errForbidden
	}
	if relation.PlayerID != playerID && relation.TeammatePlayerID != playerID {
		return nil, errForbidden
	}
	changed, err := tx.EsportsTeammate.Update().Where(
		esportsteammate.IDEQ(relationID),
		esportsteammate.StatusEQ(esportsteammate.StatusPending),
		esportsteammate.VersionEQ(relation.Version),
	).SetStatus(esportsteammate.StatusConfirmed).SetConfirmedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsTeammate.Get(ctx, relationID)
}

// declineTeammateRequest 被邀请方拒绝固排：pending → dissolved。
func declineTeammateRequest(ctx context.Context, db *ent.Client, playerID, relationID int, now time.Time) (*ent.EsportsTeammate, error) {
	if playerID < 1 || relationID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	relation, err := tx.EsportsTeammate.Get(ctx, relationID)
	if err != nil {
		return nil, err
	}
	if relation.Status != esportsteammate.StatusPending || relation.InitiatorPlayerID == playerID {
		return nil, errForbidden
	}
	if relation.PlayerID != playerID && relation.TeammatePlayerID != playerID {
		return nil, errForbidden
	}
	changed, err := tx.EsportsTeammate.Update().Where(
		esportsteammate.IDEQ(relationID),
		esportsteammate.StatusEQ(esportsteammate.StatusPending),
		esportsteammate.VersionEQ(relation.Version),
	).SetStatus(esportsteammate.StatusDissolved).
		SetDissolvedByPlayerID(playerID).SetDissolvedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsTeammate.Get(ctx, relationID)
}

// dissolveTeammate 任一方解除已建立的固排：confirmed → dissolved。
func dissolveTeammate(ctx context.Context, db *ent.Client, playerID, relationID int, now time.Time) (*ent.EsportsTeammate, error) {
	if playerID < 1 || relationID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	relation, err := tx.EsportsTeammate.Get(ctx, relationID)
	if err != nil {
		return nil, err
	}
	if relation.Status != esportsteammate.StatusConfirmed {
		return nil, errConflict
	}
	if relation.PlayerID != playerID && relation.TeammatePlayerID != playerID {
		return nil, errForbidden
	}
	changed, err := tx.EsportsTeammate.Update().Where(
		esportsteammate.IDEQ(relationID),
		esportsteammate.StatusEQ(esportsteammate.StatusConfirmed),
		esportsteammate.VersionEQ(relation.Version),
	).SetStatus(esportsteammate.StatusDissolved).
		SetDissolvedByPlayerID(playerID).SetDissolvedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsTeammate.Get(ctx, relationID)
}

// searchTeammateCandidates 按昵称搜索可添加为固排的陪玩（排除自己），供添加时选人。
func searchTeammateCandidates(ctx context.Context, db *ent.Client, keyword string, excludeID, limit int) ([]*ent.EsportsPlayer, error) {
	if limit < 1 || limit > 50 {
		limit = 20
	}
	q := db.EsportsPlayer.Query()
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q.Where(esportsplayer.DisplayNameContainsFold(keyword))
	}
	if excludeID > 0 {
		q.Where(esportsplayer.IDNEQ(excludeID))
	}
	return q.Order(ent.Asc(esportsplayer.FieldDisplayName)).Limit(limit).All(ctx)
}

// buildTeammateViews 把一批关系记录转成陪玩端视图，teammate_* 恒指向「对方」。
func buildTeammateViews(ctx context.Context, db *ent.Client, records []*ent.EsportsTeammate, me int) ([]teammateView, error) {
	if len(records) == 0 {
		return []teammateView{}, nil
	}
	ids := make([]int, 0, len(records))
	index := make(map[int]*ent.EsportsTeammate, len(records))
	for _, r := range records {
		other := r.TeammatePlayerID
		if other == me {
			other = r.PlayerID
		}
		ids = append(ids, other)
		index[r.ID] = r
	}
	players, err := db.EsportsPlayer.Query().Where(esportsplayer.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]*ent.EsportsPlayer, len(players))
	for _, p := range players {
		byID[p.ID] = p
	}
	views := make([]teammateView, 0, len(records))
	for _, r := range records {
		otherID := r.TeammatePlayerID
		if otherID == me {
			otherID = r.PlayerID
		}
		p, ok := byID[otherID]
		if !ok {
			continue
		}
		views = append(views, teammateView{
			ID:               r.ID,
			TeammatePlayerID: otherID,
			TeammateName:     p.DisplayName,
			TeammateLevel:    string(p.Level),
			TeammatePresence: string(p.Presence),
			Game:             r.Game,
			Status:           string(r.Status),
			CreatedAt:        r.CreatedAt,
		})
	}
	return views, nil
}

func sortPair(a, b int) (int, int) {
	if a <= b {
		return a, b
	}
	return b, a
}
