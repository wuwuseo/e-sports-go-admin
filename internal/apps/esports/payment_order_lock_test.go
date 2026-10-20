package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportsorder"
)

// §8.2-15 / 附录 B.3-1：收款并发窗口的订单级串行化回归。
//
// 这两条路径（余额核销 / 凭证提交与核验）都要先判「同一订单是否已有待核验凭证」，
// 仅靠同事务读无法关闭极端并发窗口。实现用「订单行乐观版本守卫（条件更新式占位）」：
// 事务前的读给出 expectedVersion，事务内的占位 `UPDATE ... WHERE version = ? AND 待付款` 影响行数
// 不为 1 即冲突（MySQL 下该 UPDATE 本身持有行级写锁，等价 SELECT ... FOR UPDATE）。
// 内存 SQLite 无 FOR UPDATE 且并发写事务会直接互相锁表，故用 paymentOrderInterleaveHook
// 复现「A 读到旧版本 → B 完整提交 → A 写回」的同一时序：A 必须在动钱之前失败。
func setPaymentOrderInterleaveHook(t *testing.T, hook func(orderID int)) {
	t.Helper()
	paymentOrderInterleaveHook = hook
	t.Cleanup(func() { paymentOrderInterleaveHook = nil })
}

// onceOnlyHook 生成一个只执行一次的交错点（避免交错点内部再触发自身）。
func onceOnlyHook(body func()) func(orderID int) {
	entered := false
	return func(orderID int) {
		if entered {
			return
		}
		entered = true
		defer func() { entered = false }()
		body()
	}
}

// A（余额核销）读到「无待核验凭证」后，B 提交转账凭证并提交事务 → A 写回必须失败，且不动钱。
func TestOrderLockBlocksBalancePaymentWhenProofSubmittedInWindow(t *testing.T) {
	db := openDuplicateGuardDB(t, "esports_order_lock_proof_window")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-OL-0001")
	intent := seedPayableOrderForOffline(t, db, "ollock", "wechat_work")
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, order.CustomerID,
		walletInput{PrincipalCents: order.PayableCents + 1000, Note: "并发窗口测试"},
		"ol-lock-recharge"); err != nil {
		t.Fatal(err)
	}

	var proofErr error
	setPaymentOrderInterleaveHook(t, onceOnlyHook(func() {
		_, proofErr = submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
			OrderID: order.ID, AccountID: account.ID, TransactionNo: "WX-OL-TXN-0001",
			AmountCents: order.PayableCents, ProofRef: "proof/ol-lock-0001.png",
		}, 11, now)
	}))

	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("交错点后余额核销必须判冲突，err=%v", err)
	}
	if proofErr != nil {
		t.Fatalf("交错点内提交待核验凭证应成功：%v", proofErr)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, order.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.total() != order.PayableCents+1000 {
		t.Fatalf("失败的余额核销不得扣款：%+v", balance)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != esportsorder.PaymentStatusUnpaid ||
		reload.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment {
		t.Fatalf("失败的余额核销不得改单：%+v", reload)
	}
	if got := db.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.OrderIDEQ(order.ID)).CountX(ctx); got != 1 {
		t.Fatalf("待核验凭证应只有 B 提交的那 1 张：%d", got)
	}
}

// 反向：A（提交待核验凭证）读到「订单待付款」后，B 已用余额核销并提交 → A 必须失败且不落凭证，
// 避免出现「订单已付款 + 仍挂一张待核验凭证」的悬账。
func TestOrderLockBlocksProofSubmitWhenBalancePaidInWindow(t *testing.T) {
	db := openDuplicateGuardDB(t, "esports_order_lock_submit_window")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-OL-0002")
	intent := seedPayableOrderForOffline(t, db, "ollocksubmit", "wechat_work")
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, order.CustomerID,
		walletInput{PrincipalCents: order.PayableCents}, "ol-lock-submit-recharge"); err != nil {
		t.Fatal(err)
	}

	var paidErr error
	setPaymentOrderInterleaveHook(t, onceOnlyHook(func() {
		_, paidErr = payOrderWithBalance(ctx, db, 11, order.ID, now)
	}))

	if _, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderID: order.ID, AccountID: account.ID, TransactionNo: "WX-OL-TXN-0002",
		AmountCents: order.PayableCents, ProofRef: "proof/ol-lock-0003.png",
	}, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("交错点后提交凭证必须判冲突，err=%v", err)
	}
	if paidErr != nil {
		t.Fatalf("交错点内余额核销应成功：%v", paidErr)
	}
	if got := db.EsportsOfflinePaymentProof.Query().CountX(ctx); got != 0 {
		t.Fatalf("被拒的凭证不得落库：%d", got)
	}
	if reload := db.EsportsOrder.GetX(ctx, order.ID); reload.PaymentStatus != esportsorder.PaymentStatusPaid ||
		reload.PaymentChannel != esportsorder.PaymentChannelCustomerBalance {
		t.Fatalf("订单应已由余额核销：%+v", reload)
	}
}

