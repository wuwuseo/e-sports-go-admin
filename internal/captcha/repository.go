package captcha

import (
	"context"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"

	"github.com/wuwuseo/cmf/secureconfig"
)

const captchaConfigGroup = "captcha"

type SystemConfigRepository struct {
	client *ent.Client
}

var _ secureconfig.Repository = (*SystemConfigRepository)(nil)

func NewSystemConfigRepository(client *ent.Client) *SystemConfigRepository {
	return &SystemConfigRepository{client: client}
}

func (r *SystemConfigRepository) Get(ctx context.Context, key string) (string, error) {
	item, err := r.client.SystemConfig.Query().
		Where(systemconfig.KeyEQ(key), systemconfig.StatusEQ(1)).
		Only(ctx)
	if err != nil {
		return "", err
	}
	return item.Value, nil
}

func (r *SystemConfigRepository) Put(ctx context.Context, key, value string) error {
	updated, err := r.client.SystemConfig.Update().
		Where(systemconfig.KeyEQ(key)).
		SetValue(value).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update captcha config %q: %w", key, err)
	}
	if updated > 0 {
		return nil
	}
	_, err = r.client.SystemConfig.Create().
		SetName(key).
		SetKey(key).
		SetValue(value).
		SetType("json").
		SetGroup(captchaConfigGroup).
		SetStatus(1).
		SetRemark("managed by captcha settings").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("create captcha config %q: %w", key, err)
	}
	return nil
}

func (r *SystemConfigRepository) ListPrefix(ctx context.Context, prefix string) ([]secureconfig.Entry, error) {
	items, err := r.client.SystemConfig.Query().
		Where(systemconfig.KeyHasPrefix(prefix)).
		Order(ent.Asc(systemconfig.FieldKey)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list captcha configs: %w", err)
	}
	entries := make([]secureconfig.Entry, 0, len(items))
	for _, item := range items {
		entries = append(entries, secureconfig.Entry{Key: item.Key, Value: item.Value})
	}
	return entries, nil
}

func (r *SystemConfigRepository) CompareAndSwap(
	ctx context.Context,
	key, oldValue, newValue string,
) (bool, error) {
	updated, err := r.client.SystemConfig.Update().
		Where(systemconfig.KeyEQ(key), systemconfig.ValueEQ(oldValue)).
		SetValue(newValue).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("compare and swap captcha config %q: %w", key, err)
	}
	return updated == 1, nil
}
