package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/messagepreference"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupPreferenceTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:pref?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

// ======================== defaultPreference 测试 ========================

func TestDefaultPreference(t *testing.T) {
	def := defaultPreference()
	if def.DesktopNotify != true {
		t.Errorf("期望 DesktopNotify=true，实际 %v", def.DesktopNotify)
	}
	if def.SoundNotify != false {
		t.Errorf("期望 SoundNotify=false，实际 %v", def.SoundNotify)
	}
	if def.SubscribeSystem != true {
		t.Errorf("期望 SubscribeSystem=true，实际 %v", def.SubscribeSystem)
	}
	if def.SubscribeBusiness != true {
		t.Errorf("期望 SubscribeBusiness=true，实际 %v", def.SubscribeBusiness)
	}
	if def.SubscribeTask != true {
		t.Errorf("期望 SubscribeTask=true，实际 %v", def.SubscribeTask)
	}
	if def.DoNotDisturbStart != "" {
		t.Errorf("期望 DoNotDisturbStart=\"\"，实际 %v", def.DoNotDisturbStart)
	}
	if def.DoNotDisturbEnd != "" {
		t.Errorf("期望 DoNotDisturbEnd=\"\"，实际 %v", def.DoNotDisturbEnd)
	}
}

// ======================== GetOrDefault 测试 ========================

func TestGetOrDefault_EmptyUID(t *testing.T) {
	setupPreferenceTest(t)
	svc := NewMessagePreferenceService()
	_, err := svc.GetOrDefault(t.Context(), "")
	if !errors.Is(err, ErrMessageForbidden) {
		t.Fatalf("期望 ErrMessageForbidden，实际 %v", err)
	}
}

func TestGetOrDefault_NotFound_ReturnsDefault(t *testing.T) {
	setupPreferenceTest(t)
	svc := NewMessagePreferenceService()
	result, err := svc.GetOrDefault(t.Context(), "unknown-uid")
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if !result.DesktopNotify || !result.SubscribeSystem {
		t.Fatalf("期望返回默认值，实际 %+v", result)
	}
}

func TestGetOrDefault_Found_ReturnsStored(t *testing.T) {
	client := setupPreferenceTest(t)
	ctx := t.Context()

	_, _ = client.MessagePreference.Create().
		SetUserUID("user1").
		SetDesktopNotify(false).
		SetSoundNotify(true).
		SetSubscribeSystem(true).
		SetSubscribeBusiness(false).
		SetSubscribeTask(true).
		SetDoNotDisturbStart("22:00").
		SetDoNotDisturbEnd("08:00").
		Save(ctx)

	svc := NewMessagePreferenceService()
	result, err := svc.GetOrDefault(ctx, "user1")
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if result.DesktopNotify != false {
		t.Errorf("期望 DesktopNotify=false，实际 %v", result.DesktopNotify)
	}
	if result.SoundNotify != true {
		t.Errorf("期望 SoundNotify=true，实际 %v", result.SoundNotify)
	}
	if result.SubscribeBusiness != false {
		t.Errorf("期望 SubscribeBusiness=false，实际 %v", result.SubscribeBusiness)
	}
	if result.DoNotDisturbStart != "22:00" {
		t.Errorf("期望 DoNotDisturbStart=22:00，实际 %v", result.DoNotDisturbStart)
	}
	if result.DoNotDisturbEnd != "08:00" {
		t.Errorf("期望 DoNotDisturbEnd=08:00，实际 %v", result.DoNotDisturbEnd)
	}
}

// ======================== Update 测试 ========================

func TestUpdate_EmptyUID(t *testing.T) {
	setupPreferenceTest(t)
	svc := NewMessagePreferenceService()
	_, err := svc.Update(t.Context(), "", nil)
	if !errors.Is(err, ErrMessageForbidden) {
		t.Fatalf("期望 ErrMessageForbidden，实际 %v", err)
	}
}

func TestUpdate_NilRequest(t *testing.T) {
	setupPreferenceTest(t)
	svc := NewMessagePreferenceService()
	result, err := svc.Update(t.Context(), "user1", nil)
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if !result.DesktopNotify {
		t.Errorf("期望默认 DesktopNotify=true，实际 %v", result.DesktopNotify)
	}
}

