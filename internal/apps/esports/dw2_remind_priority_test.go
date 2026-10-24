package esports

import (
	"errors"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
)

// ：催单优先级必须落订单字段、在看板响应中可见，并可按键排序。
//   - POST /admin/orders/:id/remind 后订单详情带 remind_priority_level / remind_note / reminded_at；
//   - GET /admin/dispatch/board 响应含该字段；
//   - GET /admin/dispatch/board?sort=remind_priority 按优先级降序（同档按 ID 倒序）；
//   - 非法 sort 值必须明确报错，不得静默忽略。
func TestDw2RemindPriorityPersistedAndBoardSortable(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_remind_priority")
	defer db.Close()
	ctx := t.Context()
	admin := seedAdminAccount(t, db, "dw2remind")

	low := seedPaidOrder(t, db, "DW2-RM-LOW", 1)
	db.EsportsOrder.UpdateOneID(low.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	high := seedPaidOrder(t, db, "DW2-RM-HIGH", 1)
	db.EsportsOrder.UpdateOneID(high.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)

	// 越界档位仍必须拒绝（沿用既有校验）。
	if err := remindOrder(ctx, db, low.ID, admin.ID, 9, "越界"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("越界档位应报参数错误：%v", err)
	}
	if err := remindOrder(ctx, db, low.ID, admin.ID, 1, "普通催"); err != nil {
		t.Fatalf("催单应成功：%v", err)
	}
	// 同单再次催单：最新一次为准（档位、备注、时间一起覆盖，可检索）。
	if err := remindOrder(ctx, db, high.ID, admin.ID, 3, "客户等急了"); err != nil {
		t.Fatalf("催单应成功：%v", err)
	}
	stored := db.EsportsOrder.GetX(ctx, high.ID)
	if stored.RemindPriorityLevel != 3 || stored.RemindNote != "客户等急了" || stored.RemindedAt == nil {
		t.Fatalf("催单优先级必须落订单字段：%+v", stored)
	}
	if db.EsportsOrder.GetX(ctx, low.ID).RemindPriorityLevel != 1 {
		t.Fatal("低档催单未落库")
	}

	// 默认看板：字段随响应下发（ent 实体直接序列化）。
	board, err := loadDispatchBoard(ctx, db, admin.ID, true)
	if err != nil {
		t.Fatalf("看板应成功：%v", err)
	}
	if len(board.PendingDispatch) != 2 {
		t.Fatalf("待派单桶应有 2 单，got=%d", len(board.PendingDispatch))
	}
	found := false
	for _, item := range board.PendingDispatch {
		if item.ID == high.ID && item.RemindPriorityLevel == 3 {
			found = true
		}
	}
	if !found {
		t.Fatal("看板待派单桶必须带催单优先级字段")
	}

	// 按催单优先级排序。
	sorted, err := loadDispatchBoardSorted(ctx, db, admin.ID, true, dispatchBoardSortRemindPriority)
	if err != nil {
		t.Fatalf("按催单优先级排序应成功：%v", err)
	}
	if len(sorted.PendingDispatch) != 2 || sorted.PendingDispatch[0].ID != high.ID {
		t.Fatalf("按催单优先级排序不正确：%+v", dw2OrderIDs(sorted.PendingDispatch))
	}
	if _, err = loadDispatchBoardSorted(ctx, db, admin.ID, true, "bogus"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法 sort 必须明确报错：%v", err)
	}

	// 非超管看板：仍只看到自己名下（排序参数不影响可见性）。
	other := seedAdminAccount(t, db, "dw2remindother")
	scoped, err := loadDispatchBoardSorted(ctx, db, other.ID, false, dispatchBoardSortRemindPriority)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.PendingDispatch) != 0 {
		t.Fatalf("非超管不应看到他人订单：%d", len(scoped.PendingDispatch))
	}
	if status := db.EsportsOrder.GetX(ctx, high.ID).FulfillmentStatus; status != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("催单不得改变履约状态：%v", status)
	}
}

// dw2OrderIDs 便于失败信息定位。
func dw2OrderIDs(orders []*ent.EsportsOrder) []int {
	ids := make([]int, 0, len(orders))
	for _, item := range orders {
		ids = append(ids, item.ID)
	}
	return ids
}
