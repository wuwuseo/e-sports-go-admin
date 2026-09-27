package pluginapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	ContributionsFileName = "contributions.json"
	maxContributionsSize  = 1 << 20
	ExtensionPublicRoutes = "public.routes"
	ExtensionUserRoutes   = "user.routes"
)

type Contributions struct {
	AdminRoutes  []AdminRouteContribution `json:"admin.routes,omitempty"`
	PublicRoutes []AdminRouteContribution `json:"public.routes,omitempty"`
	UserRoutes   []AdminRouteContribution `json:"user.routes,omitempty"`
}

type AdminRouteContribution struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

func LoadContributions(pluginRoot string) (*Contributions, error) {
	path := filepath.Join(pluginRoot, ContributionsFileName)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxContributionsSize {
		return nil, errors.New("invalid plugin contributions file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var contributions Contributions
	if err := decoder.Decode(&contributions); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid trailing plugin contributions data")
	}
	return &contributions, nil
}
