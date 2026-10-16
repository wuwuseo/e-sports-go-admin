package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsconfirmation"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidempotency"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsstoredvaluecard"
)

// seedSelfStoredValueCustomer 造一个「已绑定登录身份 + 成年 + 已分配归属客服」的自助购卡客户。
func seedSelfStoredValueCustomer(t *testing.T, db *ent.Client, ownerID int) *ent.EsportsCustomer {
	t.Helper()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, ownerID)
	uid := "uid-customer-" + strconv.Itoa(customer.ID)
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").
		SetSubjectScope("").SetExternalSubject(uid).
		SetVerificationMethod("verified_account_link").SetVerifiedAt(time.Now().UTC()).SaveX(ctx)
	return db.EsportsCustomer.GetX(ctx, customer.ID)
}

// customerSelfStoredValuePurchaseKey 造合法的自助购卡入参（套餐 / 本金档由调用方覆盖）。
func customerSelfStoredValuePurchaseKey(key string) customerStoredValuePurchaseInput {
	return customerStoredValuePurchaseInput{
		PrincipalCents: 10000, PaymentChannel: "merchant_qr", ValidityDays: 90,
		ContractConfirmed: true, IdempotencyKey: key,
	}
}

// 自助购卡：合同确认 + 支付意图齐备，卡先零余额待核验，资金一律等到核验后才入账。
func TestCustomerSelfStoredValuePurchaseCreatesPendingCardAndIntent(t *testing.T) {
	db := openStoredValueDB(t, "esports_customer_sv_purchase")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer := seedSelfStoredValueCustomer(t, db, 11)

	pkg, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "SV-SELF-PKG", Name: "充 200 送 30", PrincipalCents: 20000, BonusCents: 3000, SortOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	in := customerSelfStoredValuePurchaseKey("self-sv-purchase-package")
	// 套餐优先（与既有口径一致）：同时传本金档时以套餐本金 / 赠送为准。
	in.PackageID = pkg.ID
	in.PaymentChannel = "corporate_transfer"
	view, err := purchaseCustomerStoredValueCard(ctx, db, customer, in, now)
	if err != nil {
		t.Fatalf("自助购卡应成功，err=%v", err)
	}
	// 待核验：零余额、零消费，本金与赠送已登记但未发放。
	if view.Card.Status != esportsstoredvaluecard.StatusPendingVerification ||
		view.Card.BalanceCents != 0 || view.Card.PrincipalRemainingCents != 0 ||
		view.Card.BonusRemainingCents != 0 || view.Card.ConsumedCents != 0 {
		t.Fatalf("自助购卡申请应零余额待核验：%+v", view.Card)
	}
	if view.Card.Source != esportsstoredvaluecard.SourceSelf || !view.Card.ContractConfirmed ||
		view.Card.OpenedByAdminID != nil {
		t.Fatalf("自助开卡应记 source=self、已确认合同且无经办客服：%+v", view.Card)
	}
	if view.Card.PackageID == nil || *view.Card.PackageID != pkg.ID {
		t.Fatalf("套餐开卡应记录套餐 ID：%+v", view.Card)
	}
	if view.Card.PrincipalCents != 20000 || view.Card.BonusCents != 3000 ||
		view.PayableCents != 20000 || view.Card.CustomerID != customer.ID {
		t.Fatalf("套餐本金 / 赠送口径不正确：%+v", view)
	}
	if view.PaymentRef == "" || view.IntentNo != view.PaymentRef || view.IntentStatus != "pending" ||
		view.NextAction != "pay_and_wait_verification" || view.PayWindowExpiresAt.IsZero() {
		t.Fatalf("支付意图与付款码不正确：%+v", view)
	}
	// 支付意图：独立于订单（保留段编码卡 ID）、金额为实付本金、渠道与申请一致、已挂合同确认书。
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(view.IntentNo)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if intent.OrderID != customerStoredValueIntentOrderID(view.Card.ID) ||
		customerStoredValueIntentCardID(intent.OrderID) != view.Card.ID {
		t.Fatalf("支付意图应编码卡 ID：orders=%d card=%d", intent.OrderID, view.Card.ID)
	}
	if intent.AmountCents != 20000 || intent.Channel != "corporate_transfer" ||
		intent.Status != esportspaymentintent.StatusPending ||
		intent.ConfirmationID != view.ConfirmationID || intent.QuoteVersion != view.Card.Version {
		t.Fatalf("支付意图不正确：%+v", intent)
	}
	// 合同确认书：绑定登录身份的已验证证据。
	confirmation, err := db.EsportsConfirmation.Get(ctx, view.ConfirmationID)
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.SubjectType != customerStoredValueContractSubject ||
		confirmation.SubjectID != view.Card.ID || confirmation.CustomerID != customer.ID ||
		confirmation.Channel != customerStoredValueSelfChannel ||
		confirmation.Status != esportsconfirmation.StatusVerified ||
		confirmation.VerifiedAt == nil || !view.ContractConfirmedAt.Equal(*confirmation.VerifiedAt) {
		t.Fatalf("合同确认书不正确：%+v", confirmation)
	}
	if len(confirmation.EvidenceRef) < len("user_binding:") ||
		confirmation.EvidenceRef[:len("user_binding:")] != "user_binding:" {
		t.Fatalf("合同确认证据应绑定登录身份：%s", confirmation.EvidenceRef)
	}
	// 未核验前不得有任何入账。
	if got := db.EsportsWalletTx.Query().CountX(ctx); got != 0 {
		t.Fatalf("申请阶段不得产生钱包流水，got=%d", got)
	}
	if got := db.EsportsLedgerEntry.Query().CountX(ctx); got != 0 {
		t.Fatalf("申请阶段不得产生预付款账本，got=%d", got)
	}
	// 指定本金档 + 会员等级赠送比例：本金 10000 → 赠送 1000。
	level, err := createMemberLevel(ctx, db, memberLevelInput{
		Code: "SV-SELF-GOLD", Name: "金卡", DiscountBps: memberDiscountBpsFull,
		PrepayBonusBps: 1000, SortOrder: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode(level.Code).SaveX(ctx)
	customer = db.EsportsCustomer.GetX(ctx, customer.ID)
	custom, err := purchaseCustomerStoredValueCard(ctx, db, customer,
		customerSelfStoredValuePurchaseKey("self-sv-purchase-principal"), now)
	if err != nil {
		t.Fatalf("指定本金档购卡应成功，err=%v", err)
	}
	if custom.Card.PrincipalCents != 10000 || custom.Card.BonusCents != 1000 ||
		custom.Card.PackageID != nil || custom.PayableCents != 10000 {
		t.Fatalf("指定本金档应按会员等级比例赠送：%+v", custom)
	}
}

// 自助购卡的风控与资格拦截：未确认合同、客户端传赠送、余额渠道、未绑定身份、受限客户、未成年、
// 无归属客服、套餐与本金档同传、下架套餐、本金越界都必须拒绝且不建卡。
func TestCustomerSelfStoredValuePurchaseGuards(t *testing.T) {
	db := openStoredValueDB(t, "esports_customer_sv_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer := seedSelfStoredValueCustomer(t, db, 11)
	offSale, err := saveStoredValuePackage(ctx, db, storedValuePackageInput{
		Code: "SV-SELF-OFF", Name: "下架档", PrincipalCents: 20000, BonusCents: 3000, SortOrder: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = setStoredValuePackageStatus(ctx, db, offSale.ID, "off_sale"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		input customerStoredValuePurchaseInput
		want  error
	}{
		{"未确认合同", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-no-contract")
			in.ContractConfirmed = false
			return in
		}(), errInvalidInput},
		{"客户端传赠送权益", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-bonus")
			in.BonusCents = 50000
			return in
		}(), errInvalidInput},
		{"余额划转渠道", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-balance")
			in.PaymentChannel = "customer_balance"
			return in
		}(), errInvalidInput},
		{"无效渠道", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-channel")
			in.PaymentChannel = "cash"
			return in
		}(), errInvalidInput},
		{"下架套餐", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-off-sale")
			in.PackageID = offSale.ID
			in.PrincipalCents = 0
			return in
		}(), errInvalidInput},
		{"本金档低于下限", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-min")
			in.PrincipalCents = customerStoredValueMinPrincipalCents - 1
			return in
		}(), errInvalidInput},
		{"有效期天数越界", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-validity")
			in.ValidityDays = customerStoredValueMaxValidDays + 1
			return in
		}(), errInvalidInput},
		{"幂等键过短", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("short")
			return in
		}(), errInvalidInput},
		{"到期日格式非法", func() customerStoredValuePurchaseInput {
			in := customerSelfStoredValuePurchaseKey("guards-until")
			in.ValidityDays = 0
			in.ValidUntil = "2027/01/01"
			return in
		}(), errInvalidInput},
	}
	for _, item := range cases {
		if _, err := purchaseCustomerStoredValueCard(ctx, db, customer, item.input, now); !errors.Is(err, item.want) {
			t.Fatalf("%s 应被拒，err=%v", item.name, err)
		}
	}
	// 未绑定登录身份：另造一个没有身份绑定的客户。
	unbound := saveCustomerOrFail(t, db, 11)
	if _, err := purchaseCustomerStoredValueCard(ctx, db, unbound,
		customerSelfStoredValuePurchaseKey("guards-unbound"), now); !errors.Is(err, errForbidden) {
		t.Fatalf("未绑定登录身份应被拒，err=%v", err)
	}
	// 被限制自助下单：按既有风险口径拦截。
	restricted := seedSelfStoredValueCustomer(t, db, 11)
	db.EsportsCustomer.UpdateOneID(restricted.ID).
		SetRiskStatus(esportscustomer.RiskStatusRestricted).SetRiskReason("频繁恶意退款").SaveX(ctx)
	restricted = db.EsportsCustomer.GetX(ctx, restricted.ID)
	if _, err := purchaseCustomerStoredValueCard(ctx, db, restricted,
		customerSelfStoredValuePurchaseKey("guards-restricted"), now); !errors.Is(err, errForbidden) {
		t.Fatalf("受限客户应被拒，err=%v", err)
	}
	// 未成年：按年龄资格口径拦截。
	minor := seedSelfStoredValueCustomer(t, db, 11)
	db.EsportsCustomer.UpdateOneID(minor.ID).SetAgeStatus("minor").SaveX(ctx)
	minor = db.EsportsCustomer.GetX(ctx, minor.ID)
	if _, err := purchaseCustomerStoredValueCard(ctx, db, minor,
		customerSelfStoredValuePurchaseKey("guards-minor"), now); !errors.Is(err, errAgeUnverified) {
		t.Fatalf("未成年客户应被拒，err=%v", err)
	}
	// 未分配归属客服。
	orphan := db.EsportsCustomer.Create().SetDisplayName("无归属客户").SetAgeStatus("adult").SaveX(ctx)
	db.EsportsIdentityBinding.Create().SetCustomerID(orphan.ID).SetProvider("user").
		SetSubjectScope("").SetExternalSubject("uid-orphan").
		SetVerificationMethod("verified_account_link").SetVerifiedAt(now).SaveX(ctx)
	if _, err := purchaseCustomerStoredValueCard(ctx, db, orphan,
		customerSelfStoredValuePurchaseKey("guards-orphan"), now); !errors.Is(err, errForbidden) {
		t.Fatalf("无归属客服应被拒，err=%v", err)
	}
	// 以上全部拒绝后不得留下任何卡或支付意图。
	if got := db.EsportsStoredValueCard.Query().CountX(ctx); got != 0 {
		t.Fatalf("被拒的申请不得建卡，got=%d", got)
	}
	if got := db.EsportsPaymentIntent.Query().CountX(ctx); got != 0 {
		t.Fatalf("被拒的申请不得建支付意图，got=%d", got)
	}
}

