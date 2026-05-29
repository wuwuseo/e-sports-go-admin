package handler

import (
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/sse"
)

// IssueSSETicket 颁发 SSE 临时凭证
// @Summary 颁发 SSE 临时凭证
// @Description 鉴权后生成 5 分钟内有效的一次性 ticket，用于建立 SSE 连接
// @Tags 站内消息
// @Produce json
// @Success 200 {object} utils.Response{data=map[string]any}
// @Router /api/v1/admin/auth/sse/ticket [post]
func IssueSSETicket(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	claims := jwt.GetJWTUserData(c)
	uid, _ := claims["id"].(string)
	if uid == "" {
		return response.Fail(global.ResponseUnauthorized, "缺少用户身份").ToJSON(c)
	}

	store, ok := c.Locals("sse.TicketStore").(*sse.TicketStore)
	if !ok || store == nil {
		return response.Fail(global.ResponseInternalServerError, "SSE 凭证服务未初始化").ToJSON(c)
	}

	ticket, exp, err := sse.IssueTicket(c, store, uid)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}
	return response.Success(fiber.Map{"ticket": ticket, "expire_at": exp}).ToJSON(c)
}

// SSEConnect 建立 SSE 长连接
// @Summary 建立 SSE 长连接
// @Description 通过 ticket 鉴权后建立 EventSource 长连接，接收站内消息推送
// @Tags 站内消息
// @Param ticket       query string true  "SSE ticket"
// @Param lastEventId  query int    false "客户端最后一次收到的事件 ID"
// @Produce text/event-stream
// @Router /api/v1/admin/sse/connect [get]
func SSEConnect(c fiber.Ctx) error {
	hub, _ := c.Locals("sse.Hub").(*sse.Hub)
	store, _ := c.Locals("sse.TicketStore").(*sse.TicketStore)
	msgSvc, _ := c.Locals("service.MessageService").(service.MessageServiceInterface)

	cfg := sse.StreamConfig{
		Hub:         hub,
		TicketStore: store,
	}
	if ms, ok := msgSvc.(*service.MessageService); ok && ms != nil {
		cfg.MissedFetcher = ms.BuildMissedFetcher()
	}
	return sse.HandleConnect(c, cfg)
}
