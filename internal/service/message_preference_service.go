package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/messagepreference"
	"go-admin/internal/global"
)

// 偏好相关领域错误
var (
	ErrMessagePreferenceInvalidTime = errors.New("免打扰时间格式应为 HH:mm")
)

// hhmmRegex HH:mm 格式校验（00:00 ~ 23:59）
var hhmmRegex = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// MessagePreferenceResponse 用户偏好响应
type MessagePreferenceResponse struct {
	DesktopNotify     bool   `json:"desktop_notify"`
	SoundNotify       bool   `json:"sound_notify"`
	SubscribeSystem   bool   `json:"subscribe_system"`
	SubscribeBusiness bool   `json:"subscribe_business"`
	SubscribeTask     bool   `json:"subscribe_task"`
	DoNotDisturbStart string `json:"do_not_disturb_start"`
	DoNotDisturbEnd   string `json:"do_not_disturb_end"`
	// 外部触达渠道开关（站内信由 desktop_notify 控制）
	ChannelSMS  bool `json:"channel_sms"`
	ChannelMail bool `json:"channel_mail"`
	ChannelPush bool `json:"channel_push"`
}

// UpdateMessagePreferenceRequest 偏好更新请求（部分更新，全部字段可选）
type UpdateMessagePreferenceRequest struct {
	DesktopNotify     *bool   `json:"desktop_notify,omitempty"`
	SoundNotify       *bool   `json:"sound_notify,omitempty"`
	SubscribeSystem   *bool   `json:"subscribe_system,omitempty"`
	SubscribeBusiness *bool   `json:"subscribe_business,omitempty"`
	SubscribeTask     *bool   `json:"subscribe_task,omitempty"`
	DoNotDisturbStart *string `json:"do_not_disturb_start,omitempty"`
	DoNotDisturbEnd   *string `json:"do_not_disturb_end,omitempty"`
	ChannelSMS        *bool   `json:"channel_sms,omitempty"`
	ChannelMail       *bool   `json:"channel_mail,omitempty"`
	ChannelPush       *bool   `json:"channel_push,omitempty"`
}

// MessagePreferenceServiceInterface 偏好服务接口
type MessagePreferenceServiceInterface interface {
	GetOrDefault(ctx context.Context, uid string) (*MessagePreferenceResponse, error)
	GetByUIDs(ctx context.Context, uids []string) (map[string]*MessagePreferenceResponse, error)
	Update(ctx context.Context, uid string, req *UpdateMessagePreferenceRequest) (*MessagePreferenceResponse, error)
}

// MessagePreferenceService 偏好服务实现
type MessagePreferenceService struct {
	Client *ent.Client
}

// NewMessagePreferenceService 构造
func NewMessagePreferenceService() *MessagePreferenceService {
	return &MessagePreferenceService{Client: global.ORM}
}

var _ MessagePreferenceServiceInterface = (*MessagePreferenceService)(nil)

// defaultPreference 返回默认偏好
func defaultPreference() *MessagePreferenceResponse {
	return &MessagePreferenceResponse{
		DesktopNotify:     true,
		SoundNotify:       false,
		SubscribeSystem:   true,
		SubscribeBusiness: true,
		SubscribeTask:     true,
		DoNotDisturbStart: "",
		DoNotDisturbEnd:   "",
		ChannelSMS:        false,
		ChannelMail:       false,
		ChannelPush:       false,
	}
}

// GetOrDefault 查询偏好；未配置时返回默认值（不入库）
func (s *MessagePreferenceService) GetOrDefault(ctx context.Context, uid string) (*MessagePreferenceResponse, error) {
	if uid == "" {
		return nil, ErrMessageForbidden
	}
	entity, err := s.Client.MessagePreference.Query().
		Where(messagepreference.UserUIDEQ(uid)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return defaultPreference(), nil
		}
		return nil, fmt.Errorf("查询消息偏好失败: %w", err)
	}
	return entityToPreferenceResponse(entity), nil
}

// GetByUIDs 批量查询多个用户的偏好；未配置的用户使用默认值兜底。
// 用于批量广播场景，避免逐用户 GetOrDefault 造成 N+1 查询。
func (s *MessagePreferenceService) GetByUIDs(ctx context.Context, uids []string) (map[string]*MessagePreferenceResponse, error) {
	result := make(map[string]*MessagePreferenceResponse, len(uids))
	if len(uids) == 0 {
		return result, nil
	}
	entities, err := s.Client.MessagePreference.Query().
		Where(messagepreference.UserUIDIn(uids...)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("批量查询消息偏好失败: %w", err)
	}
	found := make(map[string]struct{}, len(entities))
	for _, e := range entities {
		result[e.UserUID] = entityToPreferenceResponse(e)
		found[e.UserUID] = struct{}{}
	}
	// 未配置的用户填默认值
	for _, uid := range uids {
		if _, ok := found[uid]; !ok {
			result[uid] = defaultPreference()
		}
	}
	return result, nil
}

