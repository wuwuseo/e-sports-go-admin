package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	entadmin "go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportscspresence"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/global"
)

// 客服在线状态与「离线 / 积压 → 自动转公共池」。
//
// 在线状态口径（可验收断言）：
//   - 数据源优先级：EsportsCsPresence（presence 表）> Admin.last_login / 最近一次管理员订单操作；
//   - 最近活跃时间 last_active_at = max(presence.last_active_at, Admin.last_login, 最近一次
//     actor_type=admin 的订单事件时间)；从未活跃 = 离线；
//   - presence=offline 直接判离线（权威），presence=online/busy 仍需在 cs_offline_timeout_minutes
//     内活跃才算在线；无 presence 档案时仅按「最近活跃时间 + 阈值」推导。
const (
	// csOfflineTimeoutMinutesKey 客服离线判定时长（分钟，裸键 cs_offline_timeout_minutes）。<1 或非法回落 15。
	csOfflineTimeoutMinutesKey     = "app.esports.cs_offline_timeout_minutes"
	defaultCsOfflineTimeoutMinutes = 15
	// csBacklogAutoTransferMinutesKey 积压自动转池时长（分钟，裸键 cs_backlog_auto_transfer_minutes）。0 = 关闭。
	csBacklogAutoTransferMinutesKey = "app.esports.cs_backlog_auto_transfer_minutes"
	// csBacklogOrderThresholdKey 积压自动转池单量阈值（单，裸键 cs_backlog_order_threshold）。0 = 不按单量。
	csBacklogOrderThresholdKey = "app.esports.cs_backlog_order_threshold"
)

// csOfflineTimeoutMinutes 读客服离线判定时长（分钟）；缺失 / <1 / >1440 回落 15。
func csOfflineTimeoutMinutes(ctx context.Context, db *ent.Client) int {
	value := csConfigInt(ctx, db, csOfflineTimeoutMinutesKey)
	if value < 1 || value > 1440 {
		return defaultCsOfflineTimeoutMinutes
	}
	return value
}

// csConfigInt 读一条整数系统配置；缺失或非法返回 0（调用方决定 0 的语义）。
func csConfigInt(ctx context.Context, db *ent.Client, key string) int {
	raw := assessmentConfigString(ctx, db, key, "")
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 || value > 1_000_000 {
		return 0
	}
	return value
}

// csAdminActivity 是一名客服的在线摘要。
type csAdminActivity struct {
	Presence     string
	Online       bool
	LastActiveAt *time.Time
}

// csAdminActivities 批量计算客服在线状态与最近活跃时间。
// 看板与「离线 / 积压自动转池」任务共用同一口径，避免两处判定漂移。
func csAdminActivities(ctx context.Context, db *ent.Client, adminIDs []int, now time.Time) (map[int]csAdminActivity, error) {
	out := make(map[int]csAdminActivity, len(adminIDs))
	if len(adminIDs) == 0 {
		return out, nil
	}
	timeout := csOfflineTimeoutMinutes(ctx, db)
	accounts, err := db.Admin.Query().Where(entadmin.IDIn(adminIDs...)).All(ctx)
	if err != nil {
		return nil, err
	}
	lastLogin := make(map[int]time.Time, len(accounts))
	for _, account := range accounts {
		if !account.LastLogin.IsZero() {
			lastLogin[account.ID] = account.LastLogin.UTC()
		}
	}
	// 最近一次管理员订单操作：按事件倒序取最近若干条，解析 actor_id（管理员 ID 字符串）。
	events, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.ActorTypeEQ("admin"),
	).Order(ent.Desc(esportsorderevent.FieldID)).Limit(2000).All(ctx)
	if err != nil {
		return nil, err
	}
	lastEvent := make(map[int]time.Time)
	for _, event := range events {
		id, convErr := strconv.Atoi(strings.TrimSpace(event.ActorID))
		if convErr != nil || id < 1 {
			continue
		}
		if prior, ok := lastEvent[id]; !ok || event.CreatedAt.After(prior) {
			lastEvent[id] = event.CreatedAt.UTC()
		}
	}
	presences, err := db.EsportsCsPresence.Query().Where(
		esportscspresence.AdminIDIn(adminIDs...),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	byAdmin := make(map[int]*ent.EsportsCsPresence, len(presences))
	for _, row := range presences {
		byAdmin[row.AdminID] = row
	}
	cutoff := now.UTC().Add(-time.Duration(timeout) * time.Minute)
	for _, adminID := range adminIDs {
		var last *time.Time
		consider := func(value time.Time) {
			if value.IsZero() {
				return
			}
			utc := value.UTC()
			if last == nil || utc.After(*last) {
				last = &utc
			}
		}
		consider(lastLogin[adminID])
		consider(lastEvent[adminID])
		row := byAdmin[adminID]
		presence := "offline"
		if row != nil {
			if row.LastActiveAt != nil {
				consider(*row.LastActiveAt)
			}
			presence = row.Presence.String()
		}
		online := false
		switch {
		case row != nil && row.Presence == esportscspresence.PresenceOffline:
			// presence 表是权威：显式下线直接判离线。
			online = false
		case last == nil:
			// 从未活跃的客服视为离线（含无 presence 档案的存量账号）。
			online = false
		default:
			online = last.After(cutoff)
		}
		if row == nil && online {
			presence = "online"
		}
		out[adminID] = csAdminActivity{Presence: presence, Online: online, LastActiveAt: last}
	}
	return out, nil
}

