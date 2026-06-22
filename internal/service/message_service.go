package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wuwuseo/cmf/log"
	"github.com/wuwuseo/cmf/sse"
	"go.uber.org/zap"

	"go-admin/internal/ent"
	"go-admin/internal/ent/message"
	"go-admin/internal/global"
	"go-admin/internal/utils"
)

// 站内消息领域错误
var (
	ErrMessageNotFound        = errors.New("消息不存在")
	ErrMessageForbidden       = errors.New("无权操作该消息")
	ErrMessageInvalidReq      = errors.New("非法的消息请求参数")
	ErrMessageBatchTooLarge   = errors.New("批量操作 ID 数量超过上限")
	ErrMessageClearNoCriteria = errors.New("清空操作必须至少指定一个条件")
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
	SSEEventMessage        = "message"
	SSEEventUnreadCount    = "unread_count"
	SSEEventMessageDeleted = "message_deleted"

	// 批量操作 ID 长度上限
	MessageBatchIDsMaxLen = 200

	// 列表排序方式
	MessageSortCreatedAtDesc = "created_at_desc"
	MessageSortCreatedAtAsc  = "created_at_asc"
)

// ========== 请求/响应结构体 ==========

// PublishMessage 业务侧调用 Publisher 时的消息载荷
type PublishMessage struct {
	Type        string         `json:"type"    validate:"required,oneof=system business task"`
	Level       string         `json:"level"   validate:"omitempty,oneof=info warning error"`
	Title       string         `json:"title"   validate:"required,max=200"`
	Content     string         `json:"content" validate:"required"`
	ContentHTML string         `json:"content_html,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// CreateMessageRequest 通过接口创建消息（管理端可用）
type CreateMessageRequest struct {
	UserUID     string         `json:"user_uid"` // 为空表示广播
	Type        string         `json:"type"    validate:"required,oneof=system business task"`
	Level       string         `json:"level"   validate:"omitempty,oneof=info warning error"`
	Title       string         `json:"title"   validate:"required,max=200"`
	Content     string         `json:"content" validate:"required"`
	ContentHTML string         `json:"content_html,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// ListMessageRequest 列表查询请求
type ListMessageRequest struct {
	PaginationRequest
	Type       string `query:"type"        json:"type"`
	ReadStatus *int   `query:"read_status" json:"read_status"` // 0=未读 1=已读 nil=全部
	Keyword    string `query:"keyword"     json:"keyword"`     // 按 title/content 模糊
	Level      string `query:"level"       json:"level"`       // info/warning/error
	StartTime  string `query:"start_time"  json:"start_time"`  // RFC3339
	EndTime    string `query:"end_time"    json:"end_time"`    // RFC3339
	Sort       string `query:"sort"        json:"sort"`        // created_at_desc/created_at_asc
}

// MessageResponse 单条消息响应
type MessageResponse struct {
	ID          int            `json:"id"`
	UserUID     string         `json:"user_uid"`
	Type        string         `json:"type"`
	Level       string         `json:"level"`
	Title       string         `json:"title"`
	Content     string         `json:"content"`
	ContentHTML string         `json:"content_html,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
	ReadAt      *time.Time     `json:"read_at,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type ListMessagesResponse = ListResponse[MessageResponse]

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

// MarkUnreadRequest 标记未读请求
type MarkUnreadRequest struct {
	IDs []int `json:"ids" validate:"required,min=1"`
}

// DeleteBatchRequest 批量删除请求
type DeleteBatchRequest struct {
	IDs []int `json:"ids" validate:"required,min=1"`
}

// DeleteBatchResponse 批量删除响应
type DeleteBatchResponse struct {
	Deleted int `json:"deleted"`
}

// ClearMessagesRequest 条件清空请求（至少传入一个条件）
type ClearMessagesRequest struct {
	Type       string `json:"type"        validate:"omitempty,oneof=system business task"`
	ReadStatus *int   `json:"read_status" validate:"omitempty,oneof=0 1"`
	Before     string `json:"before"` // RFC3339
}

// HasCriteria 至少有一个条件
func (r *ClearMessagesRequest) HasCriteria() bool {
	if r == nil {
		return false
	}
	return r.Type != "" || r.ReadStatus != nil || strings.TrimSpace(r.Before) != ""
}

// ========== 接口定义 ==========

// MessageServiceInterface 站内消息服务接口
type MessageServiceInterface interface {
	Create(ctx context.Context, req *CreateMessageRequest) (*MessageResponse, error)
	Get(ctx context.Context, uid string, id int) (*MessageResponse, error)
	List(ctx context.Context, uid string, req *ListMessageRequest) (*ListResponse[*MessageResponse], error)
	MarkRead(ctx context.Context, uid string, ids []int) (int, error)
	MarkUnread(ctx context.Context, uid string, ids []int) (int, error)
	MarkAllRead(ctx context.Context, uid string) (int, error)
	UnreadCount(ctx context.Context, uid string) (*UnreadCountResponse, error)
	Delete(ctx context.Context, uid string, id int) error
	DeleteBatch(ctx context.Context, uid string, ids []int) (int, error)
	Clear(ctx context.Context, uid string, req *ClearMessagesRequest) (int, error)
	ListSinceID(ctx context.Context, uid string, lastID int, limit int) ([]*MessageResponse, error)

	// Publisher 业务侧 API
	PublishToUser(ctx context.Context, uid string, msg PublishMessage) (*MessageResponse, error)
	Broadcast(ctx context.Context, msg PublishMessage) (*MessageResponse, error)
	// BroadcastToUsers 批量给指定用户列表入库 + 按偏好过滤推送
	BroadcastToUsers(ctx context.Context, uids []string, msg PublishMessage) (int, error)
}

// ========== 实现 ==========

// MessageService 站内消息服务实现
type MessageService struct {
	Client        *ent.Client
	Hub           *sse.Hub
	PreferenceSvc MessagePreferenceServiceInterface // 可为 nil
}

// NewMessageService 构造函数
func NewMessageService(hub *sse.Hub) *MessageService {
	return &MessageService{
		Client: global.ORM,
		Hub:    hub,
	}
}

// NewMessageServiceWithPreference 带偏好服务的构造函数
func NewMessageServiceWithPreference(hub *sse.Hub, prefSvc MessagePreferenceServiceInterface) *MessageService {
	return &MessageService{
		Client:        global.ORM,
		Hub:           hub,
		PreferenceSvc: prefSvc,
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
	if req.ContentHTML != "" {
		creator.SetContentHTML(req.ContentHTML)
	}
	if req.Extra != nil {
		creator.SetExtra(req.Extra)
	}
	entity, err := creator.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建消息失败: %w", err)
	}
	resp := s.toResponse(entity)
	s.push(req.UserUID, resp)
	// 主动下发未读数（非广播场景）
	if req.UserUID != MessageBroadcastUID {
		s.pushUnreadCount(ctx, req.UserUID)
	}
	return resp, nil
}

// Get 获取消息详情（必须是接收者或属于广播）
func (s *MessageService) Get(ctx context.Context, uid string, id int) (*MessageResponse, error) {
	if uid == "" {
		return nil, ErrMessageForbidden
	}
	if id <= 0 {
		return nil, ErrMessageInvalidReq
	}
	entity, err := s.Client.Message.Query().Where(message.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrMessageNotFound
		}
		return nil, fmt.Errorf("查询消息失败: %w", err)
	}
	if entity.UserUID != uid && entity.UserUID != MessageBroadcastUID {
		return nil, ErrMessageForbidden
	}
	return s.toResponse(entity), nil
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
	if req.Level != "" {
		q = q.Where(message.LevelEQ(req.Level))
	}
	if req.ReadStatus != nil {
		switch *req.ReadStatus {
		case 0:
			q = q.Where(message.ReadAtIsNil())
		case 1:
			q = q.Where(message.ReadAtNotNil())
		}
	}
	if kw := strings.TrimSpace(req.Keyword); kw != "" {
		q = q.Where(message.Or(
			message.TitleContains(kw),
			message.ContentContains(kw),
		))
	}
	if st := strings.TrimSpace(req.StartTime); st != "" {
		t, err := time.Parse(time.RFC3339, st)
		if err != nil {
			return nil, fmt.Errorf("%w: start_time 格式应为 RFC3339", ErrMessageInvalidReq)
		}
		q = q.Where(message.CreatedAtGTE(t))
	}
	if et := strings.TrimSpace(req.EndTime); et != "" {
		t, err := time.Parse(time.RFC3339, et)
		if err != nil {
			return nil, fmt.Errorf("%w: end_time 格式应为 RFC3339", ErrMessageInvalidReq)
		}
		q = q.Where(message.CreatedAtLTE(t))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计消息总数失败: %w", err)
	}
	// 排序方式
	orderFn := ent.Desc(message.FieldCreatedAt)
	if req.Sort == MessageSortCreatedAtAsc {
		orderFn = ent.Asc(message.FieldCreatedAt)
	}
	entities, err := q.Order(orderFn, ent.Desc(message.FieldID)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询消息列表失败: %w", err)
	}
	return BuildList(entities, total, page, s.toResponse), nil
}

// MarkRead 将指定消息标记为已读（仅自己的消息）
func (s *MessageService) MarkRead(ctx context.Context, uid string, ids []int) (int, error) {
	if uid == "" {
		return 0, ErrMessageForbidden
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) > MessageBatchIDsMaxLen {
		return 0, ErrMessageBatchTooLarge
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
	s.pushUnreadCount(ctx, uid)
	return n, nil
}

// MarkUnread 将指定消息标记为未读（清空 read_at，已读回退）
func (s *MessageService) MarkUnread(ctx context.Context, uid string, ids []int) (int, error) {
	if uid == "" {
		return 0, ErrMessageForbidden
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) > MessageBatchIDsMaxLen {
		return 0, ErrMessageBatchTooLarge
	}
	n, err := s.Client.Message.Update().
		Where(
			message.IDIn(ids...),
			message.UserUIDEQ(uid),
			message.ReadAtNotNil(),
		).
		ClearReadAt().
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("标记未读失败: %w", err)
	}
	s.pushUnreadCount(ctx, uid)
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
	s.pushUnreadCount(ctx, uid)
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
	n, err := s.Client.Message.Delete().
		Where(
			message.IDEQ(id),
			message.UserUIDEQ(uid),
		).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除消息失败: %w", err)
	}
	if n == 0 {
		exists, err := s.Client.Message.Query().Where(message.IDEQ(id)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("查询消息失败: %w", err)
		}
		if exists {
			return ErrMessageForbidden
		}
		return ErrMessageNotFound
	}
	s.pushMessageDeleted(uid, []int{id})
	s.pushUnreadCount(ctx, uid)
	return nil
}

