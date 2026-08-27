package handler

import (
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go-admin/internal/global"
	appoauth "go-admin/internal/oauth"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
)

type OAuthExchangeRequest struct {
	Code string `json:"code"`
}

// ListOAuthProviders lists enabled external login providers.
// @Summary List enabled OAuth providers
// @Tags OAuth
// @Produce json
// @Param subject_type query string false "user or admin" default(user)
// @Success 200 {object} utils.Response{data=[]service.PublicOAuthProvider}
// @Router /api/v1/public/oauth/providers [get]
func ListOAuthProviders(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	subjectType := strings.ToLower(c.Query("subject_type", "user"))
	if subjectType != "user" && subjectType != "admin" {
		return response.Fail(global.ResponseErrorCode, "Invalid OAuth subject type").ToJSON(c)
	}
	configService := c.Locals("service.OAuthProviderConfigService").(*service.OAuthProviderConfigService)
	providers, err := configService.ListEnabled(c.Context(), subjectType)
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(providers).ToJSON(c)
}

// BeginOAuthLogin redirects to the provider authorization page.
// @Summary Begin OAuth login
// @Tags OAuth
// @Param provider path string true "provider name"
// @Success 302
// @Router /api/v1/public/oauth/user/{provider}/authorize [get]
// @Router /api/v1/public/oauth/admin/{provider}/authorize [get]
func BeginOAuthLogin(c fiber.Ctx) error {
	subjectType := strings.ToLower(c.Params("subject_type"))
	if configured, ok := c.Locals("oauth.subject_type").(string); ok {
		subjectType = configured
	}
	if subjectType != "user" && subjectType != "admin" {
		return oauthFailure(c.Locals("utils.Response").(*utils.Response), c, service.ErrOAuthSessionInvalid)
	}
	authService := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService)
	result, err := authService.Begin(c.Context(), c.Params("provider"), subjectType, "login", "")
	if err != nil {
		return oauthFailure(c.Locals("utils.Response").(*utils.Response), c, err)
	}
	return c.Redirect().Status(fiber.StatusFound).To(result.AuthorizationURL)
}

// OAuthCallback consumes provider state and redirects with a one-time login code.
// @Summary OAuth provider callback
// @Tags OAuth
// @Param provider path string true "provider name"
// @Param state query string true "OAuth state"
// @Param code query string false "provider authorization code"
// @Success 302
// @Router /api/v1/public/oauth/{provider}/callback [get]
func OAuthCallback(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	state := c.Query("state")
	if state == "" {
		return oauthFailure(response, c, service.ErrOAuthSessionInvalid)
	}

	authService := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService)
	var result *service.OAuthCallbackResult
	var err error
	if providerError := c.Query("error"); providerError != "" {
		result, err = authService.HandleCallbackError(c.Context(), c.Params("provider"), state, providerError)
	} else {
		code := c.Query("code")
		if code == "" {
			return oauthFailure(response, c, service.ErrOAuthSessionInvalid)
		}
		result, err = authService.HandleCallback(c.Context(), c.Params("provider"), state, code)
	}
	if result != nil && result.RedirectURL != "" {
		return redirectOAuthCallback(c, result, err)
	}
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return oauthFailure(response, c, service.ErrOAuthSessionInvalid)
}

// ExchangeOAuthLoginCode exchanges a 60-second one-time code for site JWTs.
// @Summary Exchange OAuth login code
// @Tags OAuth
// @Accept json
// @Produce json
// @Param request body OAuthExchangeRequest true "one-time code"
// @Success 200 {object} utils.Response{data=service.TokenResponse}
// @Router /api/v1/public/oauth/exchange [post]
func ExchangeOAuthLoginCode(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	var request OAuthExchangeRequest
	if err := c.Bind().Body(&request); err != nil || strings.TrimSpace(request.Code) == "" {
		return response.Fail(global.ResponseErrorCode, "Invalid OAuth login code").ToJSON(c)
	}
	authService := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService)
	token, err := authService.ExchangeLoginCode(c.Context(), request.Code)
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(token).ToJSON(c)
}