// csPresenceStates 客服可写入的在线状态：与 schema 枚举一致。
var csPresenceStates = map[string]esportscspresence.Presence{
	"online":  esportscspresence.PresenceOnline,
	"busy":    esportscspresence.PresenceBusy,
	"offline": esportscspresence.PresenceOffline,
}

// setCsPresence 客服在线状态写入口（，POST /admin/cs/presence）。
//
// 口径（可验收断言）：
//   - 状态只接受 online / busy / offline，其他值一律参数错误（不静默落 offline）；
//   - admin_id 未指定时写调用者本人；指定他人时调用方必须是超级管理员（避免互相改状态）；
//   - 同一客服只有一条 presence 档案（admin_id 唯一索引兜底），重复写为更新；
//   - online / busy 更新 last_active_at 与 last_online_at（视为一次活跃打点）；
//     offline 只写 last_offline_at，保留 last_active_at 作为「最后活跃时间」证据；
//   - admin_id 不存在则报错，不允许为幽灵账号建档（看板与转池任务都会按 admin 表核对）。
func setCsPresence(ctx context.Context, db *ent.Client, adminID, actorAdminID int, isSuper bool,
	presence string, now time.Time) (*ent.EsportsCsPresence, error) {
	target, ok := csPresenceStates[strings.TrimSpace(presence)]
	if !ok {
		return nil, fmt.Errorf("%w：在线状态只能是 online / busy / offline", errInvalidInput)
	}
	if adminID < 1 {
		return nil, errInvalidInput
	}
	if adminID != actorAdminID && !isSuper {
		return nil, errForbidden
	}
	if _, err := db.Admin.Get(ctx, adminID); err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("%w：客服账号不存在", errInvalidInput)
		}
		return nil, err
	}
	now = now.UTC()
	existing, err := db.EsportsCsPresence.Query().Where(esportscspresence.AdminIDEQ(adminID)).Only(ctx)
	if ent.IsNotFound(err) {
		create := db.EsportsCsPresence.Create().SetAdminID(adminID).SetPresence(target)
		if target == esportscspresence.PresenceOffline {
			create.SetLastOfflineAt(now)
		} else {
			create.SetLastActiveAt(now).SetLastOnlineAt(now)
		}
		row, createErr := create.Save(ctx)
		if createErr == nil {
			return row, nil
		}
		if !ent.IsConstraintError(createErr) {
			return nil, createErr
		}
		// 并发下唯一索引兜底：回落到更新分支。
		if existing, err = db.EsportsCsPresence.Query().
			Where(esportscspresence.AdminIDEQ(adminID)).Only(ctx); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	update := db.EsportsCsPresence.UpdateOneID(existing.ID).SetPresence(target).AddVersion(1)
	if target == esportscspresence.PresenceOffline {
		update.SetLastOfflineAt(now)
	} else {
		update.SetLastActiveAt(now).SetLastOnlineAt(now)
	}
	return update.Save(ctx)
}

