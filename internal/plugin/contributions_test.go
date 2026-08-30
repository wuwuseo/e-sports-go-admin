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
