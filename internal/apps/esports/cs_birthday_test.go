package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// 生日礼遇（§10.4）：客户档案可写生日（MM-DD），今日生日的归属客户计入工作台待办；非法格式被拒。
func TestCustomerBirthdayTodoAndProfile(t *testing.T) {
	db := openCSDB(t, "esports_cs_birthday")
	ctx := t.Context()
	now := time.Now()
	today := now.In(cstZone).Format("01-02")

	customer, err := db.EsportsCustomer.Create().SetDisplayName("生日客户").
		SetOwnerAdminID(1).SetBirthday(today).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 非今日生日 / 无生日客户不计入。
	if _, err := db.EsportsCustomer.Create().SetDisplayName("非生日").
		SetOwnerAdminID(1).SetBirthday("01-01").Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EsportsCustomer.Create().SetDisplayName("无生日").
		SetOwnerAdminID(1).Save(ctx); err != nil {
		t.Fatal(err)
	}
	// 归属他人的今日生日客户：非超管不计入、超管计入。
	if _, err := db.EsportsCustomer.Create().SetDisplayName("他人客户").
		SetOwnerAdminID(2).SetBirthday(today).Save(ctx); err != nil {
		t.Fatal(err)
	}

	todos, err := csWorkbenchTodos(ctx, db, 1, false, now)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, item := range todos {
		counts[item.Key] = item.Count
	}
	if counts["customer_birthday"] != 1 {
		t.Fatalf("非超管只应统计归属本人的今日生日客户，got=%d", counts["customer_birthday"])
	}
	superTodos, err := csWorkbenchTodos(ctx, db, 1, true, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range superTodos {
		if item.Key == "customer_birthday" && item.Count != 2 {
			t.Fatalf("超管应统计全部今日生日客户，got=%d", item.Count)
		}
	}

	// 档案更新：合法生日可写，非法格式被拒。
	if _, err := updateCustomerProfile(ctx, db, 1, false, customer.ID, customerProfileInput{
		Phone: "13800000000", Birthday: "12-31",
	}); err != nil {
		t.Fatalf("合法生日应可写入，err=%v", err)
	}
	for _, bad := range []string{"13-01", "00-10", "12-32", "abc", "12/31"} {
		if _, err := updateCustomerProfile(ctx, db, 1, false, customer.ID, customerProfileInput{
			Birthday: bad,
		}); !errors.Is(err, errInvalidInput) {
			t.Fatalf("非法生日 %q 应被拒，err=%v", bad, err)
		}
	}
}
