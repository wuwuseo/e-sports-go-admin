# Admin Deduplication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the approved high-value duplication while preserving every public response, error, log, side effect, and security check order.

**Architecture:** Keep all helpers private and colocated with existing code. Extract only stable domain logic: encrypted-login validation, system-permission lookup, JWT token-pair creation, and role status mutation; make seed data local and data-driven; reuse two existing captcha helpers.

**Tech Stack:** Go 1.25.1, Fiber v3, Ent v0.14.5, golang-jwt/jwt v5, standard `testing` package.

## Global Constraints

- Work in the current `master` checkout as explicitly requested; do not create a worktree.
- Add no dependencies, public APIs, generic CRUD framework, or transaction framework.
- Do not edit generated files under `internal/ent`.
- Preserve exact error text, nonce-consumption order, token secrets/TTLs, logs, and side effects.
- Run `gofmt` on every changed Go file.
- Commit each task separately using Conventional Commits.

---

### Task 1: Share encrypted-login credential validation

**Files:**
- Create: `internal/handler/login_credentials_test.go`
- Modify: `internal/handler/public.go:78-141`
- Modify: `internal/handler/admin_auth.go:29-86`

**Interfaces:**
- Consumes: existing `crypto.ValidateTimestamp`, `crypto.GetNonceStore`, and `crypto.GetKeyManager`.
- Produces: `decryptLoginCredentials(encryptedIdentity, encryptedPassword string, timestamp int64, nonce, keyID, identityField string) (string, string, error)`.

- [ ] **Step 1: Write the failing validation-order test**

