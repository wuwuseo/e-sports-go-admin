package plugin

import (
	"testing"

	"go-admin/pluginapi"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

func TestValidateManifestCapabilitiesRequiresMatchingPermission(t *testing.T) {
	manifest := &cmfplugin.PluginManifest{
		Permissions:     []string{"route:register"},
		ExtensionPoints: []string{cmfplugin.ExtensionAdminRoutes},
	}
	if err := validateManifestCapabilities(manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Permissions = nil
	if err := validateManifestCapabilities(manifest); err == nil {
		t.Fatal("extension point without its permission was accepted")
	}
	manifest.Permissions = []string{"filesystem:all"}
	if err := validateManifestCapabilities(manifest); err == nil {
		t.Fatal("unknown permission was accepted")
	}
}

func TestRouteContributionAllowsOnlyDeclaredMethodAndPath(t *testing.T) {
	routes := []pluginapi.AdminRouteContribution{
		{Method: "GET", Path: "/manifest"},
		{Method: "POST", Path: "/admin/auth/items/*"},
	}
	for _, test := range []struct {
		method string
		path   string
		want   bool
	}{
		{"GET", "/manifest", true},
		{"POST", "/manifest", false},
		{"POST", "/admin/auth/items", true},
		{"POST", "/admin/auth/items/42", true},
		{"POST", "/admin/auth/other", false},
		{"GET", "/allowed/../manifest", false},
	} {
		if got := routeContributionAllows(routes, test.method, test.path); got != test.want {
			t.Fatalf("routeContributionAllows(%q, %q) = %v, want %v", test.method, test.path, got, test.want)
		}
	}
}

func TestValidateContributionsRejectsUndeclaredPayload(t *testing.T) {
	manifest := &cmfplugin.PluginManifest{
		Permissions:     nil,
		ExtensionPoints: nil,
	}
	contributions := &pluginapi.Contributions{
		AdminRoutes: []pluginapi.AdminRouteContribution{{Method: "GET", Path: "/manifest"}},
	}
	if err := validateContributions(manifest, contributions); err == nil {
		t.Fatal("contribution for an undeclared extension point was accepted")
	}
}

func TestValidateManifestCapabilitiesRejectsUnsupportedV1Extension(t *testing.T) {
	manifest := &cmfplugin.PluginManifest{
		Permissions:     []string{"menu:register"},
		ExtensionPoints: []string{cmfplugin.ExtensionAdminMenus},
	}
	if err := validateManifestCapabilities(manifest); err == nil {
		t.Fatal("unsupported v1 extension point was accepted")
	}
}

func TestValidateAudienceRouteContributions(t *testing.T) {
	manifest := &cmfplugin.PluginManifest{
		Permissions:     []string{"route:public:register", "route:user:register"},
		ExtensionPoints: []string{pluginapi.ExtensionPublicRoutes, pluginapi.ExtensionUserRoutes},
	}
	contributions := &pluginapi.Contributions{
		PublicRoutes: []pluginapi.AdminRouteContribution{{Method: "GET", Path: "/public/feed"}},
		UserRoutes:   []pluginapi.AdminRouteContribution{{Method: "POST", Path: "/user/auth/orders/*"}},
	}
	if err := validateManifestCapabilities(manifest); err != nil {
		t.Fatal(err)
	}
	if err := validateContributions(manifest, contributions); err != nil {
		t.Fatal(err)
	}
	manifest.Permissions = []string{"route:public:register"}
	if err := validateManifestCapabilities(manifest); err == nil {
		t.Fatal("user routes without matching permission were accepted")
	}
	manifest.Permissions = []string{"route:public:register", "route:user:register"}
	for _, invalidPath := range []string{"/admin/auth/secret", "/publicity/feed", "/*", "/public/../admin/auth/secret", "/public/%2e%2e/admin/auth/secret"} {
		contributions.PublicRoutes[0].Path = invalidPath
		if err := validateContributions(manifest, contributions); err == nil {
			t.Fatalf("public route %q was accepted", invalidPath)
		}
	}
	contributions.PublicRoutes[0].Path = "/public/feed"
	contributions.UserRoutes[0].Path = "/public/feed"
	if err := validateContributions(manifest, contributions); err == nil {
		t.Fatal("user route outside /user/auth was accepted")
	}
}