// adminSetCsPresenceHandler 客服在线打点。
// 请求体：{"presence":"online|busy|offline"}，可选 {"admin_id":N}（仅超管可代他人打点）。
func adminSetCsPresenceHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		AdminID  int    `json:"admin_id"`
		Presence string `json:"presence"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	target := in.AdminID
	if target < 1 {
		target = adminID
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	row, err := setCsPresence(c.Context(), global.ORM, target, adminID, isSuper, in.Presence, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, row)
}

// adminListCsPresencesHandler 客服在线清单：与排单看板顶部共用同一套在线判定
// （presence 表权威，无档案时回落「最近登录 / 最近一次管理员订单操作」）。
func adminListCsPresencesHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	admins, err := loadDispatchBoardAdmins(c.Context(), global.ORM, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	timeout := csOfflineTimeoutMinutes(c.Context(), global.ORM)
	return reply(c, fiber.Map{"offline_timeout_minutes": timeout, "admins": admins})
}

// releaseOrderToPublicPool 把「待派单」订单释放到公共池：
// 仅负责客服或超管可操作；清空 responsible_admin_id 后由值班客服认领。
// 已接单/服务中的订单不允许直接释放，必须先撤单/转单，避免绕过履约责任。
func releaseOrderToPublicPool(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	orderID int, reason string, now time.Time) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if orderID < 1 || adminID < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := loadStrictOwnedOrder(ctx, tx.Client(), adminID, isSuper, orderID)
	if err != nil {
		return nil, err
	}
	// ：存单冻结期间禁止一切写操作，清空归属客服属于写操作。
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.ResponsibleAdminID == nil {
		return nil, errConflict
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		return nil, errConflict
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
	).ClearResponsibleAdminID().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.released_to_public_pool").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"reason": reason, "from_admin_id": *order.ResponsibleAdminID}).
		Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, order.ID)
}

func releaseOrderToPublicPoolHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	order, err := releaseOrderToPublicPool(c.Context(), global.ORM, adminID, isSuper, orderID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// autoTransferBacklogOrders 客服离线 / 订单积压自动转公共池，
// 由定时任务周期收敛（任务名 esports_auto_transfer_backlog_orders，注册与 CronJob 由主控加）。
//
// 开关口径（可验收断言）：
//   - app.esports.cs_backlog_auto_transfer_minutes 与 app.esports.cs_backlog_order_threshold
//     同时为 0（默认）→ 任务整体关闭，返回 0，不做任何写入；
//   - 触发条件 A（客服离线超时）：该订单的负责客服已离线，且最近活跃时间早于
//     cs_backlog_auto_transfer_minutes（从未活跃的客服按「离线很久」处理）；
//   - 触发条件 B（待派单积压）：该客服名下待派单订单数已**超过** cs_backlog_order_threshold
//     （不要求离线，积压本身就是需要把订单交回公共池的信号）。
//
// 释放动作与人工转池一致：清空 responsible_admin_id、写 order.released_to_public_pool 事件
// （actor=system），并写 dispatch.auto_pool_release 定向通知原负责客服。重复运行幂等。
func autoTransferBacklogOrders(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	stallMinutes := csConfigInt(ctx, db, csBacklogAutoTransferMinutesKey)
	threshold := csConfigInt(ctx, db, csBacklogOrderThresholdKey)
	if stallMinutes <= 0 && threshold <= 0 {
		return 0, nil
	}
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	orders, err := db.EsportsOrder.Query().Where(
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
		esportsorder.ResponsibleAdminIDNotNil(),
	).Order(ent.Asc(esportsorder.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(orders) == 0 {
		return 0, nil
	}
	// 在册待派单量：单量阈值按该客服名下全部待派单订单计数（不限于本轮 limit 内）。
	backlog := map[int]int{}
	adminIDs := make([]int, 0, len(orders))
	seenAdmin := map[int]bool{}
	for _, order := range orders {
		if order.ResponsibleAdminID == nil {
			continue
		}
		backlog[*order.ResponsibleAdminID]++
		if !seenAdmin[*order.ResponsibleAdminID] {
			seenAdmin[*order.ResponsibleAdminID] = true
			adminIDs = append(adminIDs, *order.ResponsibleAdminID)
		}
	}
	activities, err := csAdminActivities(ctx, db, adminIDs, now)
	if err != nil {
		return 0, err
	}
	stallBefore := now.UTC().Add(-time.Duration(stallMinutes) * time.Minute)
	total := 0
	for _, order := range orders {
		if order.ResponsibleAdminID == nil {
			continue
		}
		adminID := *order.ResponsibleAdminID
		activity := activities[adminID]
		offlineLongEnough := stallMinutes > 0 && !activity.Online &&
			(activity.LastActiveAt == nil || !activity.LastActiveAt.After(stallBefore))
		overloaded := threshold > 0 && backlog[adminID] > threshold
		if !offlineLongEnough && !overloaded {
			continue
		}
		reason := "负责客服离线超时，自动转公共池"
		if !offlineLongEnough {
			reason = "负责客服待派单积压超阈值，自动转公共池"
		}
		released, err := autoReleaseOrderToPublicPool(ctx, db, order.ID, reason, now)
		if err != nil {
			if err == errConflict {
				continue
			}
			return total, err
		}
		if released {
			total++
		}
	}
	return total, nil
}

// autoReleaseOrderToPublicPool 系统侧释放单张订单到公共池：清空负责客服、写释放事件与定向通知外盒。
// 与人工入口 releaseOrderToPublicPool 的区别只在操作者（system 而非具体客服）与通知事件类型。
func autoReleaseOrderToPublicPool(ctx context.Context, db *ent.Client, orderID int, reason string, now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return false, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return false, err
	}
	if order.ResponsibleAdminID == nil ||
		order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		return false, errConflict
	}
	fromAdminID := *order.ResponsibleAdminID
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingDispatch),
	).ClearResponsibleAdminID().AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.released_to_public_pool").
		SetActorType("system").SetActorID("cs_backlog").
		SetDetails(map[string]interface{}{"reason": reason, "from_admin_id": fromAdminID}).
		Save(ctx); err != nil {
		return false, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("dispatch.auto_pool_release:" + order.OrderNo).
		SetEventType("dispatch.auto_pool_release").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "admin_id": fromAdminID, "reason": reason,
		}).Save(ctx); err != nil {
		if !ent.IsConstraintError(err) {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
