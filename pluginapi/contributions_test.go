package pluginapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadContributionsUsesStrictTypedSchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ContributionsFileName)
	if err := os.WriteFile(path, []byte(`{"admin.routes":[{"method":"GET","path":"/manifest"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	contributions, err := LoadContributions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(contributions.AdminRoutes) != 1 || contributions.AdminRoutes[0].Path != "/manifest" {
		t.Fatalf("contributions = %+v", contributions)
	}
	if err := os.WriteFile(path, []byte(`{"public.routes":[{"method":"GET","path":"/public/feed"}],"user.routes":[{"method":"GET","path":"/user/auth/profile"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	contributions, err = LoadContributions(root)
	if err != nil || len(contributions.PublicRoutes) != 1 || len(contributions.UserRoutes) != 1 {
		t.Fatalf("audience contributions = %+v, error = %v", contributions, err)
	}
	if err := os.WriteFile(path, []byte(`{"unknown":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContributions(root); err == nil {
		t.Fatal("unknown contribution field was accepted")
	}
}
