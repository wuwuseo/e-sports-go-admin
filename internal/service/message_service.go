package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/wuwuseo/cmf/sse"

	"go-admin/internal/ent"
	"go-admin/internal/ent/message"
	"go-admin/internal/global"
	"go-admin/internal/utils"
)

// 站内消息领域错误
var (
	ErrMessageNotFound   = errors.New("消息不存在")
	ErrMessageForbidden  = errors.New("无权操作该消息")
	ErrMessageInvalidReq = errors.New("非法的消息请求参数")
)

// 消息类型与级别常量
const (
	MessageTypeSystem   = "system"
	MessageTypeBusiness = "business"
	MessageTypeTask     = "task"

	MessageLevelInfo    = "info"
	MessageLevelWarning = "warning"
	MessageLevelError   = "error"

	// 全员广播时 user_uid 字段的值
	MessageBroadcastUID = ""

	// SSE 推送的事件名
	SSEEventMessage = "message"
)

// ========== 请求/响应结构体 ==========

// PublishMessage 业务侧调用 Publisher 时的消息载荷
type PublishMessage struct {
	Type    string                 `json:"type"    validate:"required,oneof=system business task"`
	Level   string                 `json:"level"   validate:"omitempty,oneof=info warning error"`
	Title   string                 `json:"title"   validate:"required,max=200"`
	Content string                 `json:"content" validate:"required"`
	Extra   map[string]any         `json:"extra,omitempty"`
}

// CreateMessageRequest 通过接口创建消息（管理端可用）
type CreateMessageRequest struct {
	UserUID string         `json:"user_uid"` // 为空表示广播
	Type    string         `json:"type"    validate:"required,oneof=system business task"`
	Level   string         `json:"level"   validate:"omitempty,oneof=info warning error"`
	Title   string         `json:"title"   validate:"required,max=200"`
	Content string         `json:"content" validate:"required"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// ListMessageRequest 列表查询请求
type ListMessageRequest struct {
	PaginationRequest
	Type       string `query:"type"        json:"type"`
	ReadStatus *int   `query:"read_status" json:"read_status"` // 0=未读 1=已读 nil=全部
}

// MessageResponse 单条消息响应
type MessageResponse struct {
	ID        int            `json:"id"`
	UserUID   string         `json:"user_uid"`
	Type      string         `json:"type"`
	Level     string         `json:"level"`
	Title     string         `json:"title"`
	Content   string         `json:"content"`
	Extra     map[string]any `json:"extra,omitempty"`
	ReadAt    *time.Time     `json:"read_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// UnreadCountResponse 未读数响应
type UnreadCountResponse struct {
	Total    int `json:"total"`
	System   int `json:"system"`
	Business int `json:"business"`
	Task     int `json:"task"`
}

// MarkReadRequest 标记已读请求
type MarkReadRequest struct {
	IDs []int `json:"ids" validate:"required,min=1"`
}

// ========== 接口定义 ==========

// MessageServiceInterface 站内消息服务接口
type MessageServiceInterface interface {
	Create(ctx context.Context, req *CreateMessageRequest) (*MessageResponse, error)
	List(ctx context.Context, uid string, req *ListMessageRequest) (*ListResponse[*MessageResponse], error)
	MarkRead(ctx context.Context, uid string, ids []int) (int, error)
	MarkAllRead(ctx context.Context, uid string) (int, error)
	UnreadCount(ctx context.Context, uid string) (*UnreadCountResponse, error)
	Delete(ctx context.Context, uid string, id int) error
	ListSinceID(ctx context.Context, uid string, lastID int, limit int) ([]*MessageResponse, error)

	// Publisher 业务侧 API
	PublishToUser(ctx context.Context, uid string, msg PublishMessage) (*MessageResponse, error)
	Broadcast(ctx context.Context, msg PublishMessage) (*MessageResponse, error)
}

// ========== 实现 ==========

// MessageService 站内消息服务实现
type MessageService struct {
	Client *ent.Client
	Hub    *sse.Hub // 可为 nil，仅落库不推送
}

// NewMessageService 构造函数
func NewMessageService(hub *sse.Hub) *MessageService {
	return &MessageService{
		Client: global.ORM,
		Hub:    hub,
	}
}

// 编译期接口实现断言
var _ MessageServiceInterface = (*MessageService)(nil)

// Create 通过管理端接口创建一条消息（同时落库 + 推送）
func (s *MessageService) Create(ctx context.Context, req *CreateMessageRequest) (*MessageResponse, error) {
	if req == nil {
		return nil, ErrMessageInvalidReq
	}
	level := req.Level
	if level == "" {
		level = MessageLevelInfo
	}
	creator := s.Client.Message.Create().
		SetUserUID(req.UserUID).
		SetType(req.Type).
		SetLevel(level).
		SetTitle(req.Title).
		SetContent(req.Content)
	if req.Extra != nil {
		creator.SetExtra(req.Extra)
	}
	entity, err := creator.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建消息失败: %w", err)
	}
	resp := s.toResponse(entity)
	s.push(req.UserUID, resp)
	return resp, nil
}

