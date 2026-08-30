package handler

import (
	"bytes"
	"errors"
	"net/url"
	"strings"

	"go-admin/internal/global"
	adminplugin "go-admin/internal/plugin"
	"go-admin/internal/service"
	"go-admin/internal/utils"
	"go-admin/pluginapi"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// ListPlugins 获取插件列表
// @Summary 获取插件列表
// @Description 获取所有已安装的插件
// @Tags 插件管理
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response{data=[]service.PluginResponse}
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/plugins [get]
func ListPlugins(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	pluginService := c.Locals("service.PluginService").(service.PluginServiceInterface)
	items, err := pluginService.List(c.Context())
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取插件列表失败").ToJSON(c)
	}
	return resp.Success(items, global.ResponseSuccessCode, "获取插件列表成功").ToJSON(c)
}

// GetPlugin 获取插件详情
// @Summary 获取插件详情
// @Description 根据插件ID获取插件详细信息
// @Tags 插件管理
// @Produce json
// @Security BearerAuth
// @Param plugin_id path string true "插件ID"
// @Success 200 {object} utils.Response{data=service.PluginResponse}
// @Failure 404 {object} utils.Response
// @Router /api/v1/admin/auth/plugins/{plugin_id} [get]
func GetPlugin(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	pluginService := c.Locals("service.PluginService").(service.PluginServiceInterface)
	item, err := pluginService.Get(c.Context(), c.Params("plugin_id"))
	if err != nil {
		return pluginErrorResponse(resp, c, err, "获取插件详情失败")
	}
	return resp.Success(item, global.ResponseSuccessCode, "获取插件详情成功").ToJSON(c)
}

// InstallPlugin 安装插件
// @Summary 安装插件
// @Description 通过本地插件包路径安装插件
// @Tags 插件管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.InstallPluginRequest true "插件安装请求"
// @Success 200 {object} utils.Response{data=service.PluginResponse}
// @Failure 400 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Router /api/v1/admin/auth/plugins/install [post]
func InstallPlugin(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	pluginService := c.Locals("service.PluginService").(service.PluginServiceInterface)
	req, err := validate.ParseAndValidate[service.InstallPluginRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}
	item, err := pluginService.Install(c.Context(), req)
	if err != nil {
		return pluginErrorResponse(resp, c, err, "安装插件失败")
	}
	return resp.Success(item, global.ResponseSuccessCode, "安装插件成功").ToJSON(c)
}

// EnablePlugin 启用插件
// @Summary 启用插件
// @Description 启用已安装的插件
// @Tags 插件管理
// @Produce json
// @Security BearerAuth
// @Param plugin_id path string true "插件ID"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/plugins/{plugin_id}/enable [post]
func EnablePlugin(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	pluginService := c.Locals("service.PluginService").(service.PluginServiceInterface)
	if err := pluginService.Enable(c.Context(), c.Params("plugin_id")); err != nil {
		return pluginErrorResponse(resp, c, err, "启用插件失败")
	}
	return resp.Success(nil, global.ResponseSuccessCode, "启用插件成功").ToJSON(c)
}

// DisablePlugin 停用插件
// @Summary 停用插件
// @Description 停用已启用的插件
// @Tags 插件管理
// @Produce json
// @Security BearerAuth
// @Param plugin_id path string true "插件ID"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/plugins/{plugin_id}/disable [post]
func DisablePlugin(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	pluginService := c.Locals("service.PluginService").(service.PluginServiceInterface)
	if err := pluginService.Disable(c.Context(), c.Params("plugin_id")); err != nil {
		return pluginErrorResponse(resp, c, err, "停用插件失败")
	}
	return resp.Success(nil, global.ResponseSuccessCode, "停用插件成功").ToJSON(c)
}

// UninstallPlugin 卸载插件
// @Summary 卸载插件
// @Description 卸载并删除插件
// @Tags 插件管理
// @Produce json
// @Security BearerAuth
// @Param plugin_id path string true "插件ID"
// @Success 200 {object} utils.Response
// @Router /api/v1/admin/auth/plugins/{plugin_id} [delete]
func UninstallPlugin(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	pluginService := c.Locals("service.PluginService").(service.PluginServiceInterface)
	if err := pluginService.Uninstall(c.Context(), c.Params("plugin_id")); err != nil {
		return pluginErrorResponse(resp, c, err, "卸载插件失败")
	}
	return resp.Success(nil, global.ResponseSuccessCode, "卸载插件成功").ToJSON(c)
}