// BeginOAuthBind starts a provider bind flow for the current account.
// @Summary Begin OAuth identity binding
// @Tags OAuth
// @Security BearerAuth
// @Param provider path string true "provider name"
// @Success 302
// @Router /api/v1/user/auth/oauth/{provider}/authorize [get]
// @Router /api/v1/admin/auth/oauth/{provider}/authorize [get]
func BeginOAuthBind(subjectType string) fiber.Handler {
	return func(c fiber.Ctx) error {
		response := c.Locals("utils.Response").(*utils.Response)
		uid, ok := oauthSubjectUID(c)
		if !ok {
			return response.Fail(global.ResponseUnauthorized, "Invalid login identity").ToJSON(c)
		}
		authService := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService)
		result, err := authService.Begin(c.Context(), c.Params("provider"), subjectType, "bind", uid)
		if err != nil {
			return oauthFailure(response, c, err)
		}
		return c.Redirect().Status(fiber.StatusFound).To(result.AuthorizationURL)
	}
}

// ListUserOAuthIdentities lists the current user's bound providers.
// @Summary List current user OAuth identities
// @Tags OAuth
// @Security BearerAuth
// @Produce json
// @Success 200 {object} utils.Response{data=[]service.OAuthIdentityResponse}
// @Router /api/v1/user/auth/oauth/identities [get]
func ListUserOAuthIdentities(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid, ok := oauthSubjectUID(c)
	if !ok {
		return response.Fail(global.ResponseUnauthorized, "Invalid login identity").ToJSON(c)
	}
	result, err := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService).
		ListUserIdentities(c.Context(), uid)
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(result).ToJSON(c)
}

// UnbindUserOAuthIdentity removes a provider from the current user.
// @Summary Unbind current user OAuth identity
// @Tags OAuth
// @Security BearerAuth
// @Param provider path string true "provider name"
// @Success 200 {object} utils.Response
// @Router /api/v1/user/auth/oauth/{provider} [delete]
func UnbindUserOAuthIdentity(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid, ok := oauthSubjectUID(c)
	if !ok {
		return response.Fail(global.ResponseUnauthorized, "Invalid login identity").ToJSON(c)
	}
	err := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService).
		UnbindUser(c.Context(), uid, c.Params("provider"))
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

// ListAdminOAuthIdentities lists the current administrator's bound providers.
// @Summary List current administrator OAuth identities
// @Tags OAuth
// @Security BearerAuth
// @Produce json
// @Success 200 {object} utils.Response{data=[]service.OAuthIdentityResponse}
// @Router /api/v1/admin/auth/oauth/identities [get]
func ListAdminOAuthIdentities(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid, ok := oauthSubjectUID(c)
	if !ok {
		return response.Fail(global.ResponseUnauthorized, "Invalid login identity").ToJSON(c)
	}
	result, err := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService).
		ListAdminIdentities(c.Context(), uid)
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(result).ToJSON(c)
}

// UnbindAdminOAuthIdentity removes a provider from the current administrator.
// @Summary Unbind current administrator OAuth identity
// @Tags OAuth
// @Security BearerAuth
// @Param provider path string true "provider name"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/oauth/{provider} [delete]
func UnbindAdminOAuthIdentity(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	uid, ok := oauthSubjectUID(c)
	if !ok {
		return response.Fail(global.ResponseUnauthorized, "Invalid login identity").ToJSON(c)
	}
	err := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService).
		UnbindAdmin(c.Context(), uid, c.Params("provider"))
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

// ListOAuthProviderConfigs lists masked provider configurations.
// @Summary List OAuth provider configurations
// @Tags OAuth configuration
// @Security BearerAuth
// @Produce json
// @Success 200 {object} utils.Response{data=[]service.OAuthProviderConfigResponse}
// @Router /api/v1/admin/auth/oauth/providers [get]
func ListOAuthProviderConfigs(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	if err := requireSuperAdmin(c); err != nil {
		return oauthFailure(response, c, err)
	}
	result, err := c.Locals("service.OAuthProviderConfigService").(*service.OAuthProviderConfigService).
		List(c.Context())
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(result).ToJSON(c)
}

// GetOAuthProviderConfig returns a masked provider configuration.
// @Summary Get OAuth provider configuration
// @Tags OAuth configuration
// @Security BearerAuth
// @Param provider path string true "provider name"
// @Success 200 {object} utils.Response{data=service.OAuthProviderConfigResponse}
// @Router /api/v1/admin/auth/oauth/providers/{provider} [get]
func GetOAuthProviderConfig(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	if err := requireSuperAdmin(c); err != nil {
		return oauthFailure(response, c, err)
	}
	result, err := c.Locals("service.OAuthProviderConfigService").(*service.OAuthProviderConfigService).
		Get(c.Context(), c.Params("provider"))
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(result).ToJSON(c)
}