// DeleteBatch 批量删除消息（仅自己的消息）
func (s *MessageService) DeleteBatch(ctx context.Context, uid string, ids []int) (int, error) {
	if uid == "" {
		return 0, ErrMessageForbidden
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) > MessageBatchIDsMaxLen {
		return 0, ErrMessageBatchTooLarge
	}
	// 仅删除归属于当前用户的消息（避免越权删除广播或他人的消息）
	deletedIDs, err := s.Client.Message.Query().
		Where(
			message.IDIn(ids...),
			message.UserUIDEQ(uid),
		).
		IDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("查询待删除消息失败: %w", err)
	}
	if len(deletedIDs) == 0 {
		return 0, nil
	}
	n, err := s.Client.Message.Delete().Where(message.IDIn(deletedIDs...)).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("批量删除消息失败: %w", err)
	}
	s.pushMessageDeleted(uid, deletedIDs)
	s.pushUnreadCount(ctx, uid)
	return n, nil
}

// Clear 按条件批量删除（至少必须传入一个条件）
func (s *MessageService) Clear(ctx context.Context, uid string, req *ClearMessagesRequest) (int, error) {
	if uid == "" {
		return 0, ErrMessageForbidden
	}
	if req == nil || !req.HasCriteria() {
		return 0, ErrMessageClearNoCriteria
	}
	q := s.Client.Message.Query().Where(message.UserUIDEQ(uid))
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
	if before := strings.TrimSpace(req.Before); before != "" {
		t, err := time.Parse(time.RFC3339, before)
		if err != nil {
			return 0, fmt.Errorf("%w: before 格式应为 RFC3339", ErrMessageInvalidReq)
		}
		q = q.Where(message.CreatedAtLT(t))
	}

	deletedIDs, err := q.IDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("查询待清空消息失败: %w", err)
	}
	if len(deletedIDs) == 0 {
		return 0, nil
	}
	n, err := s.Client.Message.Delete().Where(message.IDIn(deletedIDs...)).Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("清空消息失败: %w", err)
	}
	s.pushMessageDeleted(uid, deletedIDs)
	s.pushUnreadCount(ctx, uid)
	return n, nil
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
		UserUID:     uid,
		Type:        msg.Type,
		Level:       msg.Level,
		Title:       msg.Title,
		Content:     msg.Content,
		ContentHTML: msg.ContentHTML,
		Extra:       msg.Extra,
	})
}

