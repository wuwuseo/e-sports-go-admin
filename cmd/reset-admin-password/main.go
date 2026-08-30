// reset-admin-password 一次性账号密码重置工具
// 用法: go run ./cmd/reset-admin-password -username 账号 -password 新密码
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"go-admin/internal"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/user"

	"github.com/wuwuseo/cmf/crypto"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	name := flag.String("username", "admin", "要重置的账号名")
	newPwd := flag.String("password", "", "新的密码（必填）")
	flag.Parse()
	if *newPwd == "" {
		fmt.Fprintln(os.Stderr, "用法: go run ./cmd/reset-admin-password -username 账号 -password 新密码")
		os.Exit(1)
	}

	app, err := internal.InitializeApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化失败:", err)
		os.Exit(1)
	}
	defer app.EntClient.Close()
	defer app.DB.Close()

	hashed, salt, err := crypto.HashPassword(*newPwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成密码哈希失败:", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// 管理员登录入口表（账号不在该表则更新 0 条，属正常）
	n, err := app.EntClient.Admin.Update().
		Where(admin.UsernameEQ(*name)).
		SetPassword(hashed).
		SetPasswordSalt(salt).
		Save(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "更新 admins 表失败:", err)
		os.Exit(1)
	}
	fmt.Printf("admins 表已更新 %d 条记录\n", n)

	// 用户表（普通账号登录入口）
	m, err := app.EntClient.User.Update().
		Where(user.UsernameEQ(*name)).
		SetPassword(hashed).
		SetPasswordSalt(salt).
		Save(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "更新 users 表失败:", err)
		os.Exit(1)
	}
	fmt.Printf("users 表已更新 %d 条记录\n", m)
	if n+m == 0 {
		fmt.Fprintf(os.Stderr, "警告: 账号 %q 不存在，未做任何更新\n", *name)
		os.Exit(1)
	}
	fmt.Printf("重置完成，请使用新密码登录 %q 账号\n", *name)
}