// ProxyPlugin 插件接口代理
// @Summary 插件接口代理
// @Description 将请求转发给指定插件的 HTTP 接口，接受任意 HTTP 方法；/admin/auth 前缀形式会为插件路由补上 /admin/auth 前缀。实际可用接口由各插件自行定义。
// @Tags 插件管理
// @Produce json
// @Security BearerAuth
// @Param plugin_id path string true "插件ID"
// @Param path path string true "插件内路径"
// @Success 200 {object} utils.Response
// @Router /api/v1/plugin/{plugin_id}/{path} [get]
func ProxyPlugin(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	pluginService := c.Locals("service.PluginService").(service.PluginServiceInterface)
	if protectedProxyPath(c.Locals("plugin.proxy_path_prefix"), c.Params("*")) {
		return c.SendStatus(fiber.StatusForbidden)
	}
	proxyResp, err := pluginService.Proxy(c.Context(), &service.PluginProxyRequest{
		PluginID: c.Params("plugin_id"),
		Method:   c.Method(),
		Path:     proxyPath(c.Locals("plugin.proxy_path_prefix"), c.Params("*")),
		Query:    string(c.Request().URI().QueryString()),
		Header:   requestHeader(c),
		Body:     bytes.NewReader(c.Body()),
	})
	if err != nil {
		if errors.Is(err, adminplugin.ErrPermissionDenied) {
			return c.SendStatus(fiber.StatusForbidden)
		}
		return pluginErrorResponse(resp, c, err, "代理插件请求失败")
	}
	for key, values := range proxyResp.Header {
		if filteredPluginResponseHeader(key) {
			continue
		}
		for _, value := range values {
			if validHTTPHeaderValue(value) {
				c.Append(key, value)
			}
		}
	}
	return c.Status(proxyResp.StatusCode).Send(proxyResp.Body)
}

func protectedProxyPath(_ any, path string) bool {
	for range 3 {
		decoded, err := url.PathUnescape(path)
		if err != nil {
			return true
		}
		path = decoded
	}
	path = strings.Trim(path, "/")
	return path == "admin/auth" || strings.HasPrefix(path, "admin/auth/") ||
		path == "_go-admin" || strings.HasPrefix(path, "_go-admin/") || pluginapi.IsControlPath("/"+path)
}

func proxyPath(prefix any, path string) string {
	parts := make([]string, 0, 2)
	if value, ok := prefix.(string); ok && value != "" {
		parts = append(parts, strings.Trim(value, "/"))
	}
	path = strings.Trim(path, "/")
	if path != "" {
		parts = append(parts, path)
	}
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

func requestHeader(c fiber.Ctx) map[string][]string {
	out := make(map[string][]string)
	for key, values := range c.GetReqHeaders() {
		if filteredProxyHeader(key) {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}

func filteredProxyHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "cookie", "set-cookie", "host", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"x-go-admin-plugin-token", "x-go-admin-plugin-api", "x-go-admin-plugin-id", "x-go-admin-plugin-version":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(key), "x-forwarded-")
	}
}

func filteredPluginResponseHeader(key string) bool {
	if !validHTTPHeaderName(key) {
		return true
	}
	switch strings.ToLower(key) {
	case "set-cookie", "content-length", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade",
		"x-go-admin-plugin-token", "x-go-admin-plugin-api", "x-go-admin-plugin-id", "x-go-admin-plugin-version":
		return true
	default:
		return false
	}
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))) {
			return false
		}
	}
	return true
}

func validHTTPHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '\r' || character == '\n' || character == 0 || character == 0x7f || (character < 0x20 && character != '\t') {
			return false
		}
	}
	return true
}

func pluginErrorResponse(resp *utils.Response, c fiber.Ctx, err error, fallback string) error {
	switch {
	case errors.Is(err, adminplugin.ErrNotFound):
		return resp.Fail(global.ErrCodePluginNotFound, "插件不存在").ToJSON(c)
	case errors.Is(err, adminplugin.ErrAlreadyInstalled):
		return resp.Fail(global.ErrCodePluginAlreadyInstall, "插件已安装").ToJSON(c)
	case errors.Is(err, adminplugin.ErrInvalidPackage):
		return resp.Fail(global.ErrCodePluginPackageInvalid, "插件包无效").ToJSON(c)
	case errors.Is(err, adminplugin.ErrInvalidPath):
		return resp.Fail(global.ErrCodePluginPathInvalid, "插件路径非法").ToJSON(c)
	case errors.Is(err, adminplugin.ErrEnableFailed):
		return resp.Fail(global.ErrCodePluginEnableFailed, "插件启用失败").ToJSON(c)
	case errors.Is(err, adminplugin.ErrDisableFailed):
		return resp.Fail(global.ErrCodePluginDisableFailed, "插件停用失败").ToJSON(c)
	case errors.Is(err, adminplugin.ErrUninstallFailed):
		return resp.Fail(global.ErrCodePluginUninstallFail, "插件卸载失败").ToJSON(c)
	case errors.Is(err, adminplugin.ErrUpgradeFailed):
		return resp.Fail(global.ErrCodePluginUpgradeFailed, "插件升级失败，已恢复原版本").ToJSON(c)
	case errors.Is(err, adminplugin.ErrRollbackFailed):
		return resp.Fail(global.ErrCodePluginRollbackFailed, "插件升级与回滚均失败，请人工处理").ToJSON(c)
	case errors.Is(err, adminplugin.ErrNotEnabled):
		return resp.Fail(global.ResponseErrorCode, "插件未启用").ToJSON(c)
	case errors.Is(err, adminplugin.ErrProxyFailed):
		return resp.Fail(global.ResponseInternalServerError, "代理插件请求失败").ToJSON(c)
	case errors.Is(err, adminplugin.ErrUnsupportedTransport):
		return resp.Fail(global.ResponseErrorCode, "插件通信方式不支持").ToJSON(c)
	default:
		return resp.Fail(global.ResponseInternalServerError, fallback).ToJSON(c)
	}
}
