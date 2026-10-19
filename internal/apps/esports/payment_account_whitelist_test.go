package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportspaymentaccount"
)

// 收款主体白名单 fail-closed（§4.3-07）：未配置时任何主体都不得提交或被批准启用，
// 避免「默认空名单 = 任意主体名都能经财务审核变成收款账户」。
func TestPaymentSubjectWhitelistFailsClosed(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_payment_subject_whitelist_fail_closed")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	// 1) 未配置白名单：提交即被拒，且提示里带配置键。
	_, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "wechat_work", SubjectName: "示例工作室有限公司", QrURL: "https://example.com/a.png",
	}, 11, now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), paymentSubjectWhitelistKey) {
		t.Fatalf("未配置白名单时提交应被拒并提示配置键，err=%v", err)
	}

	// 2) 登记白名单后：名单内主体可提交，等待财务审核。
	poSeedConfig(t, db, paymentSubjectWhitelistKey, "示例工作室有限公司")
	item, err := createPaymentAccount(ctx, db, paymentAccountInput{
		Channel: "wechat_work", SubjectName: "示例工作室有限公司", QrURL: "https://example.com/ok.png",
	}, 11, now)
	if err != nil {
		t.Fatalf("白名单内主体应可提交：%v", err)
	}
	if item.Status != esportspaymentaccount.StatusPendingReview {
		t.Fatalf("提交后应待财务审核：%+v", item)
	}

	// 3) 提交之后白名单被清空：批准必须被拒（不能靠旧资料过审）。
	poSeedConfig(t, db, paymentSubjectWhitelistKey, "")
	if _, err = reviewPaymentAccount(ctx, db, item.ID, 12, true, "", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("白名单被清空后批准应收款账户应被拒，err=%v", err)
	}
	if recheck := db.EsportsPaymentAccount.GetX(ctx, item.ID); recheck.Status != esportspaymentaccount.StatusPendingReview {
		t.Fatalf("批准被拒后账户应仍待审核：%+v", recheck)
	}

	// 4) 重新登记白名单后可正常批准启用。
	poSeedConfig(t, db, paymentSubjectWhitelistKey, "示例工作室有限公司")
	approved, err := reviewPaymentAccount(ctx, db, item.ID, 12, true, "", now)
	if err != nil {
		t.Fatalf("白名单恢复后批准应成功：%v", err)
	}
	if approved.Status != esportspaymentaccount.StatusActive {
		t.Fatalf("批准后账户应为启用状态：%+v", approved)
	}
}
