package service

import (
	"context"
	"errors"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/blacklist"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
)

var (
	ErrUserAlreadyActive    = errors.New("用户当前已处于激活状态")
	ErrUserAlreadyInactive  = errors.New("用户当前已处于停用状态")
	ErrUserBlacklisted      = errors.New("该用户已被加入黑名单，请先解除黑名单")
	ErrCannotDeactivateSelf = errors.New("不能停用自己的账号")
)

// UserActivationService 用户激活/停用服务
type UserActivationService struct {
	client *ent.Client
}

// UserActivationServiceInterface 用户激活/停用服务接口
type UserActivationServiceInterface interface {
	ActivateUser(ctx context.Context, userID int, operatorUID string) error
	DeactivateUser(ctx context.Context, userID int, operatorUID string) error
}

// NewUserActivationService 创建用户激活服务实例
func NewUserActivationService() *UserActivationService {
	return &UserActivationService{client: global.ORM}
}

// ActivateUser 激活用户
func (s *UserActivationService) ActivateUser(ctx context.Context, userID int, operatorUID string) error {
	u, err := s.client.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	if u.Status == user.StatusActive {
		return ErrUserAlreadyActive
	}

	blExist, _ := s.client.Blacklist.Query().Where(blacklist.UserIDEQ(userID)).Exist(ctx)
	if blExist {
		return ErrUserBlacklisted
	}

	err = s.client.User.UpdateOneID(userID).SetStatus(user.StatusActive).Exec(ctx)
	if err != nil {
		return fmt.Errorf("激活用户失败: %w", err)
	}

	return nil
}

// DeactivateUser 停用用户
func (s *UserActivationService) DeactivateUser(ctx context.Context, userID int, operatorUID string) error {
	u, err := s.client.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	if u.Status == user.StatusInactive {
		return ErrUserAlreadyInactive
	}

	if u.UID == operatorUID {
		return ErrCannotDeactivateSelf
	}

	err = s.client.User.UpdateOneID(userID).SetStatus(user.StatusInactive).Exec(ctx)
	if err != nil {
		return fmt.Errorf("停用用户失败: %w", err)
	}

	return nil
}
