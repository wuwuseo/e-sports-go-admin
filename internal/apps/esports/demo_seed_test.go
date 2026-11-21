package esports

import (
	"testing"

	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidentitybinding"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportswalletaccount"
	"go-admin/internal/ent/role"
)

// 演示数据种子：一次铺「1 个商品 + 1 个客户 + 5 个陪玩」，重复执行不新增、
// 也不覆盖已存在记录上的运营改动。
func TestSeedDemoDataIdempotent(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_demo_seed")
	defer db.Close()
	ctx := t.Context()

	// 商品要挂在三角洲分区下，分区种子必须先跑。
	if _, err := SeedGameZones(ctx, db); err != nil {
		t.Fatalf("播种游戏分区失败：%v", err)
	}

	res, err := SeedDemoData(ctx, db)
	if err != nil {
		t.Fatalf("首次播种演示数据失败：%v", err)
	}
	if !res.ProductCreated || !res.CustomerCreated || res.PlayersCreated != 5 {
		t.Fatalf("首次播种结果不符：%+v", res)
	}
	if res.AccountsCreated != 6 || res.WalletsCreated != 6 {
		t.Fatalf("首次播种应新建 6 个账号与 6 个钱包（1 客户 + 5 陪玩）：%+v", res)
	}
	if !res.BindingCreated {
		t.Fatalf("首次播种应补建客户身份绑定（缺了客户端接口全 403）：%+v", res)
	}

	// 商品：编码固定、已上架、挂在三角洲行动分区。
	sku, err := db.EsportsSku.Query().Where(esportssku.CodeEQ(DemoSeedSkuCode)).Only(ctx)
	if err != nil {
		t.Fatalf("缺少演示商品 %s：%v", DemoSeedSkuCode, err)
	}
	if sku.Status != esportssku.StatusOnSale {
		t.Fatalf("演示商品应已上架：%+v", sku)
	}
	zone, err := db.EsportsProductCategory.Query().
		Where(esportsproductcategory.CodeEQ(DemoSeedGameZoneCode)).Only(ctx)
	if err != nil {
		t.Fatalf("缺少三角洲分区：%v", err)
	}
	if sku.CategoryID == nil || *sku.CategoryID != zone.ID {
		t.Fatalf("演示商品应挂在分区 %s：%+v", DemoSeedGameZoneCode, sku)
	}

	// 客户：档案关联账号、成年、手工来源。
	customer, err := db.EsportsCustomer.Query().
		Where(esportscustomer.DisplayNameEQ(demoCustomerName)).Only(ctx)
	if err != nil {
		t.Fatalf("缺少演示客户：%v", err)
	}
	if customer.UserID == nil || customer.AgeStatus != esportscustomer.AgeStatusAdult {
		t.Fatalf("演示客户字段不符：%+v", customer)
	}
	// 自助下单开关必须打开：risk.go 对它为 false 的客户直接拦截下单。
	if !customer.SelfOrderEnabled {
		t.Fatalf("演示客户应开启自助下单：%+v", customer)
	}

	// 身份绑定：客户端 /user/* 全部经 customerForVerifiedUser 校验，缺绑定即 403。
	account, err := db.User.Get(ctx, *customer.UserID)
	if err != nil {
		t.Fatalf("查询演示客户账号失败：%v", err)
	}
	bindings, err := db.EsportsIdentityBinding.Query().Where(
		esportsidentitybinding.CustomerIDEQ(customer.ID),
		esportsidentitybinding.ProviderEQ("user"),
		esportsidentitybinding.SubjectScopeEQ(""),
		esportsidentitybinding.ExternalSubjectEQ(account.UID),
		esportsidentitybinding.StatusEQ(esportsidentitybinding.StatusActive),
	).Count(ctx)
	if err != nil {
		t.Fatalf("查询客户身份绑定失败：%v", err)
	}
	if bindings != 1 {
		t.Fatalf("演示客户应有且仅有 1 条 active 身份绑定：得到 %d", bindings)
	}

	// 陪玩：5 条全部 qualified 且分区为三角洲行动，否则派单候选里看不到它们。
	players, err := db.EsportsPlayer.Query().
		Where(esportsplayer.DisplayNameHasPrefix("三角洲-")).All(ctx)
	if err != nil {
		t.Fatalf("查询演示陪玩失败：%v", err)
	}
	if len(players) != 5 {
		t.Fatalf("演示陪玩应为 5 条：得到 %d", len(players))
	}
	for _, p := range players {
		if p.Qualification != esportsplayer.QualificationQualified {
			t.Fatalf("陪玩 %s 应为 qualified：%+v", p.DisplayName, p)
		}
		if len(p.Games) != 1 || p.Games[0] != "三角洲行动" {
			t.Fatalf("陪玩 %s 接单分区不符：%+v", p.DisplayName, p.Games)
		}
	}

	// 钱包：1 个客户余额 + 5 个陪玩猫粮。
	wallets, err := db.EsportsWalletAccount.Query().All(ctx)
	if err != nil {
		t.Fatalf("查询钱包失败：%v", err)
	}
	var balance, catfood int
	for _, w := range wallets {
		switch w.AccountType {
		case esportswalletaccount.AccountTypeCustomerBalance:
			balance++
		case esportswalletaccount.AccountTypePlayerCatfood:
			catfood++
		}
	}
	if balance != 1 || catfood != 5 {
		t.Fatalf("钱包数量不符：客户余额 %d / 猫粮 %d", balance, catfood)
	}

	// 幂等：运营改过的陪玩名不被覆盖，重复播种不新增。
	first := players[0]
	if _, err := db.EsportsPlayer.UpdateOneID(first.ID).SetDisplayName("改过的名字").Save(ctx); err != nil {
		t.Fatalf("模拟运营改动失败：%v", err)
	}
	again, err := SeedDemoData(ctx, db)
	if err != nil {
		t.Fatalf("重复播种报错：%v", err)
	}
	if !again.Empty() {
		t.Fatalf("重复播种应为空操作：%+v", again)
	}
	if kept := db.EsportsPlayer.GetX(ctx, first.ID); kept.DisplayName != "改过的名字" {
		t.Fatalf("重复播种覆盖了运营改动：%+v", kept)
	}
}