// Broadcast 向所有在线用户广播消息（user_uid 落库为空字符串）
func (s *MessageService) Broadcast(ctx context.Context, msg PublishMessage) (*MessageResponse, error) {
	return s.Create(ctx, &CreateMessageRequest{
		UserUID:     MessageBroadcastUID,
		Type:        msg.Type,
		Level:       msg.Level,
		Title:       msg.Title,
		Content:     msg.Content,
		ContentHTML: msg.ContentHTML,
		Extra:       msg.Extra,
	})
}

// BroadcastToUsers 批量入库 + 按偏好过滤推送
// 逻辑：每个接收者一条消息入库；偏好关闭对应 type 的用户仅入库，不实时推送
func (s *MessageService) BroadcastToUsers(ctx context.Context, uids []string, msg PublishMessage) (int, error) {
	if len(uids) == 0 {
		return 0, nil
	}
	level := msg.Level
	if level == "" {
		level = MessageLevelInfo
	}

	// 去重
	seen := make(map[string]struct{}, len(uids))
	unique := make([]string, 0, len(uids))
	for _, u := range uids {
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		unique = append(unique, u)
	}
	if len(unique) == 0 {
		return 0, nil
	}

	// 批量构造创建器
	bulk := make([]*ent.MessageCreate, 0, len(unique))
	for _, u := range unique {
		c := s.Client.Message.Create().
			SetUserUID(u).
			SetType(msg.Type).
			SetLevel(level).
			SetTitle(msg.Title).
			SetContent(msg.Content)
		if msg.ContentHTML != "" {
			c.SetContentHTML(msg.ContentHTML)
		}
		if msg.Extra != nil {
			c.SetExtra(msg.Extra)
		}
		bulk = append(bulk, c)
	}
	entities, err := s.Client.Message.CreateBulk(bulk...).Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("批量入库消息失败: %w", err)
	}

	hubTotal := 0
	if s.Hub != nil {
		hubTotal = s.Hub.CountTotalConnections()
	}
	log.Info("BroadcastToUsers 入库完成，开始推送",
		zap.Int("target_count", len(entities)),
		zap.String("type", msg.Type),
		zap.Int("hub_total_connections", hubTotal),
	)

	// 按偏好过滤推送
	pushed := 0
	skipped := 0
	for _, e := range entities {
		// 偏好未启用：默认全推送
		if s.PreferenceSvc != nil {
			pref, perr := s.PreferenceSvc.GetOrDefault(ctx, e.UserUID)
			if perr == nil && pref != nil && !isSubscribedType(pref, msg.Type) {
				// 跳过推送，仅入库
				skipped++
				log.Info("BroadcastToUsers 按偏好跳过推送",
					zap.String("user_uid", e.UserUID),
					zap.String("type", msg.Type),
				)
				continue
			}
		}
		s.push(e.UserUID, s.toResponse(e))
		s.pushUnreadCount(ctx, e.UserUID)
		pushed++
	}
	log.Info("BroadcastToUsers 推送完成",
		zap.Int("pushed", pushed),
		zap.Int("skipped_by_preference", skipped),
	)
	return len(entities), nil
}

