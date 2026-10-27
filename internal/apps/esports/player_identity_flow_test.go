package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

// TestPlayerIdentitySecondRejectNoUniqueConflict 复现实名认证「驳回→重提→再驳回」：
// (player_id,status) 唯一索引存在时，第二次驳回不得因旧 rejected 记录冲突。
func TestPlayerIdentitySecondRejectNoUniqueConflict(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:player_identity_second_reject?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	player := db.EsportsPlayer.Create().SetUserID(9001).SetDisplayName("实名测试陪玩").SaveX(ctx)
	admin := &ent.Admin{ID: 1}
	in := playerIdentitySubmitInput{RealName: "张三", IDNumber: "110101199001011234"}

	if _, err := submitPlayerIdentity(ctx, db, player, in); err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	if _, err := reviewPlayerIdentity(ctx, db, admin, player.ID,
		playerIdentityReviewInput{Approved: false, RejectReason: "照片不清晰"}, time.Now()); err != nil {
		t.Fatalf("首次驳回失败: %v", err)
	}
	if _, err := submitPlayerIdentity(ctx, db, player, in); err != nil {
		t.Fatalf("驳回后重提失败: %v", err)
	}
	if _, err := reviewPlayerIdentity(ctx, db, admin, player.ID,
		playerIdentityReviewInput{Approved: false, RejectReason: "证件过期"}, time.Now()); err != nil {
		t.Fatalf("第二次驳回不应触发唯一约束冲突: %v", err)
	}
	record, err := latestPlayerIdentity(ctx, db, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || record.Status != "rejected" || record.RejectReason != "证件过期" {
		t.Fatalf("最终实名记录 = %+v", record)
	}
	// 统一实名档案一主体一条：驳回重提只原地回写，不新增行。
	count, err := db.EsportsRealNameVerification.Query().Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("实名档案行数 %d，期望 1", count)
	}
}