// 演示客户必须落到 cs_admin 角色的管理员名下：客服客户列表对非超管只返回自己名下的客户，
// owner 为空时报单页选不到客户；同时重复播种不得覆盖运营调整过的归属。
func TestSeedDemoDataAssignsCustomerOwner(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_demo_owner")
	defer db.Close()
	ctx := t.Context()

	if _, err := SeedGameZones(ctx, db); err != nil {
		t.Fatalf("播种游戏分区失败：%v", err)
	}
	csRole := db.Role.Create().SetName("cs_admin").SetDisplayName("客服").
		SetStatus(role.StatusActive).SaveX(ctx)
	cs := seedAdmin(t, db, "demo-cs")
	db.AdminRole.Create().SetAdminID(cs.ID).SetRoleID(csRole.ID).SaveX(ctx)

	res, err := SeedDemoData(ctx, db)
	if err != nil {
		t.Fatalf("首次播种演示数据失败：%v", err)
	}
	if !res.OwnerAssigned {
		t.Fatalf("存在 cs_admin 管理员时应补客户归属：%+v", res)
	}
	customer, err := db.EsportsCustomer.Query().
		Where(esportscustomer.DisplayNameEQ(demoCustomerName)).Only(ctx)
	if err != nil {
		t.Fatalf("缺少演示客户：%v", err)
	}
	if customer.OwnerAdminID == nil || *customer.OwnerAdminID != cs.ID {
		t.Fatalf("演示客户应归属客服 %d，实际 %+v", cs.ID, customer.OwnerAdminID)
	}

	// 运营把归属转给别人后，重复播种不得抢回。
	other := seedAdmin(t, db, "demo-other")
	db.EsportsCustomer.UpdateOneID(customer.ID).SetOwnerAdminID(other.ID).ExecX(ctx)
	again, err := SeedDemoData(ctx, db)
	if err != nil {
		t.Fatalf("重复播种报错：%v", err)
	}
	if again.OwnerAssigned {
		t.Fatalf("已有归属时不应重复补：%+v", again)
	}
	if kept := db.EsportsCustomer.GetX(ctx, customer.ID); kept.OwnerAdminID == nil || *kept.OwnerAdminID != other.ID {
		t.Fatalf("重复播种覆盖了归属调整：%+v", kept.OwnerAdminID)
	}
}
