package plugin

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"go-admin/pluginapi"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

var extensionPermission = map[string]string{
	cmfplugin.ExtensionAdminRoutes:  "route:register",
	pluginapi.ExtensionPublicRoutes: "route:public:register",
	pluginapi.ExtensionUserRoutes:   "route:user:register",
}

func validateManifestCapabilities(manifest *cmfplugin.PluginManifest) error {
	permissions := make(map[string]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		if _, known := knownPluginPermissions[permission]; !known {
			return fmt.Errorf("unknown plugin permission %q", permission)
		}
		if _, duplicate := permissions[permission]; duplicate {
			return fmt.Errorf("duplicate plugin permission %q", permission)
		}
		permissions[permission] = struct{}{}
	}
	points := make(map[string]struct{}, len(manifest.ExtensionPoints))
	for _, point := range manifest.ExtensionPoints {
		required, known := extensionPermission[point]
		if !known {
			return fmt.Errorf("unknown plugin extension point %q", point)
		}
		if _, duplicate := points[point]; duplicate {
			return fmt.Errorf("duplicate plugin extension point %q", point)
		}
		if _, allowed := permissions[required]; !allowed {
			return fmt.Errorf("extension point %q requires permission %q", point, required)
		}
		points[point] = struct{}{}
	}
	return nil
}

var knownPluginPermissions = map[string]struct{}{
	"route:register":        {},
	"route:public:register": {},
	"route:user:register":   {},
}

func validateContributions(manifest *cmfplugin.PluginManifest, contributions *pluginapi.Contributions) error {
	if contributions == nil {
		return errors.New("plugin contributions are nil")
	}
	declared := make(map[string]struct{}, len(manifest.ExtensionPoints))
	for _, point := range manifest.ExtensionPoints {
		declared[point] = struct{}{}
	}
	for _, group := range []struct {
		point  string
		prefix string
		routes []pluginapi.AdminRouteContribution
	}{
		{cmfplugin.ExtensionAdminRoutes, "", contributions.AdminRoutes},
		{pluginapi.ExtensionPublicRoutes, "/public", contributions.PublicRoutes},
		{pluginapi.ExtensionUserRoutes, "/user/auth", contributions.UserRoutes},
	} {
		_, declaredPoint := declared[group.point]
		if declaredPoint && len(group.routes) == 0 {
			return fmt.Errorf("extension point %q has no contributions", group.point)
		}
		if !declaredPoint && len(group.routes) != 0 {
			return fmt.Errorf("contributions for undeclared extension point %q", group.point)
		}
		for _, route := range group.routes {
			if !validContributionMethod(route.Method, true) || !validContributionPath(route.Path, true) ||
				(group.prefix != "" && (strings.Contains(route.Path, "%") ||
					(route.Path != group.prefix && !strings.HasPrefix(route.Path, group.prefix+"/")))) {
				return fmt.Errorf("invalid %s route contribution %s %s", group.point, route.Method, route.Path)
			}
		}
	}
	return nil
}

func contributionPayload(contributions *pluginapi.Contributions, point string) (any, error) {
	switch point {
	case cmfplugin.ExtensionAdminRoutes:
		return append([]pluginapi.AdminRouteContribution(nil), contributions.AdminRoutes...), nil
	case pluginapi.ExtensionPublicRoutes:
		return append([]pluginapi.AdminRouteContribution(nil), contributions.PublicRoutes...), nil
	case pluginapi.ExtensionUserRoutes:
		return append([]pluginapi.AdminRouteContribution(nil), contributions.UserRoutes...), nil
	default:
		return nil, fmt.Errorf("unsupported extension point %q", point)
	}
}

func routeContributionAllows(routes []pluginapi.AdminRouteContribution, method, requestPath string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	requestPath = normalizeContributionPath(requestPath)
	if method == "" || requestPath == "" {
		return false
	}
	for _, route := range routes {
		if route.Method != "*" && strings.ToUpper(route.Method) != method {
			continue
		}
		if route.Path == "/*" || route.Path == requestPath {
			return true
		}
		if strings.HasSuffix(route.Path, "/*") {
			prefix := strings.TrimSuffix(route.Path, "/*")
			if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
				return true
			}
		}
	}
	return false
}

func validContributionMethod(method string, wildcard bool) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	if wildcard && method == "*" {
		return true
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

func validContributionPath(value string, wildcard bool) bool {
	if value == "" || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "?#\\") {
		return false
	}
	if wildcard && strings.HasSuffix(value, "/*") {
		value = strings.TrimSuffix(value, "/*")
		if value == "" {
			return true
		}
	}
	return normalizeContributionPath(value) == value
}

func normalizeContributionPath(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == "/." || cleaned != value {
		return ""
	}
	return cleaned
}
