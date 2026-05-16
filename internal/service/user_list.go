package service

import (
	"context"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"

	"go-admin/internal/ent"
	"go-admin/internal/ent/blacklist"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"
	"go-admin/internal/utils"
)

// UserListRequest 用户列表查询请求
type UserListRequest struct {
	Page          int    `query:"page" json:"page"`
	PageSize      int    `query:"page_size" json:"page_size"`
	Keyword       string `query:"keyword" json:"keyword"`
	Status        string `query:"status" json:"status"`
	RoleID        int    `query:"role_id" json:"role_id"`
	Platform      string `query:"platform" json:"platform"`
	IsBlacklisted *bool  `query:"is_blacklisted" json:"is_blacklisted"`
	StartTime     string `query:"start_time" json:"start_time"`
	EndTime       string `query:"end_time" json:"end_time"`
	SortField     string `query:"sort_field" json:"sort_field"`
	SortOrder     string `query:"sort_order" json:"sort_order"`
}

// UserListItem 用户列表项
type UserListItem struct {
	ID              int             `json:"id"`
	UID             string          `json:"uid"`
	Email           string          `json:"email"`
	Name            string          `json:"name"`
	Username        string          `json:"username"`
	Nickname        string          `json:"nickname"`
	Phone           string          `json:"phone"`
	Avatar          string          `json:"avatar"`
	Status          string          `json:"status"`
	IsBlacklisted   bool            `json:"is_blacklisted"`
	Roles           []*UserRoleItem `json:"roles"`
	ThirdPartyCount int             `json:"third_party_count"`
	LastLogin       *time.Time      `json:"last_login"`
	CreateTime      time.Time       `json:"created_at"`
}

// UserRoleItem 用户角色项
type UserRoleItem struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	IsPrimary   bool   `json:"is_primary"`
}

// UserDetailResponse 用户详情响应
type UserDetailResponse struct {
	UserListItem
	ThirdPartyAccounts []*ThirdPartyAccountItem `json:"third_party_accounts"`
}