// Update 部分更新偏好（不存在则创建）
func (s *MessagePreferenceService) Update(ctx context.Context, uid string, req *UpdateMessagePreferenceRequest) (*MessagePreferenceResponse, error) {
	if uid == "" {
		return nil, ErrMessageForbidden
	}
	if req == nil {
		req = &UpdateMessagePreferenceRequest{}
	}

	// 校验免打扰时间格式
	if req.DoNotDisturbStart != nil {
		if v := strings.TrimSpace(*req.DoNotDisturbStart); v != "" && !hhmmRegex.MatchString(v) {
			return nil, ErrMessagePreferenceInvalidTime
		}
	}
	if req.DoNotDisturbEnd != nil {
		if v := strings.TrimSpace(*req.DoNotDisturbEnd); v != "" && !hhmmRegex.MatchString(v) {
			return nil, ErrMessagePreferenceInvalidTime
		}
	}

	entity, err := s.Client.MessagePreference.Query().
		Where(messagepreference.UserUIDEQ(uid)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("查询消息偏好失败: %w", err)
	}

	if entity == nil {
		// 创建（基于默认值 + 请求覆盖）
		def := defaultPreference()
		applyUpdateToDefaults(def, req)
		created, err := s.Client.MessagePreference.Create().
			SetUserUID(uid).
			SetDesktopNotify(def.DesktopNotify).
			SetSoundNotify(def.SoundNotify).
			SetSubscribeSystem(def.SubscribeSystem).
			SetSubscribeBusiness(def.SubscribeBusiness).
			SetSubscribeTask(def.SubscribeTask).
			SetDoNotDisturbStart(def.DoNotDisturbStart).
			SetDoNotDisturbEnd(def.DoNotDisturbEnd).
			SetChannelSms(def.ChannelSMS).
			SetChannelMail(def.ChannelMail).
			SetChannelPush(def.ChannelPush).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("创建消息偏好失败: %w", err)
		}
		return entityToPreferenceResponse(created), nil
	}

	// 更新（部分字段）
	updater := s.Client.MessagePreference.UpdateOneID(entity.ID)
	if req.DesktopNotify != nil {
		updater.SetDesktopNotify(*req.DesktopNotify)
	}
	if req.SoundNotify != nil {
		updater.SetSoundNotify(*req.SoundNotify)
	}
	if req.SubscribeSystem != nil {
		updater.SetSubscribeSystem(*req.SubscribeSystem)
	}
	if req.SubscribeBusiness != nil {
		updater.SetSubscribeBusiness(*req.SubscribeBusiness)
	}
	if req.SubscribeTask != nil {
		updater.SetSubscribeTask(*req.SubscribeTask)
	}
	if req.DoNotDisturbStart != nil {
		updater.SetDoNotDisturbStart(strings.TrimSpace(*req.DoNotDisturbStart))
	}
	if req.DoNotDisturbEnd != nil {
		updater.SetDoNotDisturbEnd(strings.TrimSpace(*req.DoNotDisturbEnd))
	}
	if req.ChannelSMS != nil {
		updater.SetChannelSms(*req.ChannelSMS)
	}
	if req.ChannelMail != nil {
		updater.SetChannelMail(*req.ChannelMail)
	}
	if req.ChannelPush != nil {
		updater.SetChannelPush(*req.ChannelPush)
	}
	updated, err := updater.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("更新消息偏好失败: %w", err)
	}
	return entityToPreferenceResponse(updated), nil
}

// applyUpdateToDefaults 将部分更新请求覆盖到默认值
func applyUpdateToDefaults(def *MessagePreferenceResponse, req *UpdateMessagePreferenceRequest) {
	if req == nil {
		return
	}
	if req.DesktopNotify != nil {
		def.DesktopNotify = *req.DesktopNotify
	}
	if req.SoundNotify != nil {
		def.SoundNotify = *req.SoundNotify
	}
	if req.SubscribeSystem != nil {
		def.SubscribeSystem = *req.SubscribeSystem
	}
	if req.SubscribeBusiness != nil {
		def.SubscribeBusiness = *req.SubscribeBusiness
	}
	if req.SubscribeTask != nil {
		def.SubscribeTask = *req.SubscribeTask
	}
	if req.DoNotDisturbStart != nil {
		def.DoNotDisturbStart = strings.TrimSpace(*req.DoNotDisturbStart)
	}
	if req.DoNotDisturbEnd != nil {
		def.DoNotDisturbEnd = strings.TrimSpace(*req.DoNotDisturbEnd)
	}
	if req.ChannelSMS != nil {
		def.ChannelSMS = *req.ChannelSMS
	}
	if req.ChannelMail != nil {
		def.ChannelMail = *req.ChannelMail
	}
	if req.ChannelPush != nil {
		def.ChannelPush = *req.ChannelPush
	}
}

// entityToPreferenceResponse 转换 Ent 实体到响应
func entityToPreferenceResponse(e *ent.MessagePreference) *MessagePreferenceResponse {
	if e == nil {
		return defaultPreference()
	}
	return &MessagePreferenceResponse{
		DesktopNotify:     e.DesktopNotify,
		SoundNotify:       e.SoundNotify,
		SubscribeSystem:   e.SubscribeSystem,
		SubscribeBusiness: e.SubscribeBusiness,
		SubscribeTask:     e.SubscribeTask,
		DoNotDisturbStart: e.DoNotDisturbStart,
		DoNotDisturbEnd:   e.DoNotDisturbEnd,
		ChannelSMS:        e.ChannelSms,
		ChannelMail:       e.ChannelMail,
		ChannelPush:       e.ChannelPush,
	}
}