// 凭证核验路径的订单级占位：读到旧版本后订单已被别的收款动作推进 → 占位失败（在核销前拦住）。
func TestOrderLockReserveRejectsWhenOrderPaidInWindow(t *testing.T) {
	db := openDuplicateGuardDB(t, "esports_order_lock_reserve_window")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	intent := seedPayableOrderForOffline(t, db, "olreserve", "wechat_work")
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, order.CustomerID,
		walletInput{PrincipalCents: order.PayableCents}, "ol-lock-reserve-recharge"); err != nil {
		t.Fatal(err)
	}

	var paidErr error
	setPaymentOrderInterleaveHook(t, onceOnlyHook(func() {
		_, paidErr = payOrderWithBalance(ctx, db, 11, order.ID, now)
	}))

	if err := reserveOrderPaymentVersion(ctx, db, order.ID); !errors.Is(err, errConflict) {
		t.Fatalf("订单已被并发推进时占位必须判冲突，err=%v", err)
	}
	if paidErr != nil {
		t.Fatalf("交错点内余额核销应成功：%v", paidErr)
	}
	if reload := db.EsportsOrder.GetX(ctx, order.ID); reload.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("并发核销结果应保留：%+v", reload)
	}
}

// 正常单请求路径不受影响：余额核销、凭证提交 + 核验两条路径各自一遍走通。
func TestOrderLockKeepsSingleRequestPaymentPathsWorking(t *testing.T) {
	t.Run("余额核销", func(t *testing.T) {
		db := openDuplicateGuardDB(t, "esports_order_lock_normal_balance")
		defer db.Close()
		ctx := t.Context()
		now := time.Now().UTC()

		seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-7701")
		intent := seedPayableOrderForOffline(t, db, "olnormbal", "corporate_transfer")
		order := db.EsportsOrder.GetX(ctx, intent.OrderID)
		if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, order.CustomerID,
			walletInput{PrincipalCents: order.PayableCents + 500, Note: "正常路径"},
			"ol-normal-balance-recharge"); err != nil {
			t.Fatal(err)
		}
		paid, err := payOrderWithBalance(ctx, db, 11, order.ID, now)
		if err != nil {
			t.Fatalf("单请求余额核销应成功：%v", err)
		}
		if paid.PaymentStatus != esportsorder.PaymentStatusPaid ||
			paid.PaymentChannel != esportsorder.PaymentChannelCustomerBalance ||
			paid.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
			t.Fatalf("余额核销后订单状态不正确：%+v", paid)
		}
		if paid.Version <= order.Version {
			t.Fatalf("余额核销必须推进订单版本：%d -> %d", order.Version, paid.Version)
		}
		balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, order.CustomerID)
		if err != nil || balance.total() != 500 {
			t.Fatalf("余额核销应只扣应付额：%+v err=%v", balance, err)
		}
	})

	t.Run("凭证提交与核验", func(t *testing.T) {
		db := openDuplicateGuardDB(t, "esports_order_lock_normal_proof")
		defer db.Close()
		ctx := t.Context()
		now := time.Now().UTC()

		account := seedActivePaymentAccount(t, db, "wechat_work", "WX-OL-0003")
		intent := seedPayableOrderForOffline(t, db, "olnormproof", "wechat_work")
		order := db.EsportsOrder.GetX(ctx, intent.OrderID)
		proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
			OrderID: order.ID, AccountID: account.ID, TransactionNo: "WX-OL-TXN-0003",
			AmountCents: order.PayableCents, ProofRef: "proof/ol-lock-0004.png",
		}, 11, now)
		if err != nil {
			t.Fatalf("单请求提交凭证应成功：%v", err)
		}
		verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("单请求核验凭证应成功：%v", err)
		}
		if verified.Status != esportsofflinepaymentproof.StatusVerified {
			t.Fatalf("凭证核验后应为 verified：%+v", verified)
		}
		paid := db.EsportsOrder.GetX(ctx, order.ID)
		if paid.PaymentStatus != esportsorder.PaymentStatusPaid ||
			paid.PaymentChannel != esportsorder.PaymentChannelWechatWork {
			t.Fatalf("凭证核验后订单应转已付款：%+v", paid)
		}
	})
}