// ThirdPartyAccountItem 第三方账号项
type ThirdPartyAccountItem struct {
	ID           int       `json:"id"`
	Platform     string    `json:"platform"`
	PlatformName string    `json:"platform_name"`
	Openid       string    `json:"openid"`
	Nickname     string    `json:"nickname"`
	AvatarURL    string    `json:"avatar_url"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

// UserListService 用户列表服务
type UserListService struct {
	client *ent.Client
}

// UserListServiceInterface 用户列表服务接口
type UserListServiceInterface interface {
	ListUsers(ctx context.Context, req *UserListRequest) (*ListResponse[*UserListItem], error)
	GetUserDetail(ctx context.Context, userID int) (*UserDetailResponse, error)
}

// NewUserListService 创建用户列表服务实例
func NewUserListService() *UserListService {
	return &UserListService{client: global.ORM}
}

// ListUsers 分页查询用户列表
func (s *UserListService) ListUsers(ctx context.Context, req *UserListRequest) (*ListResponse[*UserListItem], error) {
	page, pageSize := utils.NormalizePagination(req.Page, req.PageSize)

	query := s.client.User.Query()

	if req.Keyword != "" {
		keyword := strings.TrimSpace(req.Keyword)
		query = query.Where(
			user.Or(
				user.UsernameContains(keyword),
				user.EmailContains(keyword),
				user.PhoneContains(keyword),
				user.NicknameContains(keyword),
				user.NameContains(keyword),
			),
		)
	}

	if req.Status != "" {
		query = query.Where(user.StatusEQ(user.Status(req.Status)))
	}

	if req.RoleID > 0 {
		userIDs, err := s.client.UserRole.Query().
			Where(userrole.RoleIDEQ(req.RoleID)).
			Select(userrole.FieldUserID).
			Ints(ctx)
		if err != nil {
			return nil, err
		}
		if len(userIDs) == 0 {
			return &ListResponse[*UserListItem]{Total: 0, Page: page, Data: []*UserListItem{}}, nil
		}
		query = query.Where(user.IDIn(userIDs...))
	}

	if req.StartTime != "" {
		startTime, err := time.Parse(time.RFC3339, req.StartTime)
		if err == nil {
			query = query.Where(user.CreateTimeGTE(startTime))
		}
	}
	if req.EndTime != "" {
		endTime, err := time.Parse(time.RFC3339, req.EndTime)
		if err == nil {
			query = query.Where(user.CreateTimeLTE(endTime))
		}
	}

	if req.Platform != "" {
		userIDs, err := s.client.ThirdPartyUser.Query().
			Where(thirdpartyuser.PlatformEQ(req.Platform)).
			Select(thirdpartyuser.FieldUserID).
			Ints(ctx)
		if err != nil {
			return nil, err
		}
		if len(userIDs) == 0 {
			return &ListResponse[*UserListItem]{Total: 0, Page: page, Data: []*UserListItem{}}, nil
		}
		query = query.Where(user.IDIn(userIDs...))
	}

	if req.IsBlacklisted != nil {
		blacklistUserIDs, err := s.client.Blacklist.Query().
			Select(blacklist.FieldUserID).
			Ints(ctx)
		if err != nil {
			return nil, err
		}
		if *req.IsBlacklisted {
			if len(blacklistUserIDs) == 0 {
				return &ListResponse[*UserListItem]{Total: 0, Page: page, Data: []*UserListItem{}}, nil
			}
			query = query.Where(user.IDIn(blacklistUserIDs...))
		} else {
			if len(blacklistUserIDs) > 0 {
				query = query.Where(user.Not(user.IDIn(blacklistUserIDs...)))
			}
		}
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}

	sortOrder := user.ByCreateTime(sql.OrderDesc())
	if req.SortField != "" {
		switch req.SortField {
		case "last_login":
			if req.SortOrder == "asc" {
				sortOrder = user.ByLastLogin()
			} else {
				sortOrder = user.ByLastLogin(sql.OrderDesc())
			}
		case "name":
			if req.SortOrder == "asc" {
				sortOrder = user.ByName()
			} else {
				sortOrder = user.ByName(sql.OrderDesc())
			}
		case "created_at":
			if req.SortOrder == "asc" {
				sortOrder = user.ByCreateTime()
			}
		}
	}

	users, err := query.
		Order(sortOrder).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	userIDs := make([]int, len(users))
	userUIDs := make([]string, len(users))
	for i, u := range users {
		userIDs[i] = u.ID
		userUIDs[i] = u.UID
	}

	blacklistUserIDs, _ := s.client.Blacklist.Query().
		Where(blacklist.UserIDIn(userIDs...)).
		Select(blacklist.FieldUserID).
		Ints(ctx)
	blacklistSet := make(map[int]bool, len(blacklistUserIDs))
	for _, id := range blacklistUserIDs {
		blacklistSet[id] = true
	}

	userRoles, _ := s.client.UserRole.Query().
		Where(userrole.UserIDIn(userIDs...)).
		WithRole().
		All(ctx)
	roleByUserID := make(map[int][]*UserRoleItem)
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			roleByUserID[ur.UserID] = append(roleByUserID[ur.UserID], &UserRoleItem{
				ID:          ur.Edges.Role.ID,
				Name:        ur.Edges.Role.Name,
				DisplayName: ur.Edges.Role.DisplayName,
				IsPrimary:   ur.IsPrimary,
			})
		}
	}

	thirdPartyCounts, _ := s.client.ThirdPartyUser.Query().
		Where(thirdpartyuser.UserIDIn(userIDs...)).
		All(ctx)
	tpCountByUserID := make(map[int]int)
	for _, tp := range thirdPartyCounts {
		tpCountByUserID[tp.UserID]++
	}

	list := make([]*UserListItem, 0, len(users))
	for _, u := range users {
		item := &UserListItem{
			ID:              u.ID,
			UID:             u.UID,
			Email:           u.Email,
			Name:            u.Name,
			Username:        u.Username,
			Nickname:        u.Nickname,
			Phone:           u.Phone,
			Avatar:          u.Avatar,
			Status:          string(u.Status),
			IsBlacklisted:   blacklistSet[u.ID],
			Roles:           roleByUserID[u.ID],
			ThirdPartyCount: tpCountByUserID[u.ID],
			CreateTime:      u.CreateTime,
		}
		if !u.LastLogin.IsZero() {
			item.LastLogin = &u.LastLogin
		}
		list = append(list, item)
	}

	return &ListResponse[*UserListItem]{
		Total: total,
		Page:  page,
		Data:  list,
	}, nil
}

// GetUserDetail 获取用户详情
func (s *UserListService) GetUserDetail(ctx context.Context, userID int) (*UserDetailResponse, error) {
	u, err := s.client.User.Query().
		Where(user.IDEQ(userID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	blExist, _ := s.client.Blacklist.Query().
		Where(blacklist.UserIDEQ(u.ID)).
		Exist(ctx)

	userRoles, _ := s.client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole(func(q *ent.RoleQuery) {
			q.Where(role.StatusEQ(role.Status("active")))
		}).
		All(ctx)
	roles := make([]*UserRoleItem, 0, len(userRoles))
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			roles = append(roles, &UserRoleItem{
				ID:          ur.Edges.Role.ID,
				Name:        ur.Edges.Role.Name,
				DisplayName: ur.Edges.Role.DisplayName,
				IsPrimary:   ur.IsPrimary,
			})
		}
	}

	tpAccounts, _ := s.client.ThirdPartyUser.Query().
		Where(thirdpartyuser.UserIDEQ(u.ID)).
		All(ctx)
	thirdPartyList := make([]*ThirdPartyAccountItem, 0, len(tpAccounts))
	for _, tp := range tpAccounts {
		thirdPartyList = append(thirdPartyList, &ThirdPartyAccountItem{
			ID:           tp.ID,
			Platform:     tp.Platform,
			PlatformName: tp.PlatformName,
			Openid:       tp.Openid,
			Nickname:     tp.Nickname,
			AvatarURL:    tp.AvatarURL,
			Status:       string(tp.Status),
			CreatedAt:    tp.CreatedAt,
		})
	}

	detail := &UserDetailResponse{
		UserListItem: UserListItem{
			ID:              u.ID,
			UID:             u.UID,
			Email:           u.Email,
			Name:            u.Name,
			Username:        u.Username,
			Nickname:        u.Nickname,
			Phone:           u.Phone,
			Avatar:          u.Avatar,
			Status:          string(u.Status),
			IsBlacklisted:   blExist,
			Roles:           roles,
			ThirdPartyCount: len(thirdPartyList),
			CreateTime:      u.CreateTime,
		},
		ThirdPartyAccounts: thirdPartyList,
	}
	if !u.LastLogin.IsZero() {
		detail.LastLogin = &u.LastLogin
	}

	return detail, nil
}