// UpsertOAuthProviderConfig creates or updates provider configuration.
// @Summary Create or update OAuth provider configuration
// @Tags OAuth configuration
// @Security BearerAuth
// @Accept json
// @Param provider path string true "provider name"
// @Param request body service.OAuthProviderConfigInput true "provider configuration"
// @Success 200 {object} utils.Response{data=service.OAuthProviderConfigResponse}
// @Router /api/v1/admin/auth/oauth/providers/{provider} [put]
func UpsertOAuthProviderConfig(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	if err := requireSuperAdmin(c); err != nil {
		return oauthFailure(response, c, err)
	}
	var input service.OAuthProviderConfigInput
	if err := c.Bind().Body(&input); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid OAuth provider configuration").ToJSON(c)
	}
	input.Provider = c.Params("provider")
	result, err := c.Locals("service.OAuthProviderConfigService").(*service.OAuthProviderConfigService).
		Upsert(c.Context(), input)
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(result, global.ResponseSuccessActionCode).ToJSON(c)
}

// DeleteOAuthProviderConfig deletes provider configuration.
// @Summary Delete OAuth provider configuration
// @Tags OAuth configuration
// @Security BearerAuth
// @Param provider path string true "provider name"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/oauth/providers/{provider} [delete]
func DeleteOAuthProviderConfig(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	if err := requireSuperAdmin(c); err != nil {
		return oauthFailure(response, c, err)
	}
	err := c.Locals("service.OAuthProviderConfigService").(*service.OAuthProviderConfigService).
		Delete(c.Context(), c.Params("provider"))
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

// ListAdminThirdPartyIdentities lists administrator OAuth bindings.
// @Summary List administrator OAuth identities
// @Tags OAuth administration
// @Security BearerAuth
// @Param page query int false "page"
// @Param page_size query int false "page size"
// @Param provider query string false "provider"
// @Param keyword query string false "administrator or identity keyword"
// @Success 200 {object} utils.Response{data=service.ListResponse[service.AdminOAuthIdentityListItem]}
// @Router /api/v1/admin/auth/third-party-admins [get]
func ListAdminThirdPartyIdentities(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	op, err := oauthAdminOperatorContext(c)
	if err != nil {
		return oauthFailure(response, c, err)
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "10"))
	result, err := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService).
		ListAllAdminIdentities(c.Context(), op, page, pageSize, c.Query("provider"), c.Query("keyword"))
	if err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(result).ToJSON(c)
}

