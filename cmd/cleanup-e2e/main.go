// ponytail: 一次性清理脚本，删除 UI-E2E-* 测试用户
// 执行: go run ./cmd/cleanup-e2e
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	_ "github.com/go-sql-driver/mysql"

	"go-admin/internal"
	appent "go-admin/internal/ent"
	"go-admin/internal/ent/blacklist"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/message"
	"go-admin/internal/ent/messagepreference"
	"go-admin/internal/ent/oauthsession"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
)

const targetPrefix = "UI-E2E-"

func main() {
	app, err := internal.InitializeApp()
	if err != nil {
		exitError("初始化应用失败", err)
	}
	if app.EntClient != nil {
		defer app.EntClient.Close()
	}
	if app.DB != nil {
		defer app.DB.Close()
	}
	ctx := context.Background()
	client := app.EntClient

	matches, err := client.User.Query().
		Where(user.Or(
			user.UsernameHasPrefix(targetPrefix),
			user.EmailContains(strings.ToLower(targetPrefix)),
			user.NameHasPrefix(targetPrefix),
		)).
		All(ctx)
	if err != nil {
		exitError("查询测试用户失败", err)
	}
	if len(matches) == 0 {
		fmt.Println("未找到匹配", targetPrefix, "* 的用户，无需清理。")
		reportSummary(client, ctx)
		os.Exit(0)
	}

	fmt.Printf("找到 %d 个待清理用户（前缀 %s）：\n", len(matches), targetPrefix)
	var ids []int
	var uids []string
	for _, u := range matches {
		fmt.Printf("  - ID=%d  username=%q  email=%q  uid=%q  status=%s\n",
			u.ID, u.Username, u.Email, u.UID, u.Status)
		ids = append(ids, u.ID)
		if u.UID != "" {
			uids = append(uids, u.UID)
		}
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		exitError("开启事务失败", err)
	}
	rollback := func(m string, e error) { _ = tx.Rollback(); exitError(m, e) }

	delN := func(name string, n int, err error) int {
		if err != nil {
			rollback("删除 "+name+" 失败", err)
		}
		if n > 0 {
			fmt.Printf("  [关联表] %-18s 删除 %d 行\n", name, n)
		}
		return n
	}
	total := 0

	n, err := tx.UserRole.Delete().Where(userrole.UserIDIn(ids...)).Exec(ctx)
	total += delN("UserRole", n, err)
	n, err = tx.Blacklist.Delete().Where(blacklist.UserIDIn(ids...)).Exec(ctx)
	total += delN("Blacklist", n, err)
	n, err = tx.ThirdPartyUser.Delete().Where(thirdpartyuser.UserIDIn(ids...)).Exec(ctx)
	total += delN("ThirdPartyUser", n, err)
	if len(uids) > 0 {
		n, err = tx.Message.Delete().Where(message.UserUIDIn(uids...)).Exec(ctx)
		total += delN("Message", n, err)
		n, err = tx.MessagePreference.Delete().Where(messagepreference.UserUIDIn(uids...)).Exec(ctx)
		total += delN("MessagePreference", n, err)
		n, err = tx.OAuthSession.Delete().Where(oauthsession.Or(
			oauthsession.SubjectUIDIn(uids...),
			oauthsession.LoginSubjectUIDIn(uids...),
		)).Exec(ctx)
		total += delN("OAuthSession", n, err)
	}
	// CasbinRule: 每个 uid 单独匹配 PtypeEQ("g") AND V0EQ(uid)，避免复杂 predicate 组合
	cbDel := 0
	for _, uid := range uids {
		n, e := tx.CasbinRule.Delete().Where(casbinrule.PtypeEQ("g"), casbinrule.V0EQ(uid)).Exec(ctx)
		if e != nil {
			rollback("删除 CasbinRule 失败 uid="+uid, e)
		}
		cbDel += n
	}
	if cbDel > 0 {
		total += cbDel
		fmt.Printf("  [关联表] %-18s 删除 %d 行\n", "CasbinRule(g)", cbDel)
	}
	userDel, err := tx.User.Delete().Where(user.IDIn(ids...)).Exec(ctx)
	if err != nil {
		rollback("删除 User 主体失败", err)
	}
	fmt.Printf("  [主表]   %-18s 删除 %d 行\n", "User", userDel)

	if err := tx.Commit(); err != nil {
		exitError("提交事务失败", err)
	}
	fmt.Printf("\n清理完成：关联表 %d 行 + 用户主体 %d 行 = 合计 %d 行。\n", total, userDel, total+userDel)

	reportSummary(client, ctx)
}

func reportSummary(client *appent.Client, ctx context.Context) {
	total, err := client.User.Query().Count(ctx)
	if err != nil {
		fmt.Println("统计用户总数失败:", err)
		return
	}
	fmt.Printf("\n当前数据库用户总数: %d\n", total)
	list, _ := client.User.Query().
		Select(user.FieldID, user.FieldUsername, user.FieldEmail, user.FieldStatus).
		All(ctx)
	for _, u := range list {
		fmt.Printf("  - ID=%d  username=%-20s  email=%-30s  status=%s\n",
			u.ID, u.Username, u.Email, u.Status)
	}
}

func exitError(msg string, err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "[FATAL]", msg, "::", err)
	} else {
		fmt.Fprintln(os.Stderr, "[FATAL]", msg)
	}
	os.Exit(1)
}