// List 分页查询当前用户的消息列表（含广播消息）
func (s *MessageService) List(ctx context.Context, uid string, req *ListMessageRequest) (*ListResponse[*MessageResponse], error) {
	if uid == "" {
		return nil, ErrMessageForbidden
	}
	page, pageSize := utils.NormalizePagination(req.Page, req.PageSize)

	q := s.Client.Message.Query().Where(
		message.Or(
			message.UserUIDEQ(uid),
			message.UserUIDEQ(MessageBroadcastUID),
		),
	)
	if req.Type != "" {
		q = q.Where(message.TypeEQ(req.Type))
	}
	if req.ReadStatus != nil {
		switch *req.ReadStatus {
		case 0:
			q = q.Where(message.ReadAtIsNil())
		case 1:
			q = q.Where(message.ReadAtNotNil())
		}
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计消息总数失败: %w", err)
	}
	entities, err := q.Order(ent.Desc(message.FieldID)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询消息列表失败: %w", err)
	}
	return BuildList(entities, total, page, s.toResponse), nil
}

// MarkRead 将指定消息标记为已读，返回影响行数（仅自己的消息）
func (s *MessageService) MarkRead(ctx context.Context, uid string, ids []int) (int, error) {
	if uid == "" {
		return 0, ErrMessageForbidden
	}
	if len(ids) == 0 {
		return 0, nil
	}
	n, err := s.Client.Message.Update().
		Where(
			message.IDIn(ids...),
			message.UserUIDEQ(uid),
			message.ReadAtIsNil(),
		).
		SetReadAt(time.Now()).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("标记已读失败: %w", err)
	}
	return n, nil
}

// MarkAllRead 将当前用户所有未读消息标记为已读
func (s *MessageService) MarkAllRead(ctx context.Context, uid string) (int, error) {
	if uid == "" {
		return 0, ErrMessageForbidden
	}
	n, err := s.Client.Message.Update().
		Where(
			message.UserUIDEQ(uid),
			message.ReadAtIsNil(),
		).
		SetReadAt(time.Now()).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("全部已读失败: %w", err)
	}
	return n, nil
}

// UnreadCount 统计当前用户未读消息数量（含广播）
func (s *MessageService) UnreadCount(ctx context.Context, uid string) (*UnreadCountResponse, error) {
	if uid == "" {
		return nil, ErrMessageForbidden
	}
	resp := &UnreadCountResponse{}

	base := func() *ent.MessageQuery {
		return s.Client.Message.Query().Where(
			message.Or(
				message.UserUIDEQ(uid),
				message.UserUIDEQ(MessageBroadcastUID),
			),
			message.ReadAtIsNil(),
		)
	}

	var err error
	resp.Total, err = base().Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计未读总数失败: %w", err)
	}
	resp.System, err = base().Where(message.TypeEQ(MessageTypeSystem)).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计系统未读失败: %w", err)
	}
	resp.Business, err = base().Where(message.TypeEQ(MessageTypeBusiness)).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计业务未读失败: %w", err)
	}
	resp.Task, err = base().Where(message.TypeEQ(MessageTypeTask)).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计任务未读失败: %w", err)
	}
	return resp, nil
}

