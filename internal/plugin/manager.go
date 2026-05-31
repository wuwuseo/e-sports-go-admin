package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go-admin/internal/ent"
	pluginent "go-admin/internal/ent/plugin"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

type Manager interface {
	Install(ctx context.Context, packagePath string) (*cmfplugin.PluginInfo, error)
	Enable(ctx context.Context, pluginID string) error
	Disable(ctx context.Context, pluginID string) error
	Uninstall(ctx context.Context, pluginID string) error
	List(ctx context.Context) ([]cmfplugin.PluginInfo, error)
	Get(ctx context.Context, pluginID string) (*cmfplugin.PluginInfo, error)
}

type Options struct {
	PluginRoot string
	TempRoot   string
	Registry   cmfplugin.ExtensionRegistry
	Runtime    cmfplugin.PluginRuntime
}

type DefaultManager struct {
	client *ent.Client
	opts   Options
}

func NewManager(client *ent.Client, opts Options) *DefaultManager {
	if opts.PluginRoot == "" {
		opts.PluginRoot = filepath.Join(".", "data", "plugins")
	}
	if opts.TempRoot == "" {
		opts.TempRoot = filepath.Join(os.TempDir(), "go-admin-plugins")
	}
	if opts.Registry == nil {
		opts.Registry = cmfplugin.NewMemoryExtensionRegistry()
	}
	if opts.Runtime == nil {
		opts.Runtime = cmfplugin.NewProcessRuntime()
	}
	return &DefaultManager{client: client, opts: opts}
}

func (m *DefaultManager) Install(ctx context.Context, packagePath string) (*cmfplugin.PluginInfo, error) {
	if packagePath == "" {
		return nil, ErrInvalidPackage
	}
	if _, err := os.Stat(packagePath); err != nil {
		return nil, ErrInvalidPackage
	}

	tmpDir := filepath.Join(m.opts.TempRoot, fmt.Sprintf("%d", time.Now().UnixNano()))
	defer os.RemoveAll(tmpDir)
	if err := cmfplugin.SafeUnzip(packagePath, tmpDir); err != nil {
		return nil, ErrInvalidPackage
	}
	manifest, err := cmfplugin.ReadManifestFromDir(tmpDir)
	if err != nil {
		return nil, ErrInvalidPackage
	}

	exists, err := m.client.Plugin.Query().
		Where(pluginent.PluginIDEQ(manifest.ID), pluginent.VersionEQ(manifest.Version)).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrAlreadyInstalled
	}

	targetDir := filepath.Join(m.opts.PluginRoot, manifest.ID, manifest.Version)
	if !cmfplugin.IsWithinRoot(m.opts.PluginRoot, targetDir) {
		return nil, ErrInvalidPath
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, fmt.Errorf("%w: clean target: %v", ErrInvalidPath, err)
	}
	if err := cmfplugin.SafeUnzip(packagePath, targetDir); err != nil {
		return nil, ErrInvalidPackage
	}

	manifestJSON, err := manifestToMap(manifest)
	if err != nil {
		return nil, err
	}
	record, err := m.client.Plugin.Create().
		SetPluginID(manifest.ID).
		SetName(manifest.Name).
		SetVersion(manifest.Version).
		SetAuthor(manifest.Author).
		SetDescription(manifest.Description).
		SetStatus(pluginent.StatusInstalled).
		SetInstallPath(targetDir).
		SetManifestJSON(manifestJSON).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	return recordToInfo(record)
}

func (m *DefaultManager) Enable(ctx context.Context, pluginID string) error {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return err
	}
	if record.Status == pluginent.StatusEnabled {
		return nil
	}
	info, err := recordToInfo(record)
	if err != nil {
		return err
	}
	for _, point := range info.Manifest.ExtensionPoints {
		if err := m.opts.Registry.Register(info.ID, point, map[string]any{"plugin_id": info.ID}); err != nil {
			return err
		}
	}
	if info.Manifest.Engine.Type == cmfplugin.EngineTypeProcess {
		if err := m.opts.Runtime.Start(ctx, *info); err != nil {
			_ = m.markError(ctx, record, err)
			return ErrEnableFailed
		}
	}
	if err := m.opts.Runtime.Health(ctx, pluginID); err != nil {
		_ = m.markError(ctx, record, err)
		return ErrEnableFailed
	}
	_, err = m.client.Plugin.UpdateOne(record).
		SetStatus(pluginent.StatusEnabled).
		SetEnabledAt(time.Now()).
		ClearErrorMessage().
		Save(ctx)
	return err
}

func (m *DefaultManager) Disable(ctx context.Context, pluginID string) error {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return err
	}
	if record.Status != pluginent.StatusEnabled {
		return nil
	}
	if err := m.opts.Runtime.Stop(ctx, pluginID); err != nil {
		return ErrDisableFailed
	}
	if err := m.opts.Registry.UnregisterPlugin(pluginID); err != nil {
		return err
	}
	_, err = m.client.Plugin.UpdateOne(record).
		SetStatus(pluginent.StatusDisabled).
		ClearEnabledAt().
		Save(ctx)
	return err
}

func (m *DefaultManager) Uninstall(ctx context.Context, pluginID string) error {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return err
	}
	if record.Status == pluginent.StatusEnabled {
		if err := m.Disable(ctx, pluginID); err != nil {
			return err
		}
	}
	if !cmfplugin.IsWithinRoot(m.opts.PluginRoot, record.InstallPath) {
		return ErrInvalidPath
	}
	if err := os.RemoveAll(record.InstallPath); err != nil {
		return ErrUninstallFailed
	}
	return m.client.Plugin.DeleteOne(record).Exec(ctx)
}

func (m *DefaultManager) List(ctx context.Context) ([]cmfplugin.PluginInfo, error) {
	records, err := m.client.Plugin.Query().Order(ent.Desc(pluginent.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]cmfplugin.PluginInfo, 0, len(records))
	for _, record := range records {
		info, err := recordToInfo(record)
		if err != nil {
			return nil, err
		}
		out = append(out, *info)
	}
	return out, nil
}

func (m *DefaultManager) Get(ctx context.Context, pluginID string) (*cmfplugin.PluginInfo, error) {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	return recordToInfo(record)
}

func (m *DefaultManager) findRecord(ctx context.Context, pluginID string) (*ent.Plugin, error) {
	record, err := m.client.Plugin.Query().Where(pluginent.PluginIDEQ(pluginID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return record, nil
}

func (m *DefaultManager) markError(ctx context.Context, record *ent.Plugin, cause error) error {
	_, err := m.client.Plugin.UpdateOne(record).
		SetStatus(pluginent.StatusError).
		SetErrorMessage(cause.Error()).
		Save(ctx)
	return err
}

func manifestToMap(manifest *cmfplugin.PluginManifest) (map[string]any, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func mapToManifest(value map[string]any) (*cmfplugin.PluginManifest, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var manifest cmfplugin.PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func recordToInfo(record *ent.Plugin) (*cmfplugin.PluginInfo, error) {
	manifest, err := mapToManifest(record.ManifestJSON)
	if err != nil {
		return nil, err
	}
	return &cmfplugin.PluginInfo{
		ID:          record.PluginID,
		Name:        record.Name,
		Version:     record.Version,
		Author:      record.Author,
		Description: record.Description,
		Status:      cmfplugin.PluginStatus(record.Status),
		InstallPath: record.InstallPath,
		Manifest:    manifest,
	}, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
