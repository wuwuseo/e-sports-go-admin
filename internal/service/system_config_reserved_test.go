package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent/systemconfig"
)

func TestConfigServiceRejectsReservedCaptchaKeys(t *testing.T) {
	client := setupConfigTestDB(t)
	service := NewConfigService(nil)
	status := 1

	if _, err := service.Create(t.Context(), &CreateConfigRequest{
		Name:   "captcha",
		Key:    "captcha.scene.admin_login",
		Value:  `{}`,
		Type:   "json",
		Status: &status,
	}); !errors.Is(err, ErrConfigReserved) {
		t.Fatalf("Create(reserved) error = %v, want ErrConfigReserved", err)
	}

	item, err := client.SystemConfig.Create().
		SetName("captcha").
		SetKey("captcha.provider.geetest_gt4").
		SetValue("enc:v1:secret").
		SetType("json").
		SetGroup("captcha").
		Save(t.Context())
	if err != nil {
		t.Fatalf("seed reserved config: %v", err)
	}

	if _, err := service.Get(t.Context(), item.ID); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("Get(reserved) error = %v, want ErrConfigNotFound", err)
	}
	if _, err := service.GetByKey(t.Context(), item.Key); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("GetByKey(reserved) error = %v, want ErrConfigNotFound", err)
	}
	if err := service.Update(t.Context(), item.ID, &UpdateConfigRequest{}); !errors.Is(err, ErrConfigReserved) {
		t.Fatalf("Update(reserved) error = %v, want ErrConfigReserved", err)
	}
	if err := service.Delete(t.Context(), item.ID); !errors.Is(err, ErrConfigReserved) {
		t.Fatalf("Delete(reserved) error = %v, want ErrConfigReserved", err)
	}
}

func TestConfigServiceListHidesReservedCaptchaKeys(t *testing.T) {
	client := setupConfigTestDB(t)
	_, err := client.SystemConfig.Create().
		SetName("captcha").
		SetKey("captcha.scene.user_login").
		SetValue(`{"enabled":false}`).
		SetType("json").
		SetGroup("captcha").
		Save(t.Context())
	if err != nil {
		t.Fatalf("seed reserved config: %v", err)
	}
	_, err = client.SystemConfig.Create().
		SetName("site").
		SetKey("site_name").
		SetValue("Admin").
		Save(t.Context())
	if err != nil {
		t.Fatalf("seed public config: %v", err)
	}

	result, err := NewConfigService(nil).List(t.Context(), &ListConfigRequest{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Total != 1 || len(result.Data) != 1 || result.Data[0].Key != "site_name" {
		t.Fatalf("List() = %+v", result)
	}
	exists, err := client.SystemConfig.Query().
		Where(systemconfig.KeyHasPrefix("captcha.")).
		Exist(t.Context())
	if err != nil || !exists {
		t.Fatalf("reserved seed unexpectedly missing: %v", err)
	}
}
