package service

import (
	"testing"

	"go-admin/internal/ent/user"
	"go-admin/internal/global"
)

// users.phone 带唯一索引（§6.1-01，列可空）。唯一索引**允许多个 NULL、但不允许多个空串**，
// 因此「未填手机号」的注册必须落 NULL，而不是写成空串——否则第二个这样的用户就建不出来
// （唯一索引会把两个空串判为重复，注册直接报约束冲突）。
//
// 本测试锁定该不变量：它是 phone 唯一索引能安全上线的**运行时前提**，不是可选的清理动作。
// 同期还有两处配套改动：provider.go 在迁移前把存量空串归一化为 NULL（否则索引建不上、服务起不来），
// 以及 UpdateManagedUser 对空串走 ClearPhone。
func TestRegisterWithoutPhoneLeavesColumnNullSoSecondUserCanRegister(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	usernames := []string{"nophone1", "nophone2"}
	for i, username := range usernames {
		req := &RegisterRequest{
			Email:    username + "@example.com",
			Password: "Tt!12345",
			Username: username,
			Nickname: "无手机号用户",
			// Phone 刻意留空：应落 NULL，不得写成空串。
		}
		if err := svc.Register(ctx, req); err != nil {
			t.Fatalf("第 %d 个未填手机号的用户注册失败（唯一索引不允许出现第二个空串）: %v", i+1, err)
		}
	}

	if n := global.ORM.User.Query().Where(user.UsernameIn(usernames...)).CountX(ctx); n != 2 {
		t.Fatalf("应创建 2 个用户，实际 %d 个", n)
	}
	if n := global.ORM.User.Query().
		Where(user.UsernameIn(usernames...), user.PhoneEQ("")).CountX(ctx); n != 0 {
		t.Fatalf("未填手机号的行不得写成空串（会撞唯一索引），实际有 %d 行为空串", n)
	}
	if n := global.ORM.User.Query().
		Where(user.UsernameIn(usernames...), user.PhoneIsNil()).CountX(ctx); n != 2 {
		t.Fatalf("未填手机号的行应落 NULL，实际只有 %d 行为 NULL", n)
	}
}
