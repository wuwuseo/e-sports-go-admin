package esports

import (
	"strconv"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/global"
)

// ── P1：陪玩猫粮的充值 / 调整必须由超管兜底（陪玩没有归属客服这一关系） ──

// TestCatfoodWalletMutationRequiresSuperAdmin 覆盖 /admin/wallets/:id/recharge 与 /adjust 两条写入口：
// 非超管客服写猫粮被拒且不落账；超管放行；客户余额的归属边界保持不变。
func TestCatfoodWalletMutationRequiresSuperAdmin(t *testing.T) {
	db := openProductDB(t, "esports_catfood_guard")
	defer db.Close()
	ctx := t.Context()

	player := seedPlayer(t, db, 7001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	csAdmin := seedAdminAccount(t, db, "catfood-cs")
	superAdmin := seedAdminAccount(t, db, "catfood-super")

	previousORM := global.ORM
	global.ORM = db
	defer func() { global.ORM = previousORM }()

	base := "/admin/wallets/" + strconv.Itoa(player.ID)
	catfoodBody := map[string]any{"principal_cents": 1000, "note": "运营补发猫粮"}

	// 非超管客服：猫粮充值被拒，且不产生流水。
	denied := ownershipGuardPost(t, ownershipGuardWalletApp(csAdmin, false),
		base+"/recharge?account_type=player_catfood", "catfood-key-0001", catfoodBody)
	if denied.status != fiber.StatusForbidden || !strings.Contains(denied.message, "仅限超级管理员") {
		t.Fatalf("非超管客服猫粮充值应返回 403 与中文提示，实际 %d %q", denied.status, denied.message)
	}
	if n := db.EsportsWalletTx.Query().CountX(ctx); n != 0 {
		t.Fatalf("被拒的猫粮充值不得落账，实际流水 %d 条", n)
	}

	// 非超管客服：猫粮调整同样被拒。
	adjustDenied := ownershipGuardPost(t, ownershipGuardWalletApp(csAdmin, false),
		base+"/adjust?account_type=player_catfood", "catfood-key-0002",
		map[string]any{"principal_cents": 100, "note": "越权调账"})
	if adjustDenied.status != fiber.StatusForbidden || !strings.Contains(adjustDenied.message, "仅限超级管理员") {
		t.Fatalf("非超管客服猫粮调整应返回 403 与中文提示，实际 %d %q", adjustDenied.status, adjustDenied.message)
	}

	// 超管：猫粮充值放行并正确入账。
	allowed := ownershipGuardPost(t, ownershipGuardWalletApp(superAdmin, true),
		base+"/recharge?account_type=player_catfood", "catfood-key-0003", catfoodBody)
	if allowed.status != fiber.StatusOK {
		t.Fatalf("超管猫粮充值应成功，实际 %d %q", allowed.status, allowed.message)
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatalf("读取猫粮余额失败：%v", err)
	}
	if balance.total() != 1000 {
		t.Fatalf("猫粮余额不正确：%d", balance.total())
	}

	// 客户余额不受影响：非归属客服仍被拒，归属客服仍可充值。
	mineCustomer := saveCustomerOrFail(t, db, csAdmin.ID)
	otherCustomer := saveCustomerOrFail(t, db, superAdmin.ID)
	foreign := ownershipGuardPost(t, ownershipGuardWalletApp(csAdmin, false),
		"/admin/wallets/"+strconv.Itoa(otherCustomer.ID)+"/recharge?account_type=customer_balance",
		"catfood-key-0004", map[string]any{"principal_cents": 500, "note": "越权充值"})
	if foreign.status != fiber.StatusForbidden {
		t.Fatalf("非归属客服为客户余额充值应返回 403，实际 %d %q", foreign.status, foreign.message)
	}
	owned := ownershipGuardPost(t, ownershipGuardWalletApp(csAdmin, false),
		"/admin/wallets/"+strconv.Itoa(mineCustomer.ID)+"/recharge?account_type=customer_balance",
		"catfood-key-0005", map[string]any{"principal_cents": 500, "note": "客户充值"})
	if owned.status != fiber.StatusOK {
		t.Fatalf("归属客服为客户余额充值应成功，实际 %d %q", owned.status, owned.message)
	}
}
