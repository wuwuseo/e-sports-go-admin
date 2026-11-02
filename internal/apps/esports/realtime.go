// 实时通知接入点：把登录 uid 解析成站内信地址（客户 / 陪玩 / 大厅房间），
// 并在订单进入抢单大厅时广播刷新事件。协议与连接由 cmf/ws、go-admin/internal/ws 承担。
package esports

import (
	"context"

	cmfws "github.com/wuwuseo/cmf/ws"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
	"go-admin/internal/ws"
)

const (
	// hallRoom 抢单大厅的共享房间地址。hub 只在建连时按 ticket 绑定地址，
	// 没有「连接后订阅房间」的帧，所以陪玩的 ticket 会额外绑定该房间，
	// 与个人地址共用同一条连接。
	hallRoom = "hall"
	// hallOrderCreatedEvent 大厅新单事件：只送信封（order_id），前端收到后重拉列表。
	hallOrderCreatedEvent = "hall.order_created"
)

func init() {
	if featureEnabled {
		ws.RegisterAddressProvider(esportsRealtimeAddresses)
	}
}

// esportsRealtimeAddresses 是注册给框架层的地址 provider，数据源为全局 ORM。
func esportsRealtimeAddresses(ctx context.Context, uid string) ([]string, error) {
	return realtimeAddresses(ctx, global.ORM, uid)
}

// realtimeAddresses 按 uid → active 用户 → 客户 / 陪玩档案解析地址：
// 客户得 customer:<id>，陪玩得 player:<id> 并额外绑定大厅房间；双身份两者都返回。
// 查不到档案返回空，由框架层退回只用自己的 uid。
func realtimeAddresses(ctx context.Context, db *ent.Client, uid string) ([]string, error) {
	if db == nil || uid == "" {
		return nil, nil
	}
	account, err := db.User.Query().Where(user.UIDEQ(uid), user.StatusEQ(user.StatusActive)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var addresses []string
	customer, err := db.EsportsCustomer.Query().Where(esportscustomer.UserIDEQ(account.ID)).Only(ctx)
	if err == nil {
		addresses = append(addresses, customerNotificationAddress(customer.ID))
	} else if !ent.IsNotFound(err) {
		return nil, err
	}

	player, err := db.EsportsPlayer.Query().Where(esportsplayer.UserIDEQ(account.ID)).Only(ctx)
	if err == nil {
		addresses = append(addresses, playerNotificationAddress(player.ID), hallRoom)
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	return addresses, nil
}

// broadcastHallOrder 通知大厅有订单进入：进程内 hub 未初始化或无人连接时静默丢弃。
// ponytail: 事务内触发，回滚会多推一条；前端防抖后重拉列表，结果以列表接口为准。
func broadcastHallOrder(orderID int) {
	if orderID < 1 {
		return
	}
	hub := ws.RealtimeHub()
	if hub == nil {
		return
	}
	hub.Broadcast(hallRoom, cmfws.Event{
		Type: hallOrderCreatedEvent,
		Data: map[string]any{"order_id": orderID},
	})
}
