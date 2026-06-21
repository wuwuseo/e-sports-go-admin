package service

import (
	"context"
	"errors"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/global"
)

var (
	ErrThirdPartyBound    = errors.New("该第三方账号已被其他用户绑定")
	ErrThirdPartyNotFound = errors.New("第三方用户关联不存在")
)

// ThirdPartyUserListItem 第三方用户列表项
type ThirdPartyUserListItem struct {
	ID           int        `json:"id"`
	UserID       int        `json:"user_id"`
	UserUID      string     `json:"user_uid"`
	UserName     string     `json:"user_name"`
	UserEmail    string     `json:"user_email"`
	Platform     string     `json:"platform"`
	PlatformName string     `json:"platform_name"`
	Openid       string     `json:"openid"`
	Nickname     string     `json:"nickname"`
	AvatarURL    string     `json:"avatar_url"`
	Source       string     `json:"source"`
	Company      string     `json:"company"`
	Status       string     `json:"status"`
	LastSyncAt   *time.Time `json:"last_sync_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// BindThirdPartyRequest 绑定第三方账号请求
type BindThirdPartyRequest struct {
	UserID       int    `json:"user_id" validate:"required"`
	Platform     string `json:"platform" validate:"required"`
	PlatformName string `json:"platform_name"`
	Openid       string `json:"openid" validate:"required"`
	Unionid      string `json:"unionid"`
	Nickname     string `json:"nickname"`
	AvatarURL    string `json:"avatar_url"`
	Source       string `json:"source"`
	SourceUserID string `json:"source_user_id"`
	Company      string `json:"company"`
}

// ThirdPartyUserService 第三方用户管理服务
type ThirdPartyUserService struct {
	client *ent.Client
}

// ThirdPartyUserServiceInterface 第三方用户服务接口
type ThirdPartyUserServiceInterface interface {
	BindThirdPartyUser(ctx context.Context, req *BindThirdPartyRequest) (*ent.ThirdPartyUser, error)
	UnbindThirdPartyUser(ctx context.Context, id int) error
	ListUserThirdPartyAccounts(ctx context.Context, userID int) ([]*ent.ThirdPartyUser, error)
	ListThirdPartyUsers(ctx context.Context, page, pageSize int, platform, keyword string) (*ListResponse[*ThirdPartyUserListItem], error)
}

// NewThirdPartyUserService 创建第三方用户服务实例
func NewThirdPartyUserService() *ThirdPartyUserService {
	return &ThirdPartyUserService{client: global.ORM}
}

// BindThirdPartyUser 绑定第三方账号
func (s *ThirdPartyUserService) BindThirdPartyUser(ctx context.Context, req *BindThirdPartyRequest) (*ent.ThirdPartyUser, error) {
	exist, err := s.client.ThirdPartyUser.Query().Where(
		thirdpartyuser.Or(
			thirdpartyuser.And(
				thirdpartyuser.PlatformEQ(req.Platform),
				thirdpartyuser.OpenidEQ(req.Openid),
			),
			thirdpartyuser.And(
				thirdpartyuser.UserIDEQ(req.UserID),
				thirdpartyuser.PlatformEQ(req.Platform),
			),
		),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if exist {
		return nil, ErrThirdPartyBound
	}

	create := s.client.ThirdPartyUser.Create().
		SetUserID(req.UserID).
		SetPlatform(req.Platform).
		SetOpenid(req.Openid).
		SetStatus(thirdpartyuser.StatusActive)

	if req.PlatformName != "" {
		create.SetPlatformName(req.PlatformName)
	}
	if req.Unionid != "" {
		create.SetUnionid(req.Unionid)
	}
	if req.Nickname != "" {
		create.SetNickname(req.Nickname)
	}
	if req.AvatarURL != "" {
		create.SetAvatarURL(req.AvatarURL)
	}
	if req.Source != "" {
		create.SetSource(req.Source)
	}
	if req.SourceUserID != "" {
		create.SetSourceUserID(req.SourceUserID)
	}
	if req.Company != "" {
		create.SetCompany(req.Company)
	}

	return create.Save(ctx)
}

// UnbindThirdPartyUser 解绑第三方账号
func (s *ThirdPartyUserService) UnbindThirdPartyUser(ctx context.Context, id int) error {
	_, err := s.client.ThirdPartyUser.Query().Where(thirdpartyuser.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrThirdPartyNotFound
		}
		return err
	}

	return s.client.ThirdPartyUser.DeleteOneID(id).Exec(ctx)
}

// ListUserThirdPartyAccounts 查询用户的所有第三方绑定
func (s *ThirdPartyUserService) ListUserThirdPartyAccounts(ctx context.Context, userID int) ([]*ent.ThirdPartyUser, error) {
	return s.client.ThirdPartyUser.Query().
		Where(thirdpartyuser.UserIDEQ(userID)).
		All(ctx)
}

// ListThirdPartyUsers 全局第三方用户列表
func (s *ThirdPartyUserService) ListThirdPartyUsers(ctx context.Context, page, pageSize int, platform, keyword string) (*ListResponse[*ThirdPartyUserListItem], error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	query := s.client.ThirdPartyUser.Query()

	if platform != "" {
		query = query.Where(thirdpartyuser.PlatformEQ(platform))
	}
	if keyword != "" {
		query = query.Where(
			thirdpartyuser.Or(
				thirdpartyuser.NicknameContains(keyword),
				thirdpartyuser.OpenidContains(keyword),
				thirdpartyuser.CompanyContains(keyword),
			),
		)
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}

	records, err := query.
		WithUser().
		Order(ent.Desc(thirdpartyuser.FieldCreatedAt)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	list := make([]*ThirdPartyUserListItem, 0, len(records))
	for _, r := range records {
		item := &ThirdPartyUserListItem{
			ID:           r.ID,
			UserID:       r.UserID,
			Platform:     r.Platform,
			PlatformName: r.PlatformName,
			Openid:       r.Openid,
			Nickname:     r.Nickname,
			AvatarURL:    r.AvatarURL,
			Source:       r.Source,
			Company:      r.Company,
			Status:       string(r.Status),
			CreatedAt:    r.CreatedAt,
		}
		if r.LastSyncAt != nil {
			item.LastSyncAt = r.LastSyncAt
		}
		if r.Edges.User != nil {
			item.UserUID = r.Edges.User.UID
			item.UserName = r.Edges.User.Username
			item.UserEmail = r.Edges.User.Email
		}
		list = append(list, item)
	}

	return &ListResponse[*ThirdPartyUserListItem]{
		Total: total,
		Page:  page,
		Data:  list,
	}, nil
}