func TestUpdate_CreatesIfNotExists(t *testing.T) {
	client := setupPreferenceTest(t)
	ctx := t.Context()
	svc := NewMessagePreferenceService()

	result, err := svc.Update(ctx, "user1", &UpdateMessagePreferenceRequest{
		DesktopNotify:     boolPtr(false),
		DoNotDisturbStart: stringPtr("23:00"),
	})
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if result.DesktopNotify != false {
		t.Errorf("期望 DesktopNotify=false，实际 %v", result.DesktopNotify)
	}
	if result.DoNotDisturbStart != "23:00" {
		t.Errorf("期望 DoNotDisturbStart=23:00，实际 %v", result.DoNotDisturbStart)
	}
	if result.SubscribeSystem != true {
		t.Errorf("期望默认 SubscribeSystem=true，实际 %v", result.SubscribeSystem)
	}

	exists, err := client.MessagePreference.Query().Where(messagepreference.UserUIDEQ("user1")).Exist(ctx)
	if err != nil {
		t.Fatalf("检查存在性失败: %v", err)
	}
	if !exists {
		t.Error("期望创建记录，但不存在")
	}
}

func TestUpdate_UpdatesExisting(t *testing.T) {
	client := setupPreferenceTest(t)
	ctx := t.Context()

	_, _ = client.MessagePreference.Create().
		SetUserUID("user1").
		SetDesktopNotify(true).
		SetSoundNotify(false).
		SetSubscribeSystem(true).
		SetSubscribeBusiness(true).
		SetSubscribeTask(true).
		SetDoNotDisturbStart("").
		SetDoNotDisturbEnd("").
		Save(ctx)

	svc := NewMessagePreferenceService()
	result, err := svc.Update(ctx, "user1", &UpdateMessagePreferenceRequest{
		DesktopNotify: boolPtr(false),
		SoundNotify:   boolPtr(true),
	})
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if result.DesktopNotify != false {
		t.Errorf("期望 DesktopNotify=false，实际 %v", result.DesktopNotify)
	}
	if result.SoundNotify != true {
		t.Errorf("期望 SoundNotify=true，实际 %v", result.SoundNotify)
	}
	if result.SubscribeSystem != true {
		t.Errorf("期望 SubscribeSystem 保持不变，实际 %v", result.SubscribeSystem)
	}

	entity, err := client.MessagePreference.Query().Where(messagepreference.UserUIDEQ("user1")).Only(ctx)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if entity.DesktopNotify != false {
		t.Errorf("数据库中 DesktopNotify 应为 false，实际 %v", entity.DesktopNotify)
	}
	if entity.SoundNotify != true {
		t.Errorf("数据库中 SoundNotify 应为 true，实际 %v", entity.SoundNotify)
	}
}

func TestUpdate_DoNotDisturbTimeValidation_Valid(t *testing.T) {
	setupPreferenceTest(t)
	svc := NewMessagePreferenceService()

	validTimes := []string{"00:00", "12:30", "23:59", "08:00"}
	for _, time := range validTimes {
		_, err := svc.Update(t.Context(), "user1", &UpdateMessagePreferenceRequest{
			DoNotDisturbStart: stringPtr(time),
		})
		if err != nil {
			t.Errorf("期望时间 %q 有效，实际错误: %v", time, err)
		}
	}
}

func TestUpdate_DoNotDisturbTimeValidation_Invalid(t *testing.T) {
	setupPreferenceTest(t)
	svc := NewMessagePreferenceService()

	invalidTimes := []string{"25:00", "12:60", "24:00", "0:00", "12:0", "abc", "1234"}
	for _, time := range invalidTimes {
		_, err := svc.Update(t.Context(), "user1", &UpdateMessagePreferenceRequest{
			DoNotDisturbStart: stringPtr(time),
		})
		if !errors.Is(err, ErrMessagePreferenceInvalidTime) {
			t.Errorf("期望时间 %q 无效，实际错误: %v", time, err)
		}
	}
}

func TestUpdate_DoNotDisturbTimeValidation_EmptyAllowed(t *testing.T) {
	setupPreferenceTest(t)
	svc := NewMessagePreferenceService()

	_, err := svc.Update(t.Context(), "user1", &UpdateMessagePreferenceRequest{
		DoNotDisturbStart: stringPtr(""),
		DoNotDisturbEnd:   stringPtr(""),
	})
	if err != nil {
		t.Errorf("期望空字符串有效，实际错误: %v", err)
	}

	_, err = svc.Update(t.Context(), "user1", &UpdateMessagePreferenceRequest{
		DoNotDisturbStart: stringPtr("  "),
	})
	if err != nil {
		t.Errorf("期望空白字符串有效（会被清理），实际错误: %v", err)
	}
}