// DeleteAdminThirdPartyIdentity removes an administrator OAuth binding.
// @Summary Delete administrator OAuth identity
// @Tags OAuth administration
// @Security BearerAuth
// @Param id path int true "identity ID"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/third-party-admins/{id} [delete]
func DeleteAdminThirdPartyIdentity(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	op, err := oauthAdminOperatorContext(c)
	if err != nil {
		return oauthFailure(response, c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid identity ID").ToJSON(c)
	}
	if err := c.Locals("service.OAuthAuthService").(*service.OAuthAuthService).
		DeleteAdminIdentity(c.Context(), op, id); err != nil {
		return oauthFailure(response, c, err)
	}
	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

func oauthAdminOperatorContext(c fiber.Ctx) (*service.EffectiveDataScope, error) {
	uid, ok := oauthSubjectUID(c)
	if !ok {
		return nil, service.ErrPermissionOperatorRequired
	}
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
	return dataPermSvc.NewOperatorContext(c.Context(), uid, getOperatorDomain(c))
}

func oauthSubjectUID(c fiber.Ctx) (string, bool) {
	claims := jwt.GetJWTUserData(c)
	if claims == nil {
		return "", false
	}
	uid, ok := claims["id"].(string)
	return uid, ok && uid != ""
}

func requireSuperAdmin(c fiber.Ctx) error {
	uid, ok := oauthSubjectUID(c)
	if !ok {
		return errors.New("administrator identity is missing")
	}
	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)
	admin, err := adminService.GetAdminByUID(c.Context(), uid)
	if err != nil {
		return err
	}
	if !isOAuthSuperAdmin(admin) {
		return errOAuthSuperAdminRequired
	}
	return nil
}

func isOAuthSuperAdmin(admin *service.AdminResponse) bool {
	return admin.IsSuperAdmin || slices.Contains(admin.Roles, service.SuperAdminRoleName)
}

var errOAuthSuperAdminRequired = errors.New("super administrator is required")

func redirectOAuthCallback(c fiber.Ctx, result *service.OAuthCallbackResult, callbackErr error) error {
	redirectURL, err := url.Parse(result.RedirectURL)
	if err != nil {
		return oauthFailure(c.Locals("utils.Response").(*utils.Response), c, service.ErrOAuthInvalidURL)
	}
	query := redirectURL.Query()
	if callbackErr != nil {
		query.Set("oauth_status", "error")
		query.Set("error", oauthErrorCode(callbackErr))
	} else {
		query.Set("oauth_status", "success")
		if result.LoginCode != "" {
			query.Set("code", result.LoginCode)
		}
		if result.FlowType == "bind" {
			query.Set("bound", "true")
		}
	}
	redirectURL.RawQuery = query.Encode()
	return c.Redirect().Status(fiber.StatusFound).To(redirectURL.String())
}

func oauthFailure(response *utils.Response, c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, service.ErrPermissionOperatorRequired), errors.Is(err, service.ErrDataPermissionAdminNotFound):
		return response.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	case errors.Is(err, errOAuthSuperAdminRequired):
		return response.Fail(global.ResponseForbidden, "Super administrator is required").ToJSON(c)
	case errors.Is(err, service.ErrOAuthProviderNotFound), errors.Is(err, service.ErrThirdPartyNotFound):
		return response.Fail(global.ResponseNotFound, oauthErrorCode(err)).ToJSON(c)
	case errors.Is(err, service.ErrOAuthIdentityBound), errors.Is(err, service.ErrOAuthLastLoginMethod):
		return response.Fail(global.ResponseConflict, oauthErrorCode(err)).ToJSON(c)
	case errors.Is(err, service.ErrInvalidUserStatus), errors.Is(err, service.ErrInvalidAdminStatus),
		errors.Is(err, service.ErrOAuthProviderDisabled), errors.Is(err, service.ErrOAuthAdminNotBound):
		return response.Fail(global.ResponseForbidden, oauthErrorCode(err)).ToJSON(c)
	case errors.Is(err, service.ErrOAuthSessionInvalid), errors.Is(err, service.ErrOAuthLoginCodeInvalid),
		errors.Is(err, service.ErrOAuthProviderMismatch), errors.Is(err, service.ErrOAuthConfigIncomplete),
		errors.Is(err, service.ErrOAuthInvalidURL), errors.Is(err, appoauth.ErrVerifiedEmailRequired):
		return response.Fail(global.ResponseErrorCode, oauthErrorCode(err)).ToJSON(c)
	default:
		return response.Fail(global.ResponseInternalServerError, "oauth_internal_error").ToJSON(c)
	}
}

func oauthErrorCode(err error) string {
	switch {
	case errors.Is(err, service.ErrOAuthProviderNotFound):
		return "oauth_provider_not_found"
	case errors.Is(err, service.ErrOAuthProviderDisabled):
		return "oauth_provider_disabled"
	case errors.Is(err, service.ErrOAuthConfigIncomplete):
		return "oauth_config_incomplete"
	case errors.Is(err, service.ErrOAuthInvalidURL):
		return "oauth_invalid_url"
	case errors.Is(err, service.ErrOAuthSessionInvalid):
		return "oauth_session_invalid"
	case errors.Is(err, service.ErrOAuthLoginCodeInvalid):
		return "oauth_login_code_invalid"
	case errors.Is(err, service.ErrOAuthProviderMismatch):
		return "oauth_provider_mismatch"
	case errors.Is(err, service.ErrOAuthIdentityBound):
		return "oauth_identity_already_bound"
	case errors.Is(err, service.ErrOAuthAdminNotBound):
		return "oauth_admin_not_bound"
	case errors.Is(err, service.ErrOAuthLastLoginMethod):
		return "oauth_last_login_method"
	case errors.Is(err, service.ErrThirdPartyNotFound):
		return "oauth_identity_not_found"
	case errors.Is(err, service.ErrInvalidUserStatus), errors.Is(err, service.ErrInvalidAdminStatus):
		return "oauth_account_inactive"
	case errors.Is(err, appoauth.ErrVerifiedEmailRequired):
		return "oauth_verified_email_required"
	default:
		return "oauth_authorization_failed"
	}
}