// 幂等：同一客户同一幂等键重复提交只落一张卡（连同确认书与支付意图），换内容冲突，换客户互不影响。
func TestCustomerSelfStoredValuePurchaseIdempotent(t *testing.T) {
	db := openStoredValueDB(t, "esports_customer_sv_idempotent")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer := seedSelfStoredValueCustomer(t, db, 11)

	in := customerSelfStoredValuePurchaseKey("self-sv-idem-1")
	first, err := purchaseCustomerStoredValueCard(ctx, db, customer, in, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := purchaseCustomerStoredValueCard(ctx, db, customer, in, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("同幂等键重复提交应返回原申请，err=%v", err)
	}
	if again.Card.ID != first.Card.ID || again.IntentNo != first.IntentNo ||
		again.ConfirmationID != first.ConfirmationID {
		t.Fatalf("重复提交应复用同一张卡与支付意图：%+v / %+v", again, first)
	}
	if got := db.EsportsStoredValueCard.Query().CountX(ctx); got != 1 {
		t.Fatalf("重复提交不得重复建卡，got=%d", got)
	}
	if got := db.EsportsPaymentIntent.Query().CountX(ctx); got != 1 {
		t.Fatalf("重复提交不得重复建支付意图，got=%d", got)
	}
	if got := db.EsportsConfirmation.Query().CountX(ctx); got != 1 {
		t.Fatalf("重复提交不得重复建合同确认书，got=%d", got)
	}
	if got := db.EsportsIdempotency.Query().Where(
		esportsidempotency.OperationEQ(customerStoredValuePurchaseOperation)).CountX(ctx); got != 1 {
		t.Fatalf("重复提交不得重复写幂等记录，got=%d", got)
	}
	// 同一幂等键换内容：判别为请求不一致。
	changed := in
	changed.PrincipalCents = 20000
	if _, err = purchaseCustomerStoredValueCard(ctx, db, customer, changed, now); !errors.Is(err, errConflict) {
		t.Fatalf("同幂等键换内容应冲突，err=%v", err)
	}
	// 换幂等键：允许再购一张。
	other, err := purchaseCustomerStoredValueCard(ctx, db, customer,
		customerSelfStoredValuePurchaseKey("self-sv-idem-2"), now)
	if err != nil || other.Card.ID == first.Card.ID {
		t.Fatalf("换幂等键应另建一张卡：%+v %v", other, err)
	}
	// 幂等键按 customer:<id> 隔离：另一名客户用同一个键不受影响。
	another := seedSelfStoredValueCustomer(t, db, 11)
	isolated, err := purchaseCustomerStoredValueCard(ctx, db, another,
		customerSelfStoredValuePurchaseKey("self-sv-idem-1"), now)
	if err != nil {
		t.Fatalf("他人的同名幂等键不应互相影响，err=%v", err)
	}
	if isolated.Card.ID == first.Card.ID || isolated.Card.CustomerID != another.ID {
		t.Fatalf("不同客户的同键购卡应各建一张：%+v", isolated)
	}
}

// 核验：财务核对到账后激活、写卡内余额与预付款批次并同步支付意图；驳回保持零余额并作废意图。
func TestCustomerSelfStoredValueVerificationActivatesCard(t *testing.T) {
	db := openStoredValueDB(t, "esports_customer_sv_verification")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer := seedSelfStoredValueCustomer(t, db, 11)

	in := customerSelfStoredValuePurchaseKey("self-sv-verify-1")
	in.PrincipalCents = 10000
	in.ValidityDays = 30
	pending, err := purchaseCustomerStoredValueCard(ctx, db, customer, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Card.BalanceCents != 0 {
		t.Fatalf("核验前卡余额应为零：%+v", pending.Card)
	}
	// 自助卡没有经办客服，任一名财务管理员都可核验（不存在自审）。
	activated, err := verifyCustomerStoredValuePurchasePayment(ctx, db, 22, pending.Card.ID, true,
		pending.Card.PrincipalCents, "对照实际到账流水与收款账户核验", now)
	if err != nil {
		t.Fatalf("财务核验应激活自助卡，err=%v", err)
	}
	if activated.Status != esportsstoredvaluecard.StatusActive ||
		activated.PrincipalRemainingCents != 10000 || activated.BalanceCents != 10000 {
		t.Fatalf("核验后应激活并按本金入账：%+v", activated)
	}
	if !activated.ValidFrom.Equal(now.UTC()) || !activated.ValidUntil.Equal(now.UTC().AddDate(0, 0, 30)) {
		t.Fatalf("有效天数应从实际激活日计算：%v ~ %v", activated.ValidFrom, activated.ValidUntil)
	}
	if activated.PaymentVerifiedByAdminID == nil || *activated.PaymentVerifiedByAdminID != 22 ||
		activated.VerifiedPaymentKey == nil ||
		*activated.VerifiedPaymentKey != "merchant_qr:"+pending.PaymentRef {
		t.Fatalf("核验留痕不正确：%+v", activated)
	}
	// 卡内余额流水与预付款批次。
	transactions := db.EsportsWalletTx.Query().Where().AllX(ctx)
	if len(transactions) != 1 || transactions[0].PrincipalDeltaCents != 10000 ||
		transactions[0].AccountType.String() != walletStoredValueCard {
		t.Fatalf("应写一条开卡入账流水：%+v", transactions)
	}
	batches := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("card_open")).AllX(ctx)
	if len(batches) != 1 || batches[0].DeltaCents != 10000 ||
		batches[0].FundBatch != "merchant_qr:"+pending.PaymentRef ||
		batches[0].AccountID != strconv.Itoa(pending.Card.ID) {
		t.Fatalf("预付款批次账本不正确：%+v", batches)
	}
	// 支付意图同步为已支付。
	intent := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(pending.IntentNo)).OnlyX(ctx)
	if intent.Status != esportspaymentintent.StatusPaid {
		t.Fatalf("核验后支付意图应置已支付，got=%s", intent.Status)
	}
	// 重复核验不得重复入账。
	if _, err = verifyCustomerStoredValuePurchasePayment(ctx, db, 23, pending.Card.ID, true,
		pending.Card.PrincipalCents, "重复核验同一张自助卡", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复核验应冲突，err=%v", err)
	}
	// 驳回路径：保持零余额、作废支付意图、不产生新的资金流水。
	toReject, err := purchaseCustomerStoredValueCard(ctx, db, customer,
		customerSelfStoredValuePurchaseKey("self-sv-verify-reject"), now)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := verifyCustomerStoredValuePurchasePayment(ctx, db, 22, toReject.Card.ID, false,
		0, "未查到对应到账流水，驳回申请", now)
	if err != nil || rejected.Status != esportsstoredvaluecard.StatusRejected || rejected.BalanceCents != 0 {
		t.Fatalf("驳回应保持零余额：%+v %v", rejected, err)
	}
	if got := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(toReject.PaymentRef)).OnlyX(ctx).Status; got != esportspaymentintent.StatusInvalidated {
		t.Fatalf("驳回后支付意图应作废，got=%s", got)
	}
	if got := db.EsportsWalletTx.Query().CountX(ctx); got != 1 {
		t.Fatalf("驳回不得产生新的入账流水，got=%d", got)
	}
	// 非自助购卡的卡不适用自助核验入口。
	staffCard := db.EsportsStoredValueCard.Create().SetCardNo("SVC-STAFF-VERIFY").
		SetCustomerID(customer.ID).SetSource(esportsstoredvaluecard.SourceStaff).
		SetPaymentChannel(esportsstoredvaluecard.PaymentChannelMerchantQr).
		SetPrincipalCents(1000).SetStatus(esportsstoredvaluecard.StatusPendingVerification).
		SetPaymentRef("STAFF-REF-1").SetValidFrom(now).SetValidUntil(now.AddDate(0, 1, 0)).SaveX(ctx)
	if _, err = verifyCustomerStoredValuePurchasePayment(ctx, db, 22, staffCard.ID, true, 1000,
		"客服代办卡不应走自助核验入口", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非自助卡应被自助核验入口拒绝，err=%v", err)
	}
}

