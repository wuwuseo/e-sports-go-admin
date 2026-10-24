package esports

import (
	"encoding/json"
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/user"
)

// seedSelfServicePlayer 造一个已绑定登录账号的陪玩档案（手机号预留在账号上）。
func seedSelfServicePlayer(t *testing.T, db *ent.Client, uid, phone string) (*ent.User, *ent.EsportsPlayer) {
	t.Helper()
	ctx := t.Context()
	account := db.User.Create().SetEmail(uid + "@example.com").SetUID(uid).
		SetStatus(user.StatusActive).SetPhone(phone).SaveX(ctx)
	player := seedPlayer(t, db, account.ID, esportsplayer.QualificationPending, esportsplayer.PresenceOffline)
	return account, player
}

// 陪玩自助改昵称 / 手机号 / 擅长游戏：落库生效、版本递增，账号手机号同步。
func TestPlayerSelfServiceUpdateProfile(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_self_update")
	defer db.Close()
	ctx := t.Context()
	account, player := seedSelfServicePlayer(t, db, "self-update", "13900000000")

	phone := "13800138000"
	games := []string{" 王者荣耀 ", "英雄联盟", "王者荣耀", ""}
	updated, err := updatePlayerSelfProfile(ctx, db, player.ID, playerSelfProfileInput{
		DisplayName: "  小鹿  ", Phone: &phone, Games: &games, Version: player.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "小鹿" || updated.Version != player.Version+1 {
		t.Fatalf("昵称/版本未按预期更新：%+v", updated)
	}
	if len(updated.Games) != 2 || updated.Games[0] != "王者荣耀" || updated.Games[1] != "英雄联盟" {
		t.Fatalf("擅长游戏未归一化：%+v", updated.Games)
	}
	if updated.Qualification != esportsplayer.QualificationPending ||
		updated.Presence != esportsplayer.PresenceOffline {
		t.Fatalf("资格 / 在线状态被意外修改：%+v", updated)
	}
	saved := db.User.GetX(ctx, account.ID)
	if saved.Phone != "13800138000" {
		t.Fatalf("账号手机号未更新：%s", saved.Phone)
	}

	view, err := playerSelfProfile(ctx, db, updated)
	if err != nil {
		t.Fatal(err)
	}
	if view.Phone != "13800138000" || view.DisplayName != "小鹿" || view.Version != updated.Version {
		t.Fatalf("自助资料视图不正确：%+v", view)
	}
}

// 只改昵称时不传 phone / games：账号手机号与擅长游戏保持不变。
func TestPlayerSelfServicePartialUpdateKeepsUntouchedFields(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_self_partial")
	defer db.Close()
	ctx := t.Context()
	account, player := seedSelfServicePlayer(t, db, "self-partial", "13700000000")
	originalGames := []string{"永劫无间"}
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames(originalGames).SaveX(ctx)

	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, playerSelfProfileInput{DisplayName: "阿飞"}); err != nil {
		t.Fatal(err)
	}
	saved := db.EsportsPlayer.GetX(ctx, player.ID)
	if len(saved.Games) != 1 || saved.Games[0] != "永劫无间" {
		t.Fatalf("未提交擅长游戏却被清空：%+v", saved.Games)
	}
	if got := db.User.GetX(ctx, account.ID).Phone; got != "13700000000" {
		t.Fatalf("未提交手机号却被修改：%s", got)
	}
}

// 非法手机号 / 空昵称被拒，且不产生任何写入（校验先于事务）。
func TestPlayerSelfServiceRejectsInvalidInput(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_self_invalid")
	defer db.Close()
	ctx := t.Context()
	account, player := seedSelfServicePlayer(t, db, "self-invalid", "13600000000")

	for _, bad := range []string{"12345", "23600000000", "1360000000a", "136000000001"} {
		phone := bad
		_, err := updatePlayerSelfProfile(ctx, db, player.ID, playerSelfProfileInput{
			DisplayName: "合法昵称", Phone: &phone,
		})
		if !errors.Is(err, errInvalidInput) {
			t.Fatalf("非法手机号 %q 未被拦截：%v", bad, err)
		}
	}
	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, playerSelfProfileInput{
		DisplayName: "   ",
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空昵称未被拦截：%v", err)
	}
	tooLong := make([]rune, playerSelfNameMaxRunes+1)
	for i := range tooLong {
		tooLong[i] = '昵'
	}
	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, playerSelfProfileInput{
		DisplayName: string(tooLong),
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超长昵称未被拦截：%v", err)
	}
	saved := db.EsportsPlayer.GetX(ctx, player.ID)
	if saved.DisplayName != "陪玩" || saved.Version != player.Version {
		t.Fatalf("校验失败仍写入：%+v", saved)
	}
	if got := db.User.GetX(ctx, account.ID).Phone; got != "13600000000" {
		t.Fatalf("校验失败仍改手机号：%s", got)
	}
}

// 资格 / 等级 / 在线状态 / 接单上限 / 归属账号不在输入结构内，请求体里塞了也被忽略。
func TestPlayerSelfServiceIgnoresPrivilegedFields(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_self_privileged")
	defer db.Close()
	ctx := t.Context()
	account, player := seedSelfServicePlayer(t, db, "self-privileged", "13500000000")

	body := []byte(`{"display_name":"改名","qualification":"qualified","level":"mentor",
		"presence":"online","daily_limit":99,"concurrent_limit":99,"user_id":99999}`)
	var in playerSelfProfileInput
	if err := json.Unmarshal(body, &in); err != nil {
		t.Fatal(err)
	}
	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, in); err != nil {
		t.Fatal(err)
	}
	saved := db.EsportsPlayer.GetX(ctx, player.ID)
	if saved.Qualification != esportsplayer.QualificationPending ||
		saved.Level != "novice" || saved.Presence != esportsplayer.PresenceOffline ||
		saved.DailyLimit != 0 || saved.ConcurrentLimit != 0 || saved.UserID != account.ID {
		t.Fatalf("特权字段被越权修改：%+v", saved)
	}
	if saved.DisplayName != "改名" {
		t.Fatalf("允许字段未生效：%+v", saved)
	}
}

// 未绑定陪玩档案（playerID 缺失 / 不存在）一律拒绝。
func TestPlayerSelfServiceRejectsUnboundPlayer(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_self_unbound")
	defer db.Close()
	ctx := t.Context()

	for _, id := range []int{0, -1, 99999} {
		if _, err := updatePlayerSelfProfile(ctx, db, id, playerSelfProfileInput{
			DisplayName: "无档案",
		}); !errors.Is(err, errForbidden) {
			t.Fatalf("未绑定陪玩 %d 未被拒绝：%v", id, err)
		}
	}
}

// 带上过期版本号时按乐观锁返回 errConflict，不覆盖他人写入。
func TestPlayerSelfServiceVersionConflict(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_self_conflict")
	defer db.Close()
	ctx := t.Context()
	_, player := seedSelfServicePlayer(t, db, "self-conflict", "13400000000")

	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, playerSelfProfileInput{
		DisplayName: "第一次", Version: player.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := updatePlayerSelfProfile(ctx, db, player.ID, playerSelfProfileInput{
		DisplayName: "过期写入", Version: player.Version,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("过期版本号未被拦截：%v", err)
	}
	if got := db.EsportsPlayer.GetX(ctx, player.ID).DisplayName; got != "第一次" {
		t.Fatalf("过期写入覆盖了最新数据：%s", got)
	}
}