// ========== 内部辅助 ==========

// isSubscribedType 根据偏好判断是否订阅指定 type
func isSubscribedType(pref *MessagePreferenceResponse, t string) bool {
	switch t {
	case MessageTypeSystem:
		return pref.SubscribeSystem
	case MessageTypeBusiness:
		return pref.SubscribeBusiness
	case MessageTypeTask:
		return pref.SubscribeTask
	}
	return true
}

// push 根据消息接收者决定调用 Hub.PushToUser 还是 Broadcast
func (s *MessageService) push(userUID string, resp *MessageResponse) {
	if s.Hub == nil {
		log.Warn("SSE 推送跳过：Hub 未初始化",
			zap.String("user_uid", userUID),
		)
		return
	}
	if resp == nil {
		return
	}
	evt := sse.Event{
		ID:    strconv.Itoa(resp.ID),
		Event: SSEEventMessage,
		Data:  resp,
	}
	if userUID == MessageBroadcastUID {
		total := s.Hub.CountTotalConnections()
		log.Info("SSE 全员广播",
			zap.String("event", SSEEventMessage),
			zap.Int("msg_id", resp.ID),
			zap.Int("online_total", total),
		)
		s.Hub.Broadcast(evt)
	} else {
		conns := s.Hub.CountUserConnections(userUID)
		log.Info("SSE 推送给用户",
			zap.String("event", SSEEventMessage),
			zap.Int("msg_id", resp.ID),
			zap.String("user_uid", userUID),
			zap.Int("user_connections", conns),
		)
		s.Hub.PushToUser(userUID, evt)
	}
}

