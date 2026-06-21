package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminthirdpartyuser"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	appoauth "go-admin/internal/oauth"
	"go-admin/internal/utils"
)

var (
	ErrOAuthIdentityBound    = errors.New("OAuth identity is already bound")
	ErrOAuthAdminNotBound    = errors.New("administrator OAuth identity is not bound")
	ErrOAuthProviderMismatch = errors.New("OAuth provider does not match session")
)

type OAuthBeginResult struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"-"`
}

type OAuthCallbackResult struct {
	SubjectType string
	FlowType    string
	RedirectURL string
	LoginCode   string
}

type OAuthAuthService struct {
	client         *ent.Client
	registry       *appoauth.Registry
	configService  *OAuthProviderConfigService
	sessionService *OAuthSessionService
	now            func() time.Time
}

func NewOAuthAuthService(client *ent.Client, registry *appoauth.Registry, configService *OAuthProviderConfigService, sessionService *OAuthSessionService, now func() time.Time) *OAuthAuthService {
	if now == nil {
		now = time.Now
	}
	return &OAuthAuthService{client: client, registry: registry, configService: configService, sessionService: sessionService, now: now}
}

func (s *OAuthAuthService) Begin(ctx context.Context, providerName, subjectType, flowType, subjectUID string) (*OAuthBeginResult, error) {
	provider, ok := s.registry.Get(providerName)
	if !ok {
		return nil, ErrOAuthProviderNotFound
	}
	runtimeConfig, _, err := s.configService.Runtime(ctx, providerName, subjectType)
	if err != nil {
		return nil, err
	}
	if flowType == "bind" && subjectUID == "" {
		return nil, ErrOAuthSessionInvalid
	}
	started, err := s.sessionService.Start(ctx, StartOAuthSessionRequest{
		Provider: provider.Name(), SubjectType: subjectType, FlowType: flowType, SubjectUID: subjectUID,
	})
	if err != nil {
		return nil, err
	}
	return &OAuthBeginResult{
		AuthorizationURL: provider.AuthCodeURL(runtimeConfig, started.State, started.Verifier),
		State:            started.State,
	}, nil
}

func (s *OAuthAuthService) HandleCallback(ctx context.Context, providerName, state, code string) (*OAuthCallbackResult, error) {
	claimed, err := s.sessionService.ConsumeCallback(ctx, state)
	if err != nil {
		return nil, err
	}
	if claimed.Provider != strings.ToLower(providerName) {
		return nil, ErrOAuthProviderMismatch
	}
	provider, ok := s.registry.Get(providerName)
	if !ok {
		return nil, ErrOAuthProviderNotFound
	}
	runtimeConfig, redirectURL, err := s.configService.Runtime(ctx, providerName, claimed.SubjectType)
	if err != nil {
		return nil, err
	}
	result := &OAuthCallbackResult{SubjectType: claimed.SubjectType, FlowType: claimed.FlowType, RedirectURL: redirectURL}
	providerToken, err := provider.Exchange(ctx, runtimeConfig, code, claimed.Verifier)
	if err != nil {
		_ = s.sessionService.FailCallback(ctx, claimed.ID, "provider_exchange_failed")
		return result, err
	}
	identity, err := provider.FetchIdentity(ctx, providerToken)
	if err != nil {
		_ = s.sessionService.FailCallback(ctx, claimed.ID, "provider_identity_failed")
		return result, err
	}
	if identity == nil || identity.ID == "" || identity.Email == "" || !identity.EmailVerified {
		_ = s.sessionService.FailCallback(ctx, claimed.ID, "verified_email_required")
		return result, appoauth.ErrVerifiedEmailRequired
	}

	if claimed.FlowType == "bind" {
		switch claimed.SubjectType {
		case "user":
			err = s.bindUserIdentity(ctx, claimed.SubjectUID, provider.Name(), identity)
		case "admin":
			err = s.bindAdminIdentity(ctx, claimed.SubjectUID, provider.Name(), identity)
		default:
			err = ErrOAuthSessionInvalid
		}
		if err != nil {
			_ = s.sessionService.FailCallback(ctx, claimed.ID, "bind_failed")
			return result, err
		}
		if err := s.sessionService.CompleteBind(ctx, claimed.ID); err != nil {
			return result, err
		}
		return result, nil
	}

	var subjectUID string
	switch claimed.SubjectType {
	case "user":
		subjectUID, err = s.resolveUserLogin(ctx, provider.Name(), identity)
	case "admin":
		subjectUID, err = s.resolveAdminLogin(ctx, provider.Name(), identity)
	default:
		err = ErrOAuthSessionInvalid
	}
	if err != nil {
		_ = s.sessionService.FailCallback(ctx, claimed.ID, "login_failed")
		return result, err
	}
	result.LoginCode, err = s.sessionService.CompleteLogin(ctx, claimed.ID, subjectUID)
	if err != nil {
		return result, err
	}
	return result, nil
}

func (s *OAuthAuthService) HandleCallbackError(ctx context.Context, providerName, state, providerError string) (*OAuthCallbackResult, error) {
	claimed, err := s.sessionService.ConsumeCallback(ctx, state)
	if err != nil {
		return nil, err
	}
	if claimed.Provider != strings.ToLower(providerName) {
		return nil, ErrOAuthProviderMismatch
	}
	_, redirectURL, err := s.configService.Runtime(ctx, providerName, claimed.SubjectType)
	if err != nil {
		return nil, err
	}
	result := &OAuthCallbackResult{
		SubjectType: claimed.SubjectType,
		FlowType:    claimed.FlowType,
		RedirectURL: redirectURL,
	}
	if err := s.sessionService.FailCallback(ctx, claimed.ID, providerError); err != nil {
		return result, err
	}
	return result, errors.New("OAuth authorization was denied")
}

func (s *OAuthAuthService) ExchangeLoginCode(ctx context.Context, code string) (*TokenResponse, error) {
	grant, err := s.sessionService.ConsumeLoginCode(ctx, code)
	if err != nil {
		return nil, err
	}
	switch grant.SubjectType {
	case "user":
		return issueUserToken(ctx, s.client, grant.SubjectUID, s.now())
	case "admin":
		return issueAdminToken(ctx, s.client, grant.SubjectUID, s.now())
	default:
		return nil, ErrOAuthLoginCodeInvalid
	}
}

func (s *OAuthAuthService) resolveUserLogin(ctx context.Context, provider string, identity *appoauth.Identity) (string, error) {
	binding, err := s.client.ThirdPartyUser.Query().Where(
		thirdpartyuser.PlatformEQ(provider), thirdpartyuser.OpenidEQ(identity.ID),
	).Only(ctx)
	if err == nil {
		if binding.Status != thirdpartyuser.StatusActive {
			return "", ErrInvalidUserStatus
		}
		u, err := s.client.User.Query().Where(user.IDEQ(binding.UserID)).Only(ctx)
		if err != nil {
			return "", err
		}
		if u.Status != user.StatusActive {
			return "", ErrInvalidUserStatus
		}
		if err := s.syncUserIdentity(ctx, binding, identity); err != nil {
			return "", err
		}
		return u.UID, nil
	}
	if !ent.IsNotFound(err) {
		return "", err
	}

	u, err := s.client.User.Query().Where(user.EmailEqualFold(strings.TrimSpace(identity.Email))).Only(ctx)
	if ent.IsNotFound(err) {
		create := s.client.User.Create().
			SetEmail(strings.ToLower(identity.Email)).
			SetUID(utils.GenerateUID()).
			SetStatus(user.StatusActive)
		if identity.Username != "" {
			create.SetUsername(truncate(identity.Username, 255))
		}
		if identity.Name != "" {
			create.SetNickname(truncate(identity.Name, 255))
		}
		if identity.AvatarURL != "" {
			create.SetAvatar(truncate(identity.AvatarURL, 1024))
		}
		u, err = create.Save(ctx)
	}
	if err != nil {
		return "", err
	}
	if u.Status != user.StatusActive {
		return "", ErrInvalidUserStatus
	}
	if err := s.bindUserByID(ctx, u.ID, provider, identity); err != nil {
		return "", err
	}
	return u.UID, nil
}

func (s *OAuthAuthService) resolveAdminLogin(ctx context.Context, provider string, identity *appoauth.Identity) (string, error) {
	binding, err := s.client.AdminThirdPartyUser.Query().Where(
		adminthirdpartyuser.PlatformEQ(provider), adminthirdpartyuser.OpenidEQ(identity.ID),
	).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", ErrOAuthAdminNotBound
		}
		return "", err
	}
	if binding.Status != adminthirdpartyuser.StatusActive {
		return "", ErrInvalidAdminStatus
	}
	a, err := s.client.Admin.Query().Where(admin.IDEQ(binding.AdminID)).Only(ctx)
	if err != nil {
		return "", err
	}
	if a.Status != admin.StatusActive {
		return "", ErrInvalidAdminStatus
	}
	if err := s.syncAdminIdentity(ctx, binding, identity); err != nil {
		return "", err
	}
	return a.UID, nil
}

func (s *OAuthAuthService) bindUserIdentity(ctx context.Context, uid, provider string, identity *appoauth.Identity) error {
	u, err := s.client.User.Query().Where(user.UIDEQ(uid)).Only(ctx)
	if err != nil {
		return err
	}
	if u.Status != user.StatusActive {
		return ErrInvalidUserStatus
	}
	return s.bindUserByID(ctx, u.ID, provider, identity)
}

func (s *OAuthAuthService) bindUserByID(ctx context.Context, userID int, provider string, identity *appoauth.Identity) error {
	byIdentity, err := s.client.ThirdPartyUser.Query().Where(
		thirdpartyuser.PlatformEQ(provider), thirdpartyuser.OpenidEQ(identity.ID),
	).Only(ctx)
	if err == nil {
		if byIdentity.UserID != userID {
			return ErrOAuthIdentityBound
		}
		return s.syncUserIdentity(ctx, byIdentity, identity)
	}
	if !ent.IsNotFound(err) {
		return err
	}
	exists, err := s.client.ThirdPartyUser.Query().Where(
		thirdpartyuser.UserIDEQ(userID), thirdpartyuser.PlatformEQ(provider),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return ErrOAuthIdentityBound
	}
	create := s.client.ThirdPartyUser.Create().
		SetUserID(userID).
		SetPlatform(provider).
		SetPlatformName(s.providerDisplayName(provider)).
		SetOpenid(identity.ID).
		SetStatus(thirdpartyuser.StatusActive).
		SetLastSyncAt(s.now())
	applyUserIdentityCreate(create, identity)
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return ErrOAuthIdentityBound
		}
		return err
	}
	return nil
}

func (s *OAuthAuthService) bindAdminIdentity(ctx context.Context, uid, provider string, identity *appoauth.Identity) error {
	a, err := s.client.Admin.Query().Where(admin.UIDEQ(uid)).Only(ctx)
	if err != nil {
		return err
	}
	if a.Status != admin.StatusActive {
		return ErrInvalidAdminStatus
	}
	byIdentity, err := s.client.AdminThirdPartyUser.Query().Where(
		adminthirdpartyuser.PlatformEQ(provider), adminthirdpartyuser.OpenidEQ(identity.ID),
	).Only(ctx)
	if err == nil {
		if byIdentity.AdminID != a.ID {
			return ErrOAuthIdentityBound
		}
		return s.syncAdminIdentity(ctx, byIdentity, identity)
	}
	if !ent.IsNotFound(err) {
		return err
	}
	exists, err := s.client.AdminThirdPartyUser.Query().Where(
		adminthirdpartyuser.AdminIDEQ(a.ID), adminthirdpartyuser.PlatformEQ(provider),
	).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return ErrOAuthIdentityBound
	}
	create := s.client.AdminThirdPartyUser.Create().
		SetAdminID(a.ID).
		SetPlatform(provider).
		SetPlatformName(s.providerDisplayName(provider)).
		SetOpenid(identity.ID).
		SetStatus(adminthirdpartyuser.StatusActive).
		SetLastSyncAt(s.now())
	applyAdminIdentityCreate(create, identity)
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return ErrOAuthIdentityBound
		}
		return err
	}
	return nil
}

func (s *OAuthAuthService) syncUserIdentity(ctx context.Context, binding *ent.ThirdPartyUser, identity *appoauth.Identity) error {
	update := s.client.ThirdPartyUser.UpdateOne(binding).SetLastSyncAt(s.now())
	if identity.Name != "" {
		update.SetNickname(truncate(identity.Name, 255))
	}
	if identity.AvatarURL != "" {
		update.SetAvatarURL(truncate(identity.AvatarURL, 1024))
	}
	if identity.Extra != nil {
		update.SetExtraData(identity.Extra)
	}
	_, err := update.Save(ctx)
	return err
}

func (s *OAuthAuthService) syncAdminIdentity(ctx context.Context, binding *ent.AdminThirdPartyUser, identity *appoauth.Identity) error {
	update := s.client.AdminThirdPartyUser.UpdateOne(binding).SetLastSyncAt(s.now())
	if identity.Name != "" {
		update.SetNickname(truncate(identity.Name, 255))
	}
	if identity.AvatarURL != "" {
		update.SetAvatarURL(truncate(identity.AvatarURL, 1024))
	}
	if identity.Extra != nil {
		update.SetExtraData(identity.Extra)
	}
	_, err := update.Save(ctx)
	return err
}

func applyUserIdentityCreate(create *ent.ThirdPartyUserCreate, identity *appoauth.Identity) {
	if identity.Name != "" {
		create.SetNickname(truncate(identity.Name, 255))
	}
	if identity.AvatarURL != "" {
		create.SetAvatarURL(truncate(identity.AvatarURL, 1024))
	}
	if identity.Extra != nil {
		create.SetExtraData(identity.Extra)
	}
}

func applyAdminIdentityCreate(create *ent.AdminThirdPartyUserCreate, identity *appoauth.Identity) {
	if identity.Name != "" {
		create.SetNickname(truncate(identity.Name, 255))
	}
	if identity.AvatarURL != "" {
		create.SetAvatarURL(truncate(identity.AvatarURL, 1024))
	}
	if identity.Extra != nil {
		create.SetExtraData(identity.Extra)
	}
}

func truncate(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

func (s *OAuthAuthService) providerDisplayName(provider string) string {
	if registered, ok := s.registry.Get(provider); ok {
		return registered.DisplayName()
	}
	return provider
}

var ErrOAuthLastLoginMethod = errors.New("cannot remove the last available login method")

type OAuthIdentityResponse struct {
	ID          int        `json:"id"`
	Provider    string     `json:"provider"`
	DisplayName string     `json:"display_name"`
	OpenID      string     `json:"openid"`
	Nickname    string     `json:"nickname"`
	AvatarURL   string     `json:"avatar_url"`
	Status      string     `json:"status"`
	LastSyncAt  *time.Time `json:"last_sync_at,omitempty"`
}

func (s *OAuthAuthService) ListUserIdentities(ctx context.Context, uid string) ([]OAuthIdentityResponse, error) {
	u, err := s.client.User.Query().Where(user.UIDEQ(uid)).Only(ctx)
	if err != nil {
		return nil, err
	}
	identities, err := s.client.ThirdPartyUser.Query().Where(thirdpartyuser.UserIDEQ(u.ID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]OAuthIdentityResponse, 0, len(identities))
	for _, identity := range identities {
		result = append(result, OAuthIdentityResponse{
			ID: identity.ID, Provider: identity.Platform, DisplayName: identity.PlatformName,
			OpenID: identity.Openid, Nickname: identity.Nickname, AvatarURL: identity.AvatarURL,
			Status: string(identity.Status), LastSyncAt: identity.LastSyncAt,
		})
	}
	return result, nil
}

func (s *OAuthAuthService) ListAdminIdentities(ctx context.Context, uid string) ([]OAuthIdentityResponse, error) {
	a, err := s.client.Admin.Query().Where(admin.UIDEQ(uid)).Only(ctx)
	if err != nil {
		return nil, err
	}
	identities, err := s.client.AdminThirdPartyUser.Query().Where(adminthirdpartyuser.AdminIDEQ(a.ID)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]OAuthIdentityResponse, 0, len(identities))
	for _, identity := range identities {
		result = append(result, OAuthIdentityResponse{
			ID: identity.ID, Provider: identity.Platform, DisplayName: identity.PlatformName,
			OpenID: identity.Openid, Nickname: identity.Nickname, AvatarURL: identity.AvatarURL,
			Status: string(identity.Status), LastSyncAt: identity.LastSyncAt,
		})
	}
	return result, nil
}

func (s *OAuthAuthService) UnbindUser(ctx context.Context, uid, provider string) error {
	u, err := s.client.User.Query().Where(user.UIDEQ(uid)).Only(ctx)
	if err != nil {
		return err
	}
	identity, err := s.client.ThirdPartyUser.Query().Where(
		thirdpartyuser.UserIDEQ(u.ID), thirdpartyuser.PlatformEQ(strings.ToLower(provider)),
	).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrThirdPartyNotFound
		}
		return err
	}
	if u.Password == "" {
		count, err := s.client.ThirdPartyUser.Query().Where(
			thirdpartyuser.UserIDEQ(u.ID), thirdpartyuser.StatusEQ(thirdpartyuser.StatusActive),
		).Count(ctx)
		if err != nil {
			return err
		}
		if count <= 1 {
			return ErrOAuthLastLoginMethod
		}
	}
	return s.client.ThirdPartyUser.DeleteOne(identity).Exec(ctx)
}

func (s *OAuthAuthService) UnbindAdmin(ctx context.Context, uid, provider string) error {
	a, err := s.client.Admin.Query().Where(admin.UIDEQ(uid)).Only(ctx)
	if err != nil {
		return err
	}
	identity, err := s.client.AdminThirdPartyUser.Query().Where(
		adminthirdpartyuser.AdminIDEQ(a.ID), adminthirdpartyuser.PlatformEQ(strings.ToLower(provider)),
	).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrThirdPartyNotFound
		}
		return err
	}
	return s.client.AdminThirdPartyUser.DeleteOne(identity).Exec(ctx)
}

type AdminOAuthIdentityListItem struct {
	ID          int        `json:"id"`
	AdminID     int        `json:"admin_id"`
	AdminUID    string     `json:"admin_uid"`
	AdminEmail  string     `json:"admin_email"`
	AdminName   string     `json:"admin_name"`
	Provider    string     `json:"provider"`
	DisplayName string     `json:"display_name"`
	OpenID      string     `json:"openid"`
	Nickname    string     `json:"nickname"`
	AvatarURL   string     `json:"avatar_url"`
	Status      string     `json:"status"`
	LastSyncAt  *time.Time `json:"last_sync_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *OAuthAuthService) ListAllAdminIdentities(ctx context.Context, page, pageSize int, provider, keyword string) (*ListResponse[*AdminOAuthIdentityListItem], error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}

	query := s.client.AdminThirdPartyUser.Query()
	if provider != "" {
		query = query.Where(adminthirdpartyuser.PlatformEQ(strings.ToLower(provider)))
	}
	if keyword != "" {
		query = query.Where(adminthirdpartyuser.Or(
			adminthirdpartyuser.OpenidContains(keyword),
			adminthirdpartyuser.NicknameContains(keyword),
			adminthirdpartyuser.HasAdminWith(admin.Or(
				admin.UIDContainsFold(keyword),
				admin.UsernameContainsFold(keyword),
				admin.EmailContainsFold(keyword),
			)),
		))
	}
	total, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}
	records, err := query.
		WithAdmin().
		Order(ent.Desc(adminthirdpartyuser.FieldCreatedAt)).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]*AdminOAuthIdentityListItem, 0, len(records))
	for _, record := range records {
		item := &AdminOAuthIdentityListItem{
			ID: record.ID, AdminID: record.AdminID, Provider: record.Platform,
			DisplayName: record.PlatformName, OpenID: record.Openid,
			Nickname: record.Nickname, AvatarURL: record.AvatarURL,
			Status: string(record.Status), LastSyncAt: record.LastSyncAt,
			CreatedAt: record.CreatedAt,
		}
		if record.Edges.Admin != nil {
			item.AdminUID = record.Edges.Admin.UID
			item.AdminEmail = record.Edges.Admin.Email
			item.AdminName = record.Edges.Admin.Username
		}
		items = append(items, item)
	}
	return &ListResponse[*AdminOAuthIdentityListItem]{Total: total, Page: page, Data: items}, nil
}

func (s *OAuthAuthService) DeleteAdminIdentity(ctx context.Context, id int) error {
	deleted, err := s.client.AdminThirdPartyUser.Delete().
		Where(adminthirdpartyuser.IDEQ(id)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrThirdPartyNotFound
	}
	return nil
}
