package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
)

// P1-11 消费点（会员专属通道 → 派单排序）：看板待派单池 / 公共池按
// 「会员优先派单（第一档） > 专属通道（第二档） > 普通订单」分档置顶，并分别给出标记 ID。
func TestDispatchBoardMemberChannelSecondaryPriority(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_member_channel_board")
	defer db.Close()
	ctx := t.Context()

	seedMemberLevel(t, db, memberLevelInput{
		Code: "w3-priority", Name: "优先卡", GrowthThreshold: 0, DiscountBps: 10000,
		PriorityDispatch: true,
	})
	seedMemberLevel(t, db, memberLevelInput{
		Code: "w3-channel", Name: "通道卡", GrowthThreshold: 0, DiscountBps: 10000,
		DedicatedChannel: memberBoolPtr(true),
	})
	// 同时开通两个权益：只进第一档，不得重复计入第二档。
	seedMemberLevel(t, db, memberLevelInput{
		Code: "w3-both", Name: "双权益卡", GrowthThreshold: 0, DiscountBps: 10000,
		PriorityDispatch: true, DedicatedChannel: memberBoolPtr(true),
	})

	priorityCustomer := saveCustomerOrFail(t, db, 11)
	channelCustomer := saveCustomerOrFail(t, db, 11)
	bothCustomer := saveCustomerOrFail(t, db, 11)
	normalCustomer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(priorityCustomer.ID).SetMemberLevelCode("w3-priority").SaveX(ctx)
	db.EsportsCustomer.UpdateOneID(channelCustomer.ID).SetMemberLevelCode("w3-channel").SaveX(ctx)
	db.EsportsCustomer.UpdateOneID(bothCustomer.ID).SetMemberLevelCode("w3-both").SaveX(ctx)

	seed := func(orderNo string, customerID int) *ent.EsportsOrder {
		return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
			SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
			SetPromisedServiceUnits(1).SetRequiredSeats(1).
			SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
			SetPaymentStatus(esportsorder.PaymentStatusPaid).
			SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)
	}
	// 建单顺序即 ID 升序：按 ID 倒序本应是 both > normal > channel > priority。
	priorityOrder := seed("W3-MB-PRIO", priorityCustomer.ID)
	channelOrder := seed("W3-MB-CHAN", channelCustomer.ID)
	normalOrder := seed("W3-MB-NORM", normalCustomer.ID)
	bothOrder := seed("W3-MB-BOTH", bothCustomer.ID)

	board, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.PendingDispatch) != 4 {
		t.Fatalf("待派单池应有 4 单，got=%d", len(board.PendingDispatch))
	}
	// 第一档（会员优先派单，含双权益）在前，第二档（仅专属通道）居中，普通订单最后。
	wantOrder := []int{bothOrder.ID, priorityOrder.ID, channelOrder.ID, normalOrder.ID}
	for index, want := range wantOrder {
		if got := board.PendingDispatch[index].ID; got != want {
			t.Fatalf("第 %d 位订单应为 %d（顺序 priority > dedicated_channel > 普通），got=%d：%+v",
				index+1, want, got, orderIDsOf(board.PendingDispatch))
		}
	}
	if got, want := board.MemberPriorityOrderIDs, []int{bothOrder.ID, priorityOrder.ID}; !sameIntSet(got, want) {
		t.Fatalf("会员优先派单标记不正确：got=%v want=%v", got, want)
	}
	if got, want := board.MemberDedicatedChannelOrderIDs, []int{channelOrder.ID}; !sameIntSet(got, want) {
		t.Fatalf("专属通道标记只应含因专属通道置顶的订单：got=%v want=%v", got, want)
	}
}

// 回归：客户无会员权益（未定级 / 等级未开两个权益）时，看板顺序与标记必须与接入前完全一致。
func TestDispatchBoardMemberChannelNoPrivilegeKeepsOrder(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_member_channel_plain")
	defer db.Close()
	ctx := t.Context()

	// 等级存在但两个派单权益都关闭：不得产生任何置顶与标记。
	seedMemberLevel(t, db, memberLevelInput{
		Code: "w3-plain", Name: "普通卡", GrowthThreshold: 0, DiscountBps: 10000,
	})
	plain := saveCustomerOrFail(t, db, 11)
	plainCustomer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(plainCustomer.ID).SetMemberLevelCode("w3-plain").SaveX(ctx)

	seed := func(orderNo string, customerID int) *ent.EsportsOrder {
		return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
			SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
			SetPromisedServiceUnits(1).SetRequiredSeats(1).
			SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
			SetPaymentStatus(esportsorder.PaymentStatusPaid).
			SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)
	}
	plainOrder := seed("W3-MB-PLAIN", plainCustomer.ID)
	plainNormalOrder := seed("W3-MB-PLAIN-NORM", plain.ID)

	board, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	// 默认按 ID 倒序：后建的普通订单在前，未被任何权益置顶打乱。
	wantOrder := []int{plainNormalOrder.ID, plainOrder.ID}
	if got := orderIDsOf(board.PendingDispatch); !sameIntSet(got, wantOrder) {
		t.Fatalf("无会员权益时顺序应与接入前一致：got=%v want=%v", got, wantOrder)
	}
	if len(board.MemberPriorityOrderIDs) != 0 || len(board.MemberDedicatedChannelOrderIDs) != 0 {
		t.Fatalf("无会员权益时不得产生置顶标记：priority=%v channel=%v",
			board.MemberPriorityOrderIDs, board.MemberDedicatedChannelOrderIDs)
	}
}

// orderIDsOf 提取订单 ID 序列，便于断言排序。
func orderIDsOf(orders []*ent.EsportsOrder) []int {
	ids := make([]int, 0, len(orders))
	for _, order := range orders {
		ids = append(ids, order.ID)
	}
	return ids
}

// sameIntSet 判断两个 ID 序列是否同序且等长（本文件的标记都要求保持顺序）。
func sameIntSet(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