// 已验签通道适配器核销：渠道流水唯一 + 回调幂等 + 金额 / 渠道 / 有效期校验后走既有核验激活。
func TestCustomerSelfStoredValueChannelSettlement(t *testing.T) {
	db := openStoredValueDB(t, "esports_customer_sv_channel")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	customer := seedSelfStoredValueCustomer(t, db, 11)

	first, err := purchaseCustomerStoredValueCard(ctx, db, customer,
		customerSelfStoredValuePurchaseKey("self-sv-channel-1"), now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := purchaseCustomerStoredValueCard(ctx, db, customer,
		customerSelfStoredValuePurchaseKey("self-sv-channel-2"), now)
	if err != nil {
		t.Fatal(err)
	}
	settlement := customerStoredValueSettlementInput{
		IntentNo: first.IntentNo, Channel: "merchant_qr", TransactionNo: "WX-CHANNEL-0001",
		ReceivedCents: first.PayableCents, ConfirmedAt: now, OperatorAdminID: 22,
		EvidenceNote: "通道回调已验签，商户号与到账时间一致",
	}
	// 渠道不符 / 金额不符 / 意图不存在：一律拒绝。
	wrongChannel := settlement
	wrongChannel.Channel = "wechat_work"
	if _, err = settleCustomerStoredValuePurchase(ctx, db, wrongChannel, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("到账渠道不符应被拒，err=%v", err)
	}
	wrongAmount := settlement
	wrongAmount.ReceivedCents = settlement.ReceivedCents - 1
	if _, err = settleCustomerStoredValuePurchase(ctx, db, wrongAmount, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("实收金额不符应被拒，err=%v", err)
	}
	unknown := settlement
	unknown.IntentNo = "SVPAY-NOT-EXIST"
	if _, err = settleCustomerStoredValuePurchase(ctx, db, unknown, now); err == nil {
		t.Fatal("未知支付意图应被拒")
	}
	// 正常核销。
	settled, err := settleCustomerStoredValuePurchase(ctx, db, settlement, now)
	if err != nil {
		t.Fatalf("通道核销应成功，err=%v", err)
	}
	if settled.Status != esportsstoredvaluecard.StatusActive || settled.BalanceCents != first.PayableCents {
		t.Fatalf("通道核销后应激活并入账：%+v", settled)
	}
	receipt, err := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ(settlement.Channel),
		esportspaymentreceipt.TransactionNoEQ(settlement.TransactionNo),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != esportspaymentreceipt.StatusVerified ||
		receipt.AppliedCents != first.PayableCents || receipt.ExceptionCents != 0 ||
		receipt.VerifiedByAdminID == nil || *receipt.VerifiedByAdminID != 22 {
		t.Fatalf("渠道回执不正确：%+v", receipt)
	}
	if got := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(first.IntentNo)).OnlyX(ctx).Status; got != esportspaymentintent.StatusPaid {
		t.Fatalf("核销后支付意图应为已支付，got=%s", got)
	}
	// 重复回调：幂等返回已激活卡，不重复入账。
	repeat, err := settleCustomerStoredValuePurchase(ctx, db, settlement, now.Add(time.Minute))
	if err != nil || repeat.CardNo != settled.CardNo || repeat.BalanceCents != settled.BalanceCents {
		t.Fatalf("重复回调应幂等返回已激活卡：%+v %v", repeat, err)
	}
	if got := db.EsportsPaymentReceipt.Query().CountX(ctx); got != 1 {
		t.Fatalf("重复回调不得重复写回执，got=%d", got)
	}
	// 同一笔渠道流水不得激活第二张卡。
	reused := settlement
	reused.IntentNo = second.IntentNo
	if _, err = settleCustomerStoredValuePurchase(ctx, db, reused, now); !errors.Is(err, errConflict) {
		t.Fatalf("同一渠道流水复用应冲突，err=%v", err)
	}
	if got := db.EsportsStoredValueCard.GetX(ctx, second.Card.ID).Status; got != esportsstoredvaluecard.StatusPendingVerification {
		t.Fatalf("冲突后第二张卡应保持待核验，got=%s", got)
	}
	// 支付意图过期：通道不再自动核销，卡保持待核验（仍可由财务人工核验）。
	db.SystemConfig.Create().SetKey(customerStoredValuePayWindowKey).SetName("自助购卡支付窗口").
		SetValue("1").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	expiring, err := purchaseCustomerStoredValueCard(ctx, db, customer,
		customerSelfStoredValuePurchaseKey("self-sv-channel-expired"), now)
	if err != nil {
		t.Fatal(err)
	}
	expired := settlement
	expired.IntentNo = expiring.IntentNo
	expired.TransactionNo = "WX-CHANNEL-0002"
	expired.ConfirmedAt = now.Add(2 * time.Minute)
	if _, err = settleCustomerStoredValuePurchase(ctx, db, expired, now.Add(2*time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("过期支付意图应拒绝自动核销，err=%v", err)
	}
	// 过期扫描只把未核验卡的意图置 expired，已激活卡不受影响。
	if expiring.PayWindowExpiresAt.After(now.Add(2 * time.Minute)) {
		t.Fatalf("支付窗口配置未生效：%v", expiring.PayWindowExpiresAt)
	}
	count, err := expireCustomerStoredValueIntents(ctx, db, now.Add(2*time.Minute), 100)
	if err != nil || count != 1 {
		t.Fatalf("应过期 1 条自助购卡支付意图，got=%d err=%v", count, err)
	}
	if got := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(expiring.IntentNo)).OnlyX(ctx).Status; got != esportspaymentintent.StatusExpired {
		t.Fatalf("过期意图应置 expired，got=%s", got)
	}
	if got := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(first.IntentNo)).OnlyX(ctx).Status; got != esportspaymentintent.StatusPaid {
		t.Fatalf("已支付意图不得被过期扫描改动，got=%s", got)
	}
}

