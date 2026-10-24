package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsteammate"
	"go-admin/internal/ent/migrate"
)

func seedTeammatePlayer(t *testing.T, db *ent.Client, userID int, name string) *ent.EsportsPlayer {
	t.Helper()
	return db.EsportsPlayer.Create().SetUserID(userID).SetDisplayName(name).
		SetQualification(esportsplayer.QualificationQualified).
		SetPresence(esportsplayer.PresenceOffline).SaveX(t.Context())
}

// 固排：发起 → 确认 → 解除的完整闭环，以及幂等、重复、越权拦截。
func TestTeammateRequestAcceptDissolve(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_teammate?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	a := seedTeammatePlayer(t, db, 1001, "陪玩甲")
	b := seedTeammatePlayer(t, db, 1002, "陪玩乙")

	// 自己加自己：拦截。
	if _, err := requestTeammate(ctx, db, a.ID, a.ID, "王者", "self-key-123", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("加自己未拦截：%v", err)
	}

	// A 发起对 B 的固排邀请。
	rel, err := requestTeammate(ctx, db, a.ID, b.ID, "王者", "tm-req-0001", now)
	if err != nil {
		t.Fatalf("发起固排失败：%v", err)
	}
	if rel.Status != esportsteammate.StatusPending || rel.InitiatorPlayerID != a.ID ||
		rel.PlayerID != a.ID || rel.TeammatePlayerID != b.ID {
		t.Fatalf("固排关系不正确：%+v", rel)
	}

	// 幂等重放：同一幂等键返回既有记录。
	replayed, err := requestTeammate(ctx, db, a.ID, b.ID, "王者", "tm-req-0001", now)
	if err != nil || replayed.ID != rel.ID {
		t.Fatalf("幂等重放失败：%v %+v", err, replayed)
	}

	// 重复发起：不同幂等键应冲突。
	if _, err := requestTeammate(ctx, db, a.ID, b.ID, "王者", "tm-req-0002", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复发起未拦截：%v", err)
	}

	// 待我确认列表：B 能看到，A（发起方）看不到。
	pendingB, err := listPendingTeammateRequests(ctx, db, b.ID)
	if err != nil || len(pendingB) != 1 || pendingB[0].TeammatePlayerID != a.ID || pendingB[0].TeammateName != "陪玩甲" {
		t.Fatalf("B 的待确认列表不正确：%v %+v", err, pendingB)
	}
	pendingA, err := listPendingTeammateRequests(ctx, db, a.ID)
	if err != nil || len(pendingA) != 0 {
		t.Fatalf("A 不应有待确认邀请：%v %+v", err, pendingA)
	}

	// 发起方 A 不能确认自己的邀请。
	if _, err := acceptTeammateRequest(ctx, db, a.ID, rel.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("发起方确认未拦截：%v", err)
	}

	// B 确认 → confirmed。
	if _, err := acceptTeammateRequest(ctx, db, b.ID, rel.ID, now); err != nil {
		t.Fatalf("确认固排失败：%v", err)
	}
	confirmedA, err := listConfirmedTeammates(ctx, db, a.ID)
	if err != nil || len(confirmedA) != 1 || confirmedA[0].TeammatePlayerID != b.ID || confirmedA[0].TeammateName != "陪玩乙" {
		t.Fatalf("A 的固排列表不正确：%v %+v", err, confirmedA)
	}
	confirmedB, err := listConfirmedTeammates(ctx, db, b.ID)
	if err != nil || len(confirmedB) != 1 || confirmedB[0].TeammatePlayerID != a.ID {
		t.Fatalf("B 的固排列表不正确：%v %+v", err, confirmedB)
	}

	// 任一方解除 → dissolved。
	if _, err := dissolveTeammate(ctx, db, a.ID, rel.ID, now); err != nil {
		t.Fatalf("解除固排失败：%v", err)
	}
	after, err := listConfirmedTeammates(ctx, db, b.ID)
	if err != nil || len(after) != 0 {
		t.Fatalf("解除后仍有关联：%v %+v", err, after)
	}

	// 解除后可重新发起（新幂等键）。
	rel2, err := requestTeammate(ctx, db, b.ID, a.ID, "王者", "tm-req-0003", now)
	if err != nil || rel2.Status != esportsteammate.StatusPending || rel2.InitiatorPlayerID != b.ID {
		t.Fatalf("解除后重新发起失败：%v %+v", err, rel2)
	}
	// 换游戏分区不冲突。
	if _, err := requestTeammate(ctx, db, a.ID, b.ID, "和平精英", "tm-req-0004", now); err != nil {
		t.Fatalf("换分区发起失败：%v", err)
	}
}

// 拒绝邀请后关系为 dissolved，且被邀请方确认/拒绝越权被拦截。
func TestTeammateDecline(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_teammate_decline?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	a := seedTeammatePlayer(t, db, 2001, "陪玩甲")
	b := seedTeammatePlayer(t, db, 2002, "陪玩乙")

	rel, err := requestTeammate(ctx, db, a.ID, b.ID, "王者", "tm-dec-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := declineTeammateRequest(ctx, db, b.ID, rel.ID, now); err != nil {
		t.Fatalf("拒绝固排失败：%v", err)
	}
	got, err := db.EsportsTeammate.Get(ctx, rel.ID)
	if err != nil || got.Status != esportsteammate.StatusDissolved || got.DissolvedByPlayerID == nil || *got.DissolvedByPlayerID != b.ID {
		t.Fatalf("拒绝后状态不正确：%v %+v", err, got)
	}
	// 无关陪玩不能确认 / 拒绝。
	c := seedTeammatePlayer(t, db, 2003, "陪玩丙")
	if _, err := acceptTeammateRequest(ctx, db, c.ID, rel.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("无关陪玩确认未拦截：%v", err)
	}
	if _, err := declineTeammateRequest(ctx, db, c.ID, rel.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("无关陪玩拒绝未拦截：%v", err)
	}
}

// 选人搜索按昵称模糊匹配并排除自己。
func TestTeammateSearchCandidates(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_teammate_search?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	a := seedTeammatePlayer(t, db, 3001, "王者玩家甲")
	seedTeammatePlayer(t, db, 3002, "王者玩家乙")
	seedTeammatePlayer(t, db, 3003, "和平玩家丙")

	got, err := searchTeammateCandidates(ctx, db, "王者玩家", a.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DisplayName != "王者玩家乙" {
		t.Fatalf("搜索结果不正确：%+v", got)
	}
}
