package service

import (
	"context"
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupBroadcastTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:broadcast?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

type mockMessageService struct {
	broadcastFn func(ctx context.Context, uids []string, msg PublishMessage) (int, error)
}

func (m *mockMessageService) Create(ctx context.Context, req *CreateMessageRequest) (*MessageResponse, error) {
	return nil, nil
}
func (m *mockMessageService) Get(ctx context.Context, uid string, id int) (*MessageResponse, error) {
	return nil, nil
}
func (m *mockMessageService) List(ctx context.Context, uid string, req *ListMessageRequest) (*ListResponse[*MessageResponse], error) {
	return nil, nil
}
func (m *mockMessageService) MarkRead(ctx context.Context, uid string, ids []int) (int, error) {
	return 0, nil
}
func (m *mockMessageService) MarkUnread(ctx context.Context, uid string, ids []int) (int, error) {
	return 0, nil
}
func (m *mockMessageService) MarkAllRead(ctx context.Context, uid string) (int, error) { return 0, nil }
func (m *mockMessageService) UnreadCount(ctx context.Context, uid string) (*UnreadCountResponse, error) {
	return nil, nil
}
func (m *mockMessageService) Delete(ctx context.Context, uid string, id int) error { return nil }
func (m *mockMessageService) DeleteBatch(ctx context.Context, uid string, ids []int) (int, error) {
	return 0, nil
}
func (m *mockMessageService) Clear(ctx context.Context, uid string, req *ClearMessagesRequest) (int, error) {
	return 0, nil
}
func (m *mockMessageService) ListSinceID(ctx context.Context, uid string, lastID int, limit int) ([]*MessageResponse, error) {
	return nil, nil
}
func (m *mockMessageService) PublishToUser(ctx context.Context, uid string, msg PublishMessage) (*MessageResponse, error) {
	return nil, nil
}
func (m *mockMessageService) Broadcast(ctx context.Context, msg PublishMessage) (*MessageResponse, error) {
	return nil, nil
}
func (m *mockMessageService) BroadcastToUsers(ctx context.Context, uids []string, msg PublishMessage) (int, error) {
	if m.broadcastFn != nil {
		return m.broadcastFn(ctx, uids, msg)
	}
	return len(uids), nil
}

func TestDedupNonEmpty_Basic(t *testing.T) {
	input := []string{"a", "b", "a", "c", "", "  ", "b"}
	result := dedupNonEmpty(input)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
}

func TestResolveTargetUIDs_NilTarget(t *testing.T) {
	svc := NewMessageBroadcastService(&mockMessageService{})
	_, err := svc.resolveTargetUIDs(context.Background(), nil)
	if !errors.Is(err, ErrBroadcastInvalidScope) {
		t.Fatalf("expected ErrBroadcastInvalidScope, got %v", err)
	}
}

func TestResolveAllUIDs_EmptyDB(t *testing.T) {
	setupBroadcastTest(t)
	svc := NewMessageBroadcastService(&mockMessageService{})
	result, err := svc.resolveAllUIDs(t.Context())
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty, got %v", result)
	}
}

func TestBroadcast_NilRequest(t *testing.T) {
	svc := NewMessageBroadcastService(&mockMessageService{})
	_, err := svc.Broadcast(context.Background(), "default", nil)
	if !errors.Is(err, ErrMessageInvalidReq) {
		t.Fatalf("expected ErrMessageInvalidReq, got %v", err)
	}
}

func TestBroadcast_RequiresDomain(t *testing.T) {
	svc := NewMessageBroadcastService(&mockMessageService{})
	_, err := svc.Broadcast(context.Background(), "", &BroadcastRequest{})
	if !errors.Is(err, ErrPermissionOperatorRequired) {
		t.Fatalf("expected ErrPermissionOperatorRequired, got %v", err)
	}
}

func TestBroadcast_Success(t *testing.T) {
	client := setupBroadcastTest(t)
	ctx := t.Context()
	_, _ = client.Admin.Create().SetUID("admin1").SetUsername("admin1").SetEmail("a1@test.com").SetPassword("p").SetPasswordSalt("s").SetStatus(admin.StatusActive).Save(ctx)

	mockMsgSvc := &mockMessageService{}
	svc := NewMessageBroadcastService(mockMsgSvc)

	resp, err := svc.Broadcast(ctx, "default", &BroadcastRequest{
		Type:    MessageTypeSystem,
		Level:   MessageLevelInfo,
		Title:   "test",
		Content: "test",
		Target:  BroadcastTarget{Scope: BroadcastScopeAll},
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if resp.Delivered != 1 {
		t.Fatalf("expected 1, got %d", resp.Delivered)
	}
}

func TestBroadcast_RoleTargetsUseDatabaseAssignments(t *testing.T) {
	client := setupBroadcastTest(t)
	ctx := t.Context()

	r := client.Role.Create().
		SetName("editor").
		SetDisplayName("Editor").
		SetStatus(role.StatusActive).
		SaveX(ctx)
	assignedUser := client.User.Create().
		SetUID("assigned-user").
		SetEmail("assigned-user@test.com").
		SetUsername("assigned-user").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(user.StatusActive).
		SaveX(ctx)
	assignedAdmin := client.Admin.Create().
		SetUID("assigned-admin").
		SetEmail("assigned-admin@test.com").
		SetUsername("assigned-admin").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.UserRole.Create().SetUserID(assignedUser.ID).SetRoleID(r.ID).SaveX(ctx)
	client.AdminRole.Create().SetAdminID(assignedAdmin.ID).SetRoleID(r.ID).SaveX(ctx)
	client.CasbinRule.Create().
		SetPtype("g").
		SetV0("stale-casbin-user").
		SetV1(r.Name).
		SetV2("default").
		SaveX(ctx)

	var delivered []string
	svc := NewMessageBroadcastService(&mockMessageService{broadcastFn: func(_ context.Context, uids []string, _ PublishMessage) (int, error) {
		delivered = append([]string(nil), uids...)
		return len(uids), nil
	}})
	resp, err := svc.Broadcast(ctx, "default", &BroadcastRequest{
		Type: MessageTypeSystem, Level: MessageLevelInfo, Title: "test", Content: "test",
		Target: BroadcastTarget{Scope: BroadcastScopeRoles, RoleCodes: []string{r.Name}},
	})
	if err != nil {
		t.Fatalf("Broadcast failed: %v", err)
	}
	if resp.Delivered != 2 || len(delivered) != 2 {
		t.Fatalf("delivered = %v, response = %+v", delivered, resp)
	}
	got := map[string]bool{delivered[0]: true, delivered[1]: true}
	if !got[assignedUser.UID] || !got[assignedAdmin.UID] || got["stale-casbin-user"] {
		t.Fatalf("role recipients = %v", delivered)
	}
}
