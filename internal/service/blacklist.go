package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/blacklist"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/eko/gocache/lib/v4/store"
	"github.com/wuwuseo/cmf/cache"
)

var (
	ErrCannotBlacklistSelf = errors.New("不能将自己加入黑名单")
	ErrUserNotBlacklisted  = errors.New("该用户不在黑名单中")
)

// BlacklistItem 黑名单列表项
type BlacklistItem struct {
	ID           int       `json:"id"`
	UserID       int       `json:"user_id"`
	UserUID      string    `json:"user_uid"`
	UserName     string    `json:"user_name"`
	UserEmail    string    `json:"user_email"`
	Reason       string    `json:"reason"`
	OperatorID   string    `json:"operator_id"`
	OperatorName string    `json:"operator_name"`
	CreatedAt    time.Time `json:"created_at"`
}

// BlacklistService 黑名单管理服务
type BlacklistService struct {
	client *ent.Client
	cache  *cache.Cache[[]byte]
}

// BlacklistServiceInterface 黑名单服务接口
type BlacklistServiceInterface interface {
	AddToBlacklist(ctx context.Context, userID int, reason, operatorUID, operatorName string) error
	RemoveFromBlacklist(ctx context.Context, userID int, operatorUID string) error
	ListBlacklist(ctx context.Context, page, pageSize int) (*ListResponse[*BlacklistItem], error)
	IsBlacklisted(ctx context.Context, uid string) (bool, error)
	IsLoggedOut(ctx context.Context, uid string) (bool, error)
}

// NewBlacklistService 创建黑名单服务实例
func NewBlacklistService(rawCache *cache.Cache[[]byte]) *BlacklistService {
	return &BlacklistService{
		client: global.ORM,
		cache:  rawCache,
	}
}

// AddToBlacklist 将用户加入黑名单
func (s *BlacklistService) AddToBlacklist(ctx context.Context, userID int, reason, operatorUID, operatorName string) error {
	u, err := s.client.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	if u.UID == operatorUID {
		return ErrCannotBlacklistSelf
	}

	blExist, _ := s.client.Blacklist.Query().Where(blacklist.UserIDEQ(userID)).Exist(ctx)
	if blExist {
		return ErrUserBlacklisted
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}

	_, err = tx.Blacklist.Create().
		SetUserID(userID).
		SetReason(reason).
		SetOperatorID(operatorUID).
		SetOperatorName(operatorName).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("加入黑名单失败: %w", err)
	}

	err = tx.User.UpdateOneID(userID).SetStatus(user.StatusSuspended).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("更新用户状态失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	if s.cache != nil {
		key := fmt.Sprintf("blacklist:uid:%s", u.UID)
		_ = s.cache.Set(ctx, key, []byte("1"))
	}

	return nil
}

// RemoveFromBlacklist 将用户从黑名单移除
func (s *BlacklistService) RemoveFromBlacklist(ctx context.Context, userID int, operatorUID string) error {
	u, err := s.client.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	bl, err := s.client.Blacklist.Query().Where(blacklist.UserIDEQ(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotBlacklisted
		}
		return err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}

	err = tx.Blacklist.DeleteOne(bl).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("移除黑名单失败: %w", err)
	}

	err = tx.User.UpdateOneID(userID).SetStatus(user.StatusInactive).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("恢复用户状态失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	if s.cache != nil {
		key := fmt.Sprintf("blacklist:uid:%s", u.UID)
		_ = s.cache.Delete(ctx, key)
	}

	return nil
}

// ListBlacklist 分页查询黑名单列表
func (s *BlacklistService) ListBlacklist(ctx context.Context, page, pageSize int) (*ListResponse[*BlacklistItem], error) {
	page, pageSize = pageSafe(page, pageSize)

	total, err := s.client.Blacklist.Query().Count(ctx)
	if err != nil {
		return nil, err
	}

	records, err := s.client.Blacklist.Query().
		WithUser().
		Order(ent.Desc(blacklist.FieldCreatedAt)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	list := make([]*BlacklistItem, 0, len(records))
	for _, r := range records {
		item := &BlacklistItem{
			ID:           r.ID,
			UserID:       r.UserID,
			Reason:       r.Reason,
			OperatorID:   r.OperatorID,
			OperatorName: r.OperatorName,
			CreatedAt:    r.CreatedAt,
		}
		if r.Edges.User != nil {
			item.UserUID = r.Edges.User.UID
			item.UserName = r.Edges.User.Username
			item.UserEmail = r.Edges.User.Email
		}
		list = append(list, item)
	}

	return &ListResponse[*BlacklistItem]{
		Total: total,
		Page:  page,
		Data:  list,
	}, nil
}

// IsBlacklisted 检查用户是否在黑名单中（优先查缓存）
func (s *BlacklistService) IsBlacklisted(ctx context.Context, uid string) (bool, error) {
	if s.cache != nil {
		key := fmt.Sprintf("blacklist:uid:%s", uid)
		_, err := s.cache.Get(ctx, key)
		if err == nil {
			return true, nil
		}
	}

	u, err := s.client.User.Query().Where(user.UIDEQ(uid)).Only(ctx)
	if err != nil {
		return false, nil
	}

	exist, err := s.client.Blacklist.Query().Where(blacklist.UserIDEQ(u.ID)).Exist(ctx)
	if err != nil {
		return false, err
	}

	if exist && s.cache != nil {
		key := fmt.Sprintf("blacklist:uid:%s", uid)
		_ = s.cache.Set(ctx, key, []byte("1"))
	}

	return exist, nil
}

// RecordLogout 记录用户退出时间戳
func (s *BlacklistService) RecordLogout(ctx context.Context, uid string, ttl time.Duration) error {
	if s.cache == nil {
		return nil
	}
	key := fmt.Sprintf("logout:uid:%s", uid)
	timestamp := []byte(fmt.Sprintf("%d", time.Now().Unix()))
	return s.cache.Set(ctx, key, timestamp, store.WithExpiration(ttl))
}

// IsLoggedOut 检查用户是否已退出
func (s *BlacklistService) IsLoggedOut(ctx context.Context, uid string) (bool, error) {
	if s.cache == nil {
		return false, nil
	}
	key := fmt.Sprintf("logout:uid:%s", uid)
	_, err := s.cache.Get(ctx, key)
	if err != nil {
		return false, nil
	}
	return true, nil
}

func pageSafe(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}
