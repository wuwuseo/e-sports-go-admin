package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

// 收款渠道枚举必须与 externalPaymentChannelValues 完全一致。
// 客户侧收款方式列表按该切片渲染、管理端下拉也按它给选项，schema enum 少一个值就会
// 出现「渠道看得见、账户永远配不上」（2026-11 微信商家码 / 支付宝商家码就是这种情况）。
func TestPaymentAccountSupportsEveryExternalChannel(t *testing.T) {
	db := enttest.Open(t, "sqlite3",
		"file:esports_payment_channel_enum?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	seedPaymentSubjectWhitelist(t, db)

	for _, channel := range externalPaymentChannelValues {
		in := paymentAccountInput{Channel: channel, SubjectName: "示例工作室有限公司"}
		if channel == "corporate_transfer" {
			// 对公转账必须有银行账号。
			in.AccountNo = "6222020200000000"
			in.BankName = "示例银行"
		} else {
			// 码类渠道（含细分商家码）必须有可展示的收款码或商户号。
			in.QrURL = "https://example.com/qr-" + channel + ".png"
		}
		account, err := createPaymentAccount(ctx, db, in, 11, now)
		if err != nil {
			t.Fatalf("渠道 %s 应能创建收款账户：%v", channel, err)
		}
		if got := account.Channel.String(); got != channel {
			t.Fatalf("渠道落库不一致：want %s got %s", channel, got)
		}
	}
}

// 细分商家码缺收款码与商户号时应被拒（与商户码 / 企微收款同口径）。
func TestPaymentAccountRejectsEmptyFixedMerchantQR(t *testing.T) {
	db := enttest.Open(t, "sqlite3",
		"file:esports_payment_channel_empty?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	seedPaymentSubjectWhitelist(t, db)

	for _, channel := range []string{"wechat_merchant_qr", "alipay_merchant_qr"} {
		if _, err := createPaymentAccount(ctx, db, paymentAccountInput{
			Channel: channel, SubjectName: "示例工作室有限公司",
		}, 11, time.Now().UTC()); err == nil {
			t.Fatalf("渠道 %s 无收款码、无商户号应被拒", channel)
		}
	}
}