// pushUnreadCount 主动下发未读数事件
func (s *MessageService) pushUnreadCount(ctx context.Context, userUID string) {
	if s.Hub == nil || userUID == "" {
		return
	}
	cnt, err := s.UnreadCount(ctx, userUID)
	if err != nil || cnt == nil {
		return
	}
	s.Hub.PushToUser(userUID, sse.Event{
		Event: SSEEventUnreadCount,
		Data:  cnt,
	})
}

// pushMessageDeleted 主动下发消息删除事件
func (s *MessageService) pushMessageDeleted(userUID string, ids []int) {
	if s.Hub == nil || userUID == "" || len(ids) == 0 {
		return
	}
	s.Hub.PushToUser(userUID, sse.Event{
		Event: SSEEventMessageDeleted,
		Data:  map[string]any{"ids": ids},
	})
}

// toResponse Ent 实体 -> 响应 DTO
func (s *MessageService) toResponse(e *ent.Message) *MessageResponse {
	if e == nil {
		return nil
	}
	return &MessageResponse{
		ID:          e.ID,
		UserUID:     e.UserUID,
		Type:        e.Type,
		Level:       e.Level,
		Title:       e.Title,
		Content:     e.Content,
		ContentHTML: e.ContentHTML,
		Extra:       e.Extra,
		ReadAt:      e.ReadAt,
		CreatedAt:   e.CreatedAt,
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