```go
package handler

import (
	"strings"
	"testing"
	"time"
)

func TestDecryptLoginCredentialsValidationOrder(t *testing.T) {
	now := time.Now().Unix()
	tests := []struct {
		name      string
		timestamp int64
		nonce     string
		keyID     string
		want      string
	}{
		{name: "missing timestamp", nonce: "unused", keyID: "unused", want: "Missing timestamp"},
		{name: "expired timestamp", timestamp: now - 301, nonce: "unused", keyID: "unused", want: "Timestamp expired"},
		{name: "missing nonce", timestamp: now, keyID: "unused", want: "Missing nonce"},
		{name: "missing key", timestamp: now, nonce: "missing-key-" + t.Name(), want: "Missing key_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := decryptLoginCredentials("identity", "password", tt.timestamp, tt.nonce, tt.keyID, "email")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDecryptLoginCredentialsConsumesNonceBeforeKeyValidation(t *testing.T) {
	nonce := "consumed-" + t.Name()
	_, _, first := decryptLoginCredentials("identity", "password", time.Now().Unix(), nonce, "", "email")
	_, _, second := decryptLoginCredentials("identity", "password", time.Now().Unix(), nonce, "", "email")
	if first == nil || first.Error() != "Missing key_id" {
		t.Fatalf("first error = %v", first)
	}
	if second == nil || !strings.Contains(second.Error(), "Nonce already used") {
		t.Fatalf("second error = %v", second)
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `go test ./internal/handler -run TestDecryptLoginCredentials -count=1`

Expected: build failure containing `undefined: decryptLoginCredentials`.

- [ ] **Step 3: Add the minimal helper and replace both duplicated blocks**

Add to `public.go`:

```go
func decryptLoginCredentials(encryptedIdentity, encryptedPassword string, timestamp int64, nonce, keyID, identityField string) (string, string, error) {
	if timestamp == 0 {
		return "", "", fmt.Errorf("Missing timestamp")
	}
	if !crypto.ValidateTimestamp(timestamp, 5*time.Minute) {
		return "", "", fmt.Errorf("Timestamp expired")
	}
	if nonce == "" {
		return "", "", fmt.Errorf("Missing nonce")
	}
	if !crypto.GetNonceStore().Add(nonce) {
		return "", "", fmt.Errorf("Nonce already used")
	}
	if keyID == "" {
		return "", "", fmt.Errorf("Missing key_id")
	}
	identity, err := crypto.GetKeyManager().DecryptData(keyID, encryptedIdentity)
	if err != nil {
		return "", "", fmt.Errorf("Failed to decrypt %s: %w", identityField, err)
	}
	password, err := crypto.GetKeyManager().DecryptData(keyID, encryptedPassword)
	if err != nil {
		return "", "", fmt.Errorf("Failed to decrypt password: %w", err)
	}
	return identity, password, nil
}
```

Replace the user block with:

```go
if req.EncryptedEmail != "" && req.EncryptedPassword != "" {
	decryptedEmail, decryptedPassword, err := decryptLoginCredentials(
		req.EncryptedEmail, req.EncryptedPassword, req.Timestamp, req.Nonce, req.KeyID, "email",
	)
	if err != nil {
		return response.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
	}
	req.Email = decryptedEmail
	req.Password = decryptedPassword
}
```

Replace the admin block with:

```go
if req.EncryptedUsername != "" && req.EncryptedPassword != "" {
	decryptedUsername, decryptedPassword, err := decryptLoginCredentials(
		req.EncryptedUsername, req.EncryptedPassword, req.Timestamp, req.Nonce, req.KeyID, "username",
	)
	if err != nil {
		return response.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
	}
	req.Email = decryptedUsername
	req.Password = decryptedPassword
}
```

Remove now-unused `time` and `crypto` imports from `admin_auth.go` only.

- [ ] **Step 4: Format and verify GREEN**

Run: `gofmt -w internal/handler/public.go internal/handler/admin_auth.go internal/handler/login_credentials_test.go`

Run: `go test ./internal/handler -run TestDecryptLoginCredentials -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handler/public.go internal/handler/admin_auth.go internal/handler/login_credentials_test.go
git commit -m "refactor(auth): share encrypted login validation"
```

### Task 2: Share super-admin system-permission lookup

**Files:**
- Modify: `internal/service/permission_menu_superadmin_test.go`
- Modify: `internal/service/permission_menu_superadmin.go`

**Interfaces:**
- Consumes: `PermissionTypeMenu`, `PermissionTypeButton`, and `systemPermissionObject`.
- Produces: `getEnabledSystemPermissions(ctx context.Context, domain string, permissionType PermissionType) ([]*PermissionResponse, error)`; existing wrappers remain unchanged.

- [ ] **Step 1: Add a failing direct-helper test**

```go
func TestGetEnabledSystemPermissionsFiltersRequestedType(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()
	createSystemPermission(t, ctx, client, "菜单权限", "admin", string(PermissionTypeMenu), 1, false)
	createSystemPermission(t, ctx, client, "按钮权限", "admin", string(PermissionTypeButton), 1, false)

	permissions, err := getEnabledSystemPermissions(ctx, "", PermissionTypeMenu)
	if err != nil {
		t.Fatalf("getEnabledSystemPermissions() error = %v", err)
	}
	if len(permissions) != 1 || permissions[0].Type != PermissionTypeMenu {
		t.Fatalf("permissions = %#v", permissions)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/service -run TestGetEnabledSystemPermissionsFiltersRequestedType -count=1`

Expected: build failure containing `undefined: getEnabledSystemPermissions`.

- [ ] **Step 3: Implement one shared query and thin wrappers**

```go
func getEnabledSystemMenuPermissions(ctx context.Context, domain string) ([]*PermissionResponse, error) {
	return getEnabledSystemPermissions(ctx, domain, PermissionTypeMenu)
}

func getEnabledSystemButtonPermissions(ctx context.Context, domain string) ([]*PermissionResponse, error) {
	return getEnabledSystemPermissions(ctx, domain, PermissionTypeButton)
}

func getEnabledSystemPermissions(ctx context.Context, domain string, permissionType PermissionType) ([]*PermissionResponse, error) {
	if global.ORM == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	label := map[PermissionType]string{PermissionTypeMenu: "菜单", PermissionTypeButton: "按钮"}[permissionType]
	query := global.ORM.SystemPermission.Query().Where(
		systempermission.TypeEQ(string(permissionType)),
		systempermission.StatusEQ(1),
	)
	if domain != "" {
		query = query.Where(systempermission.Or(systempermission.DomainEQ(domain), systempermission.IsCommonEQ(true)))
	}
	permissions, err := query.Order(ent.Asc(systempermission.FieldSortOrder), ent.Asc(systempermission.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取系统%s权限失败: %w", label, err)
	}
	seen := make(map[string]struct{})
	data := make([]*PermissionResponse, 0, len(permissions))
	for _, p := range permissions {
		object := systemPermissionObject(p)
		if _, exists := seen[object]; exists {
			continue
		}
		seen[object] = struct{}{}
		data = append(data, &PermissionResponse{
			ID: p.ID, Domain: p.Domain, Type: permissionType, Object: object,
			Action: string(permissionType), Description: p.Description,
		})
	}
	sort.Slice(data, func(i, j int) bool { return data[i].Object < data[j].Object })
	return data, nil
}
```

- [ ] **Step 4: Format and run the complete permission helper suite**

Run: `gofmt -w internal/service/permission_menu_superadmin.go internal/service/permission_menu_superadmin_test.go`

Run: `go test ./internal/service -run 'TestGetEnabledSystem|TestSystemPermissionObject' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/permission_menu_superadmin.go internal/service/permission_menu_superadmin_test.go
git commit -m "refactor(permission): share system permission lookup"
```

### Task 3: Share JWT token-pair creation

**Files:**
- Create: `internal/service/token_issuer_test.go`
- Modify: `internal/service/token_issuer.go`
- Modify: `internal/service/user.go`
- Modify: `internal/service/admin_service.go`

**Interfaces:**
- Consumes: existing `TokenResponse` and `cmfJwt.CreateToken`.
- Produces: `createTokenPair(id, subject, issuer, secret string, accessTTL, refreshTTL time.Duration, now func() time.Time) (*TokenResponse, error)`.

- [ ] **Step 1: Add a failing token helper test**

```go
package service

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestCreateTokenPairUsesSeparateAccessAndRefreshTimes(t *testing.T) {
	base := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	calls := 0
	now := func() time.Time {
		value := base.Add(time.Duration(calls) * time.Second)
		calls++
		return value
	}
	pair, err := createTokenPair("uid", "subject", "issuer", "secret", time.Hour, 2*time.Hour, now)
	if err != nil {
		t.Fatalf("createTokenPair() error = %v", err)
	}
	if calls != 4 || pair.ExpiresIn != 3600 || pair.TokenType != "Bearer" {
		t.Fatalf("calls=%d pair=%+v", calls, pair)
	}
	for _, tt := range []struct {
		raw       string
		tokenType string
		expiresAt time.Time
		issuedAt  time.Time
	}{
		{raw: pair.AccessToken, tokenType: "access", expiresAt: base.Add(time.Hour), issuedAt: base.Add(time.Second)},
		{raw: pair.RefreshToken, tokenType: "refresh", expiresAt: base.Add(2*time.Second + 2*time.Hour), issuedAt: base.Add(3 * time.Second)},
	} {
		token, err := jwt.Parse(tt.raw, func(*jwt.Token) (any, error) { return []byte("secret"), nil }, jwt.WithoutClaimsValidation())
		if err != nil || !token.Valid {
			t.Fatalf("Parse() token=%v error=%v", token, err)
		}
		claims := token.Claims.(jwt.MapClaims)
		expiresAt, _ := claims.GetExpirationTime()
		issuedAt, _ := claims.GetIssuedAt()
		if claims["token_type"] != tt.tokenType || !expiresAt.Time.Equal(tt.expiresAt) || !issuedAt.Time.Equal(tt.issuedAt) {
			t.Fatalf("claims = %#v", claims)
		}
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/service -run TestCreateTokenPair -count=1`

Expected: build failure containing `undefined: createTokenPair`.

- [ ] **Step 3: Implement the private helper**

```go
func createTokenPair(id, subject, issuer, secret string, accessTTL, refreshTTL time.Duration, now func() time.Time) (*TokenResponse, error) {
	create := func(tokenType string, ttl time.Duration) (string, error) {
		expiresAt := now().Add(ttl)
		issuedAt := now()
		return cmfJwt.CreateToken(jwt.MapClaims{
			"id": id, "subject": subject, "issuer": issuer, "domain": "default",
			"expires": jwt.NewNumericDate(expiresAt), "issued_at": jwt.NewNumericDate(issuedAt), "token_type": tokenType,
		}, secret)
	}
	accessToken, err := create("access", accessTTL)
	if err != nil {
		return nil, err
	}
	refreshToken, err := create("refresh", refreshTTL)
	if err != nil {
		return nil, err
	}
	return &TokenResponse{
		AccessToken: accessToken, TokenType: "Bearer",
		ExpiresIn: int64(accessTTL / time.Second), RefreshToken: refreshToken,
	}, nil
}
```

- [ ] **Step 4: Replace all four token-pair blocks**

In `UserService.Login`, replace only token construction and the final response with:

```go
configService, _ := global.Bootstrap.GetService("config")
config := configService.(*cmfConfig.Config)
tokens, err := createTokenPair(
	 u.UID, u.Username, config.App.Name, config.App.Secret,
	 time.Duration(config.App.LoginExpires)*time.Second,
	 time.Duration(config.App.RefreshExpires)*time.Second,
	 time.Now,
)
if err != nil {
	return nil, err
}
if _, err = s.client.User.UpdateOne(u).SetLastLogin(time.Now()).Save(ctx); err != nil {
	return nil, err
}
return tokens, nil
```

In `AdminService.Login`, use:

```go
cfg, _ := global.Bootstrap.GetService("config")
config := cfg.(*cmfConfig.Config)
tokens, err := createTokenPair(
	a.UID, a.Username, config.App.Name, config.App.AdminSecret,
	time.Duration(config.App.AdminLoginExpires)*time.Second,
	time.Duration(config.App.AdminRefreshExpires)*time.Second,
	time.Now,
)
if err != nil {
	return nil, err
}
if _, err = s.client.Admin.UpdateOne(a).SetLastLogin(time.Now()).Save(ctx); err != nil {
	return nil, err
}
if s.blacklistSvc != nil {
	_ = s.blacklistSvc.ClearLogout(ctx, a.UID)
}
return tokens, nil
```

In `issueUserToken`, after the existing query/status checks, use:

```go
configService, _ := global.Bootstrap.GetService("config")
config := configService.(*cmfConfig.Config)
tokens, err := createTokenPair(
	 u.UID, u.Username, config.App.Name, config.App.Secret,
	 time.Duration(config.App.LoginExpires)*time.Second,
	 time.Duration(config.App.RefreshExpires)*time.Second,
	 func() time.Time { return now },
)
if err != nil {
	return nil, err
}
if _, err := client.User.UpdateOne(u).SetLastLogin(now).Save(ctx); err != nil {
	return nil, err
}
return tokens, nil
```

In `issueAdminToken`, use:

```go
configService, _ := global.Bootstrap.GetService("config")
config := configService.(*cmfConfig.Config)
tokens, err := createTokenPair(
	a.UID, a.Username, config.App.Name, config.App.AdminSecret,
	time.Duration(config.App.AdminLoginExpires)*time.Second,
	time.Duration(config.App.AdminRefreshExpires)*time.Second,
	func() time.Time { return now },
)
if err != nil {
	return nil, err
}
if _, err := client.Admin.UpdateOne(a).SetLastLogin(now).Save(ctx); err != nil {
	return nil, err
}
return tokens, nil
```

- [ ] **Step 5: Format and verify**

Run: `gofmt -w internal/service/token_issuer.go internal/service/token_issuer_test.go internal/service/user.go internal/service/admin_service.go`

Run: `go test ./internal/service -run 'TestCreateTokenPair|TestOAuthAuthService|TestUserService|TestAdminService' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/service/token_issuer.go internal/service/token_issuer_test.go internal/service/user.go internal/service/admin_service.go
git commit -m "refactor(auth): share token pair creation"
```

### Task 4: Share role status mutation

**Files:**
- Modify: `internal/service/role_test.go`
- Modify: `internal/service/role.go:432-511`

**Interfaces:**
- Consumes: `role.StatusActive`, `role.StatusInactive`, and existing role guards.
- Produces: `(*RoleService).setStatus(ctx context.Context, op *EffectiveDataScope, id int, target role.Status) error`; public `Enable` and `Disable` remain unchanged.

- [ ] **Step 1: Add a failing direct-helper test**

```go
func TestRoleServiceSetStatusUsesTargetStatus(t *testing.T) {
	client := setupTestClient(t)
	svc := NewRoleService(nil)
	r := client.Role.Create().
		SetName("set_status").
		SetDisplayName("set status").
		SetStatus(role.StatusInactive).
		SaveX(t.Context())
	if err := svc.setStatus(t.Context(), nil, r.ID, role.StatusActive); err != nil {
		t.Fatalf("setStatus() error = %v", err)
	}
	if got := client.Role.GetX(t.Context(), r.ID).Status; got != role.StatusActive {
		t.Fatalf("status = %s", got)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/service -run TestRoleServiceSetStatusUsesTargetStatus -count=1`

Expected: build failure containing `svc.setStatus undefined`.

- [ ] **Step 3: Replace both methods with wrappers and one implementation**

```go
func (s *RoleService) Enable(ctx context.Context, op *EffectiveDataScope, id int) error {
	return s.setStatus(ctx, op, id, role.StatusActive)
}

func (s *RoleService) Disable(ctx context.Context, op *EffectiveDataScope, id int) error {
	return s.setStatus(ctx, op, id, role.StatusInactive)
}
```

Add the complete shared implementation:

```go
func (s *RoleService) setStatus(ctx context.Context, op *EffectiveDataScope, id int, target role.Status) error {
	r, err := s.Client.Role.Query().Where(role.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}
	if err := ensureRoleNotHeldByOperator(ctx, s.Client, op, id); err != nil {
		return err
	}
	if r.IsSystem {
		return fmt.Errorf("系统内置角色不允许修改状态")
	}
	if r.Status == target {
		return nil
	}
	if err := ensureRoleScopeWritable(op, DataScope(r.DataScope), r.DataScopeDepartmentIds); err != nil {
		return err
	}
	if err := ensureRoleManageableForAssignedAdmins(ctx, s.Client, s.deptSvc, op, r, "", nil, false); err != nil {
		return err
	}
	action := "启用"
	if target == role.StatusInactive {
		action = "禁用"
	}
	if _, err := s.Client.Role.UpdateOne(r).SetStatus(target).Save(ctx); err != nil {
		return fmt.Errorf("%s角色失败: %w", action, err)
	}
	s.clearUserPermissionsByRoleID(ctx, id, "default")
	return nil
}
```

- [ ] **Step 4: Format and run role tests**

Run: `gofmt -w internal/service/role.go internal/service/role_test.go`

Run: `go test ./internal/service -run 'TestRoleService_(Enable|Disable|SetStatus)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/service/role.go internal/service/role_test.go
git commit -m "refactor(role): share status mutation"
```

### Task 5: Make dictionary seed initialization data-driven

**Files:**
- Modify: `internal/init_test.go`
- Modify: `internal/init.go:455-559`

**Interfaces:**
- Consumes: existing `global.ORM`, `appInstance.Logger`, and Ent `DictData` builders.
- Produces: no new function or public interface; one local seed slice and one loop.

- [ ] **Step 1: Add a characterization test**

Add `go-admin/internal/ent/dictdata` to the test imports and add:

```go
func TestInitDictSeedDataCreatesBothGroupsIdempotently(t *testing.T) {
	client := setupInitTest(t)
	for range 2 {
		if err := initDictSeedData(); err != nil {
			t.Fatalf("initDictSeedData() error = %v", err)
		}
	}
	for _, tt := range []struct {
		dictType string
		want     int
	}{
		{dictType: "sys_oper_log_module", want: 7},
		{dictType: "sys_config_group", want: 2},
	} {
		count, err := client.DictData.Query().Where(dictdata.DictTypeEQ(tt.dictType)).Count(t.Context())
		if err != nil {
			t.Fatalf("count %s: %v", tt.dictType, err)
		}
		if count != tt.want {
			t.Fatalf("count %s = %d, want %d", tt.dictType, count, tt.want)
		}
	}
}
```

- [ ] **Step 2: Establish the green behavior baseline and structural RED**

Run: `go test ./internal -run TestInitDictSeedData -count=1`

Expected: PASS.

Run the existing dupl command and confirm it reports `internal/init.go:477-510` duplicated by `522-555`.

- [ ] **Step 3: Replace both loops with one local data slice**

Replace `initDictSeedData` with:

```go
func initDictSeedData() error {
	ctx := context.Background()
	client := global.ORM
	appInstance.Logger.Info("initializing dict seed data...")
	items := []struct {
		dictType string
		label    string
		value    string
		sort     int
	}{
		{"sys_oper_log_module", "认证", "auth", 1},
		{"sys_oper_log_module", "用户", "user", 2},
		{"sys_oper_log_module", "角色", "role", 3},
		{"sys_oper_log_module", "权限", "permission", 4},
		{"sys_oper_log_module", "附件", "attachment", 5},
		{"sys_oper_log_module", "定时任务", "cron", 6},
		{"sys_oper_log_module", "系统", "system", 7},
		{"sys_config_group", "基本配置", "basic", 1},
		{"sys_config_group", "附件配置", "attachment", 2},
	}
	for _, item := range items {
		subject := item.value
		if item.dictType == "sys_config_group" {
			subject = "config group " + subject
		}
		exists, err := client.DictData.Query().Where(
			dictdata.DictTypeEQ(item.dictType),
			dictdata.ValueEQ(item.value),
		).Exist(ctx)
		if err != nil {
			return fmt.Errorf("failed to query dict data for %s: %w", subject, err)
		}
		if exists {
			appInstance.Logger.Warn("dict data already exists, skipping",
				zap.String("dict_type", item.dictType), zap.String("value", item.value))
			continue
		}
		if _, err := client.DictData.Create().
			SetLabel(item.label).
			SetValue(item.value).
			SetDictType(item.dictType).
			SetSort(item.sort).
			SetStatus(1).
			SetIsDefault(false).
			Save(ctx); err != nil {
			return fmt.Errorf("failed to create dict data for %s: %w", subject, err)
		}
		appInstance.Logger.Info("dict data created",
			zap.String("dict_type", item.dictType),
			zap.String("label", item.label),
			zap.String("value", item.value))
	}
	appInstance.Logger.Info("dict seed data initialization completed")
	return nil
}
```

- [ ] **Step 4: Format and verify behavior and structure**

Run: `gofmt -w internal/init.go internal/init_test.go`

Run: `go test ./internal -run TestInitDictSeedData -count=1`

Expected: PASS.

Rerun dupl and confirm no issue references the two former `init.go` loop ranges.

- [ ] **Step 5: Commit**

```bash
git add internal/init.go internal/init_test.go
git commit -m "refactor(init): deduplicate dictionary seeds"
```

### Task 6: Reuse existing captcha helpers

**Files:**
- Modify: `internal/captcha/runtime.go:55-66`
- Modify: `internal/captcha/settings.go:253-256`

**Interfaces:**
- Consumes: existing `NewSecretManager` and `cloneConfig`.
- Produces: no new interface.

- [ ] **Step 1: Run the existing captcha tests before refactoring**

Run: `go test ./internal/captcha -count=1`

Expected: PASS.

- [ ] **Step 2: Apply the two minimal substitutions**

In `NewRuntime`, retain the active-version gate, call `NewSecretManager(client, lookup)`, and wrap any error with `configure captcha master key: %w`. Remove only the now-unused local `repository` variable; keep the `secureconfig` import because `Runtime.Secrets` and `ActiveVersionEnv` still use it.

In `prepareProviderConfig`, replace the manual map-copy loop with `merged := cloneConfig(publicConfig)`.

The exact runtime block is:

```go
var secrets *secureconfig.Manager
if lookup != nil && strings.TrimSpace(lookup(secureconfig.ActiveVersionEnv)) != "" {
	secrets, err = NewSecretManager(client, lookup)
	if err != nil {
		return nil, fmt.Errorf("configure captcha master key: %w", err)
	}
}
```

The exact settings replacement is:

```go
merged := cloneConfig(publicConfig)
```

- [ ] **Step 3: Format and verify**

Run: `gofmt -w internal/captcha/runtime.go internal/captcha/settings.go`

Run: `go test ./internal/captcha -count=1`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/captcha/runtime.go internal/captcha/settings.go
git commit -m "refactor(captcha): reuse configuration helpers"
```

### Task 7: Final verification

**Files:**
- Verify all files changed by Tasks 1-6.

**Interfaces:**
- Consumes: all task outputs.
- Produces: a verified, behavior-preserving refactor.

- [ ] **Step 1: Run focused packages**

Run: `go test ./internal/handler ./internal/service ./internal/captcha ./internal -count=1`

Expected: all four packages PASS.

- [ ] **Step 2: Run the full suite**

Run: `go test ./... -count=1`

Expected: exit code 0; every package PASS or reports `[no test files]`.

- [ ] **Step 3: Run formatting and duplicate checks**

Run: `git diff --check HEAD~6..HEAD`

Expected: no output.

Run the existing read-only dupl command with the workspace-local Go and golangci caches. Confirm the selected login, permission, token, role, and init clones are absent; intentional CRUD/Cron clones may remain.

- [ ] **Step 4: Inspect final scope**

Run: `git status --short` and `git diff --stat 7b0ddf1..HEAD`.

Expected: clean working tree; only the approved design, plan, test baseline commit, and six focused refactor commits are present.