// 我的储值卡：列表与详情只返回本人数据；购卡申请详情仅自助卡可查。
func TestCustomerSelfStoredValueMyCardsScopedToOwner(t *testing.T) {
	db := openStoredValueDB(t, "esports_customer_sv_scope")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	mine := seedSelfStoredValueCustomer(t, db, 11)
	other := seedSelfStoredValueCustomer(t, db, 12)

	purchased, err := purchaseCustomerStoredValueCard(ctx, db, mine,
		customerSelfStoredValuePurchaseKey("self-sv-scope-mine"), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifyCustomerStoredValuePurchasePayment(ctx, db, 22, purchased.Card.ID, true,
		purchased.PayableCents, "对照实际到账流水核验", now); err != nil {
		t.Fatal(err)
	}
	// 客服代办的卡也属于「我的储值卡」，但不属于自助购卡申请。
	staffCard := db.EsportsStoredValueCard.Create().SetCardNo("SVC-SCOPE-STAFF").
		SetCustomerID(mine.ID).SetSource(esportsstoredvaluecard.SourceStaff).
		SetPaymentChannel(esportsstoredvaluecard.PaymentChannelMerchantQr).
		SetPrincipalCents(5000).SetStatus(esportsstoredvaluecard.StatusActive).
		SetContractConfirmed(true).SetValidFrom(now).SetValidUntil(now.AddDate(0, 1, 0)).SaveX(ctx)
	db.EsportsStoredValueCard.Create().SetCardNo("SVC-SCOPE-OTHER").
		SetCustomerID(other.ID).SetPaymentChannel(esportsstoredvaluecard.PaymentChannelMerchantQr).
		SetPrincipalCents(8000).SetStatus(esportsstoredvaluecard.StatusActive).
		SetValidFrom(now).SetValidUntil(now.AddDate(0, 1, 0)).SaveX(ctx)

	items, err := listCustomerStoredValueCards(ctx, db, mine.ID, "", 100)
	if err != nil || len(items) != 2 {
		t.Fatalf("我的储值卡应只含本人两张：%v %+v", err, items)
	}
	for _, item := range items {
		if item.CustomerID != mine.ID {
			t.Fatalf("列表越权返回他人卡：%+v", item)
		}
	}
	if items, err = listCustomerStoredValueCards(ctx, db, mine.ID, "active", 100); err != nil || len(items) != 2 {
		t.Fatalf("按状态过滤应有 2 张可用卡：%v %d", err, len(items))
	}
	if items, err = listCustomerStoredValueCards(ctx, db, mine.ID, "pending_verification", 100); err != nil || len(items) != 0 {
		t.Fatalf("待核验卡应为空：%v %d", err, len(items))
	}
	if _, err = listCustomerStoredValueCards(ctx, db, mine.ID, "unknown", 100); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态应被拒，err=%v", err)
	}
	// 详情：本人可读且带流水，他人卡 403。
	view, transactions, err := getCustomerStoredValueCard(ctx, db, mine.ID, purchased.Card.ID, 100)
	if err != nil || view.BalanceCents != purchased.PayableCents || len(transactions) != 1 {
		t.Fatalf("本人卡详情不正确：%v %+v %d", err, view, len(transactions))
	}
	otherCard := db.EsportsStoredValueCard.Query().Where(
		esportsstoredvaluecard.CardNoEQ("SVC-SCOPE-OTHER")).OnlyX(ctx)
	if _, _, err = getCustomerStoredValueCard(ctx, db, mine.ID, otherCard.ID, 100); !errors.Is(err, errForbidden) {
		t.Fatalf("他人卡详情应 403，err=%v", err)
	}
	// 购卡申请详情：本人自助卡可查，他人自助卡与本人客服代办卡都 403。
	if purchase, err := getCustomerStoredValuePurchase(ctx, db, mine.ID, purchased.Card.ID); err != nil ||
		purchase.IntentNo != purchased.IntentNo || purchase.IntentStatus != "paid" {
		t.Fatalf("本人自助购卡申请应可查：%v %+v", err, purchase)
	}
	if _, err = getCustomerStoredValuePurchase(ctx, db, mine.ID, staffCard.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("客服代办卡不属于自助购卡申请，err=%v", err)
	}
	if _, err = getCustomerStoredValuePurchase(ctx, db, other.ID, purchased.Card.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("他人购卡申请应 403，err=%v", err)
	}
}