func TestUpdate_PartialUpdate(t *testing.T) {
	client := setupPreferenceTest(t)
	ctx := t.Context()

	_, _ = client.MessagePreference.Create().
		SetUserUID("user1").
		SetDesktopNotify(true).
		SetSoundNotify(false).
		SetSubscribeSystem(true).
		SetSubscribeBusiness(true).
		SetSubscribeTask(true).
		SetDoNotDisturbStart("22:00").
		SetDoNotDisturbEnd("07:00").
		Save(ctx)

	svc := NewMessagePreferenceService()
	result, err := svc.Update(ctx, "user1", &UpdateMessagePreferenceRequest{
		SoundNotify:   boolPtr(true),
		SubscribeTask: boolPtr(false),
	})
	if err != nil {
		t.Fatalf("期望成功，实际 %v", err)
	}
	if result.DesktopNotify != true {
		t.Errorf("期望 DesktopNotify 保持不变，实际 %v", result.DesktopNotify)
	}
	if result.SoundNotify != true {
		t.Errorf("期望 SoundNotify=true，实际 %v", result.SoundNotify)
	}
	if result.SubscribeTask != false {
		t.Errorf("期望 SubscribeTask=false，实际 %v", result.SubscribeTask)
	}
	if result.DoNotDisturbStart != "22:00" {
		t.Errorf("期望 DoNotDisturbStart 保持不变，实际 %v", result.DoNotDisturbStart)
	}
}

// ======================== entityToPreferenceResponse 测试 ========================

func TestEntityToPreferenceResponse_Nil(t *testing.T) {
	result := entityToPreferenceResponse(nil)
	if !result.DesktopNotify {
		t.Errorf("期望返回默认值，实际 DesktopNotify=%v", result.DesktopNotify)
	}
}

func TestEntityToPreferenceResponse_Valid(t *testing.T) {
	entity := &ent.MessagePreference{
		DesktopNotify:     false,
		SoundNotify:       true,
		SubscribeSystem:   true,
		SubscribeBusiness: false,
		SubscribeTask:     true,
		DoNotDisturbStart: "22:00",
		DoNotDisturbEnd:   "08:00",
	}
	result := entityToPreferenceResponse(entity)
	if result.DesktopNotify != false {
		t.Errorf("期望 DesktopNotify=false，实际 %v", result.DesktopNotify)
	}
	if result.SoundNotify != true {
		t.Errorf("期望 SoundNotify=true，实际 %v", result.SoundNotify)
	}
	if result.DoNotDisturbStart != "22:00" {
		t.Errorf("期望 DoNotDisturbStart=22:00，实际 %v", result.DoNotDisturbStart)
	}
}

// ======================== applyUpdateToDefaults 测试 ========================

func TestApplyUpdateToDefaults_NilRequest(t *testing.T) {
	def := defaultPreference()
	applyUpdateToDefaults(def, nil)
	if !def.DesktopNotify {
		t.Errorf("期望保持默认值，实际 DesktopNotify=%v", def.DesktopNotify)
	}
}

func TestApplyUpdateToDefaults_AppliesAllFields(t *testing.T) {
	def := defaultPreference()
	req := &UpdateMessagePreferenceRequest{
		DesktopNotify:     boolPtr(false),
		SoundNotify:       boolPtr(true),
		SubscribeSystem:   boolPtr(false),
		SubscribeBusiness: boolPtr(false),
		SubscribeTask:     boolPtr(false),
		DoNotDisturbStart: stringPtr("20:00"),
		DoNotDisturbEnd:   stringPtr("06:00"),
	}
	applyUpdateToDefaults(def, req)
	if def.DesktopNotify != false {
		t.Errorf("期望 DesktopNotify=false，实际 %v", def.DesktopNotify)
	}
	if def.SoundNotify != true {
		t.Errorf("期望 SoundNotify=true，实际 %v", def.SoundNotify)
	}
	if def.SubscribeSystem != false {
		t.Errorf("期望 SubscribeSystem=false，实际 %v", def.SubscribeSystem)
	}
	if def.DoNotDisturbStart != "20:00" {
		t.Errorf("期望 DoNotDisturbStart=20:00，实际 %v", def.DoNotDisturbStart)
	}
	if def.DoNotDisturbEnd != "06:00" {
		t.Errorf("期望 DoNotDisturbEnd=06:00，实际 %v", def.DoNotDisturbEnd)
	}
}

func stringPtr(v string) *string {
	return &v
}