// Delete 删除指定 ID 的消息（仅允许删自己的，不允许删广播消息）
func (s *MessageService) Delete(ctx context.Context, uid string, id int) error {
	if uid == "" {
		return ErrMessageForbidden
	}
	entity, err := s.Client.Message.Query().Where(message.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrMessageNotFound
		}
		return fmt.Errorf("查询消息失败: %w", err)
	}
	if entity.UserUID != uid {
		return ErrMessageForbidden
	}
	if err := s.Client.Message.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("删除消息失败: %w", err)
	}
	return nil
}

// ListSinceID 查询指定 ID 之后的消息（用于 SSE 断线补发）
func (s *MessageService) ListSinceID(ctx context.Context, uid string, lastID int, limit int) ([]*MessageResponse, error) {
	if uid == "" {
		return nil, ErrMessageForbidden
	}
	if limit <= 0 {
		limit = 100
	}
	entities, err := s.Client.Message.Query().Where(
		message.IDGT(lastID),
		message.Or(
			message.UserUIDEQ(uid),
			message.UserUIDEQ(MessageBroadcastUID),
		),
	).Order(ent.Asc(message.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询补发消息失败: %w", err)
	}
	out := make([]*MessageResponse, 0, len(entities))
	for _, e := range entities {
		out = append(out, s.toResponse(e))
	}
	return out, nil
}

// PublishToUser 向指定用户发布消息（先落库后推送）
func (s *MessageService) PublishToUser(ctx context.Context, uid string, msg PublishMessage) (*MessageResponse, error) {
	if uid == "" {
		return nil, errors.New("uid 不能为空")
	}
	return s.Create(ctx, &CreateMessageRequest{
		UserUID: uid,
		Type:    msg.Type,
		Level:   msg.Level,
		Title:   msg.Title,
		Content: msg.Content,
		Extra:   msg.Extra,
	})
}

// Broadcast 向所有在线用户广播消息（user_uid 落库为空字符串）
func (s *MessageService) Broadcast(ctx context.Context, msg PublishMessage) (*MessageResponse, error) {
	return s.Create(ctx, &CreateMessageRequest{
		UserUID: MessageBroadcastUID,
		Type:    msg.Type,
		Level:   msg.Level,
		Title:   msg.Title,
		Content: msg.Content,
		Extra:   msg.Extra,
	})
}

// ========== 内部辅助 ==========

// push 根据消息接收者决定调用 Hub.PushToUser 还是 Broadcast
func (s *MessageService) push(userUID string, resp *MessageResponse) {
	if s.Hub == nil || resp == nil {
		return
	}
	evt := sse.Event{
		ID:    strconv.Itoa(resp.ID),
		Event: SSEEventMessage,
		Data:  resp,
	}
	if userUID == MessageBroadcastUID {
		s.Hub.Broadcast(evt)
	} else {
		s.Hub.PushToUser(userUID, evt)
	}
}

// toResponse Ent 实体 -> 响应 DTO
func (s *MessageService) toResponse(e *ent.Message) *MessageResponse {
	if e == nil {
		return nil
	}
	return &MessageResponse{
		ID:        e.ID,
		UserUID:   e.UserUID,
		Type:      e.Type,
		Level:     e.Level,
		Title:     e.Title,
		Content:   e.Content,
		Extra:     e.Extra,
		ReadAt:    e.ReadAt,
		CreatedAt: e.CreatedAt,
	}
}

// defaultSSEMissedLimit SSE 断线补发的默认单次条数上限
const defaultSSEMissedLimit = 200

// BuildMissedFetcher 返回一个 cmf/sse.MissedFetcher，
// 供 HandleConnect 在客户端断线重连时用于补发未送达消息。
func (s *MessageService) BuildMissedFetcher() sse.MissedFetcher {
	return func(ctx context.Context, uid string, lastID int) ([]sse.Event, error) {
		list, err := s.ListSinceID(ctx, uid, lastID, defaultSSEMissedLimit)
		if err != nil {
			return nil, err
		}
		out := make([]sse.Event, 0, len(list))
		for _, m := range list {
			out = append(out, sse.Event{
				ID:    strconv.Itoa(m.ID),
				Event: SSEEventMessage,
				Data:  m,
			})
		}
		return out, nil
	}
}
