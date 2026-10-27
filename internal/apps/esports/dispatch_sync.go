package esports

import (
	"github.com/gofiber/fiber/v3"
	cmfapp "github.com/wuwuseo/cmf/app"
	"github.com/wuwuseo/cmf/sse"

	"go-admin/internal/global"
)

// dispatchChangedEvent 派单看板变更事件名，管理端 sse.service.ts 按同名事件订阅。
const dispatchChangedEvent = "dispatch_changed"

// notifyDispatchChanged 广播一次看板变更：HTTP 写操作与定时任务共用。
// Hub 未初始化（测试环境或 SSE 未启用）时静默跳过。
func notifyDispatchChanged(data map[string]any) {
	if global.Hub == nil {
		return
	}
	global.Hub.Broadcast(sse.Event{Event: dispatchChangedEvent, Data: data})
}

// withDispatchBroadcast 让功能集内的写路由在成功后广播看板变更。
//
// ponytail: 只广播「有变化」的轻量信号，不携带业务快照——看板口径复杂（多表聚合、
// 数据权限脱敏），增量下发容易与列表接口不一致；出现高频场景再改为按路由携带变更实体。
func withDispatchBroadcast(next fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		if err := next(c); err != nil {
			return err
		}
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return nil
		}
		if status := c.Response().StatusCode(); status >= 200 && status < 300 {
			notifyDispatchChanged(map[string]any{"method": c.Method(), "path": c.Route().Path})
		}
		return nil
	}
}

// syncedRoutes 给功能集声明的全部路由统一套上看板广播。
func syncedRoutes(routes []cmfapp.Route) []cmfapp.Route {
	for i := range routes {
		routes[i].Handler = withDispatchBroadcast(routes[i].Handler)
	}
	return routes
}
