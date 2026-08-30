package plugin

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"go-admin/internal/ent"
	pluginent "go-admin/internal/ent/plugin"
	"go-admin/pluginapi"

	version "github.com/hashicorp/go-version"
	cmflog "github.com/wuwuseo/cmf/log"
	cmfplugin "github.com/wuwuseo/cmf/plugin"
	"go.uber.org/zap"
)

type Manager interface {
	Close(ctx context.Context) error
	Install(ctx context.Context, packagePath string) (*cmfplugin.PluginInfo, error)
	Enable(ctx context.Context, pluginID string) error
	Disable(ctx context.Context, pluginID string) error
	Uninstall(ctx context.Context, pluginID string) error
	List(ctx context.Context) ([]cmfplugin.PluginInfo, error)
	Get(ctx context.Context, pluginID string) (*cmfplugin.PluginInfo, error)
	Authorize(ctx context.Context, pluginID string, method string, path string) error
	ResolveEndpoint(ctx context.Context, pluginID string) (*Endpoint, error)
	Invoke(ctx context.Context, pluginID string, request *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error)
}

type Endpoint struct {
	PluginID  string
	Protocol  string
	Transport string
	Address   string
	Socket    string
	Token     string
}

type Diagnostics struct {
	ErrorMessage string
	InstalledAt  time.Time
	EnabledAt    *time.Time
	UpdatedAt    time.Time
	Restarting   bool
}

type runtimeInvoker interface {
	Invoke(context.Context, string, *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error)
}

type runtimeEndpointResolver interface {
	Endpoint(string) (RuntimeEndpoint, bool)
}

type runtimeExitObserver interface {
	SetExitHandler(func(string, error))
}

type runtimeStopper interface {
	StopAll(context.Context) error
}

type Options struct {
	PluginRoot        string
	PackageRoot       string
	TempRoot          string
	RequireSignature  bool
	TrustedPublicKeys []ed25519.PublicKey
	Registry          cmfplugin.ExtensionRegistry
	Runtime           cmfplugin.PluginRuntime
}

type DefaultManager struct {
	client      *ent.Client
	opts        Options
	opsMu       sync.Mutex
	recoveryMu  sync.Mutex
	recovering  map[string]struct{}
	recoveryCtx context.Context
	cancel      context.CancelFunc
	recoveryWG  sync.WaitGroup
	closed      bool
}

const (
	maxPluginPackageSize    = int64(128 << 20)
	maxPluginFileSize       = uint64(128 << 20)
	maxPluginUnpackedSize   = uint64(512 << 20)
	maxPluginFiles          = 2048
	runtimeRestartAttempts  = 3
	runtimeRestartBaseDelay = 250 * time.Millisecond
	runtimeRecoveryTimeout  = 15 * time.Second
)

var (
	safePluginVersionPattern      = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	appConstraintSeparatorPattern = regexp.MustCompile(`\s+(<=|>=|!=|~>|<|>|=)\s*`)
)

// HostVersion can be overridden at build time with -ldflags for release builds.
var HostVersion = "1.0.0"

func NewManager(client *ent.Client, opts Options) *DefaultManager {
	if opts.PluginRoot == "" {
		opts.PluginRoot = filepath.Join(".", "data", "plugins")
	}
	if absRoot, err := filepath.Abs(opts.PluginRoot); err == nil {
		opts.PluginRoot = absRoot
	}
	// 安全兜底：未配置 PackageRoot 时禁止任意路径安装，
	// 默认收敛到 ./data/plugin-packages 目录；
	// 避免 IsWithinRoot("", absPath) 因 filepath.Abs("") 退化为 CWD，
	// 导致整个项目目录都被视为合法插件包来源。
	if opts.PackageRoot == "" {
		opts.PackageRoot = filepath.Join(".", "data", "plugin-packages")
	}
	if absPkg, err := filepath.Abs(opts.PackageRoot); err == nil {
		opts.PackageRoot = absPkg
	}
	if opts.TempRoot == "" {
		opts.TempRoot = filepath.Join(os.TempDir(), "go-admin-plugins")
	}
	if absTemp, err := filepath.Abs(opts.TempRoot); err == nil {
		opts.TempRoot = absTemp
	}
	if opts.Registry == nil {
		opts.Registry = cmfplugin.NewMemoryExtensionRegistry()
	}
	if opts.Runtime == nil {
		opts.Runtime = newProcessRuntime(opts.TempRoot)
	}
	trustedKeys := make([]ed25519.PublicKey, len(opts.TrustedPublicKeys))
	for index, key := range opts.TrustedPublicKeys {
		trustedKeys[index] = append(ed25519.PublicKey(nil), key...)
	}
	opts.TrustedPublicKeys = trustedKeys
	recoveryCtx, cancel := context.WithCancel(context.Background())
	manager := &DefaultManager{
		client: client, opts: opts, recovering: make(map[string]struct{}),
		recoveryCtx: recoveryCtx, cancel: cancel,
	}
	if observer, ok := opts.Runtime.(runtimeExitObserver); ok {
		observer.SetExitHandler(manager.markRuntimeExited)
	}
	return manager
}

func (m *DefaultManager) Close(ctx context.Context) error {
	m.recoveryMu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
	}
	m.recoveryMu.Unlock()

	done := make(chan struct{})
	go func() {
		m.recoveryWG.Wait()
		close(done)
	}()
	var waitErr error
	select {
	case <-done:
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	m.opsMu.Lock()
	defer m.opsMu.Unlock()
	var stopErr error
	if runtimeManager, ok := m.opts.Runtime.(runtimeStopper); ok {
		stopErr = runtimeManager.StopAll(ctx)
	}
	return errors.Join(waitErr, stopErr)
}

func (m *DefaultManager) Install(ctx context.Context, packagePath string) (*cmfplugin.PluginInfo, error) {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	if packagePath == "" {
		return nil, fmt.Errorf("%w: package path is empty", ErrInvalidPackage)
	}
	absPath, err := filepath.Abs(packagePath)
	if err != nil || !cmfplugin.IsWithinRoot(m.opts.PackageRoot, absPath) {
		return nil, ErrInvalidPath
	}
	if _, err := os.Lstat(absPath); err != nil {
		return nil, fmt.Errorf("%w: inspect package: %v", ErrInvalidPackage, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(m.opts.PackageRoot)
	if err != nil {
		return nil, ErrInvalidPath
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil || !cmfplugin.IsWithinRoot(resolvedRoot, resolvedPath) {
		return nil, ErrInvalidPath
	}
	fileInfo, err := os.Stat(resolvedPath)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() > maxPluginPackageSize {
		return nil, fmt.Errorf("%w: package must be a regular file no larger than %d bytes", ErrInvalidPackage, maxPluginPackageSize)
	}

	if err := os.MkdirAll(m.opts.PluginRoot, 0755); err != nil {
		return nil, err
	}
	stageRoot, err := os.MkdirTemp(m.opts.PluginRoot, ".install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageRoot)
	snapshotPath := filepath.Join(stageRoot, "package.zip")
	if err := copyPluginPackage(resolvedPath, snapshotPath); err != nil {
		return nil, fmt.Errorf("%w: snapshot package: %v", ErrInvalidPackage, err)
	}
	packageDigest, signer, err := verifyPluginPackage(snapshotPath, resolvedPath+signatureFileSuffix, m.opts)
	if err != nil {
		return nil, fmt.Errorf("%w: verify package trust: %v", ErrInvalidPackage, err)
	}
	if err := validatePluginArchive(snapshotPath); err != nil {
		return nil, fmt.Errorf("%w: validate archive: %v", ErrInvalidPackage, err)
	}
	extractPath := filepath.Join(stageRoot, "payload")
	if err := cmfplugin.SafeUnzip(snapshotPath, extractPath); err != nil {
		return nil, fmt.Errorf("%w: extract archive: %v", ErrInvalidPackage, err)
	}
	manifest, err := cmfplugin.ReadManifestFromDir(extractPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read manifest: %v", ErrInvalidPackage, err)
	}
	if err := validateAdminManifest(manifest); err != nil {
		return nil, fmt.Errorf("%w: validate manifest: %v", ErrInvalidPackage, err)
	}
	contributions, err := pluginapi.LoadContributions(extractPath)
	if err != nil {
		return nil, fmt.Errorf("%w: read contributions: %v", ErrInvalidPackage, err)
	}
	if err := validateContributions(manifest, contributions); err != nil {
		return nil, fmt.Errorf("%w: validate contributions: %v", ErrInvalidPackage, err)
	}
	if err := writeIntegrityMetadata(extractPath, packageDigest, signer); err != nil {
		return nil, fmt.Errorf("%w: write integrity metadata: %v", ErrInvalidPackage, err)
	}

	existing, err := m.client.Plugin.Query().Where(pluginent.PluginIDEQ(manifest.ID)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if err == nil && existing.Version == manifest.Version {
		return nil, ErrAlreadyInstalled
	}

	targetDir := filepath.Join(m.opts.PluginRoot, manifest.ID, manifest.Version)
	if !safePluginTarget(m.opts.PluginRoot, targetDir) || cmfplugin.IsWithinRoot(stageRoot, targetDir) {
		return nil, ErrInvalidPath
	}
	if err := os.MkdirAll(filepath.Dir(targetDir), 0755); err != nil {
		return nil, err
	}
	if !resolvedWithinRoot(m.opts.PluginRoot, filepath.Dir(targetDir)) {
		return nil, ErrInvalidPath
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, fmt.Errorf("%w: clean target: %v", ErrInvalidPath, err)
	}
	if err := os.Rename(extractPath, targetDir); err != nil {
		return nil, fmt.Errorf("install plugin files: %w", err)
	}

	manifestJSON, err := manifestToMap(manifest)
	if err != nil {
		_ = os.RemoveAll(targetDir)
		return nil, err
	}
	if existing != nil {
		return m.upgradeLocked(ctx, existing, manifest, manifestJSON, targetDir)
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
		_ = os.RemoveAll(targetDir)
		return nil, err
	}
	return recordToInfo(record)
}

func (m *DefaultManager) upgradeLocked(ctx context.Context, current *ent.Plugin, manifest *cmfplugin.PluginManifest, manifestJSON map[string]any, targetDir string) (*cmfplugin.PluginInfo, error) {
	previous, err := recordToInfo(current)
	if err != nil {
		_ = os.RemoveAll(targetDir)
		return nil, err
	}
	wasEnabled := current.Status == pluginent.StatusEnabled
	if wasEnabled {
		if err := m.opts.Runtime.Stop(ctx, current.PluginID); err != nil {
			_ = os.RemoveAll(targetDir)
			return nil, ErrUpgradeFailed
		}
		if err := m.opts.Registry.UnregisterPlugin(current.PluginID); err != nil {
			_ = m.startAndRegister(ctx, current, previous)
			_ = os.RemoveAll(targetDir)
			return nil, ErrUpgradeFailed
		}
	}

	nextStatus := current.Status
	if wasEnabled {
		nextStatus = pluginent.StatusInstalled
	} else if nextStatus == pluginent.StatusError {
		nextStatus = pluginent.StatusDisabled
	}
	updated, err := m.client.Plugin.UpdateOne(current).
		SetName(manifest.Name).
		SetVersion(manifest.Version).
		SetAuthor(manifest.Author).
		SetDescription(manifest.Description).
		SetStatus(nextStatus).
		SetInstallPath(targetDir).
		SetManifestJSON(manifestJSON).
		ClearEnabledAt().
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		if wasEnabled {
			_ = m.startAndRegister(ctx, current, previous)
		}
		_ = os.RemoveAll(targetDir)
		return nil, err
	}
	next, err := recordToInfo(updated)
	if err != nil {
		rollbackErr := m.rollbackUpgrade(ctx, updated, previous, current.EnabledAt, wasEnabled)
		_ = os.RemoveAll(targetDir)
		if rollbackErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrRollbackFailed, rollbackErr)
		}
		return nil, ErrUpgradeFailed
	}
	if !wasEnabled {
		return next, nil
	}
	if err := m.startAndRegister(ctx, updated, next); err != nil {
		rollbackErr := m.rollbackUpgrade(ctx, updated, previous, current.EnabledAt, true)
		_ = os.RemoveAll(targetDir)
		if rollbackErr != nil {
			return nil, fmt.Errorf("%w: upgrade error: %v; rollback error: %v", ErrRollbackFailed, err, rollbackErr)
		}
		return nil, fmt.Errorf("%w: %v", ErrUpgradeFailed, err)
	}
	nextRecord := updated
	updated, err = m.client.Plugin.UpdateOne(nextRecord).
		SetStatus(pluginent.StatusEnabled).
		SetEnabledAt(time.Now()).
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		_ = m.opts.Runtime.Stop(context.Background(), next.ID)
		_ = m.opts.Registry.UnregisterPlugin(next.ID)
		rollbackErr := m.rollbackUpgrade(ctx, nextRecord, previous, current.EnabledAt, true)
		if rollbackErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrRollbackFailed, rollbackErr)
		}
		return nil, ErrUpgradeFailed
	}
	return recordToInfo(updated)
}

func (m *DefaultManager) rollbackUpgrade(ctx context.Context, current *ent.Plugin, previous *cmfplugin.PluginInfo, previousEnabledAt *time.Time, restart bool) error {
	manifestJSON, err := manifestToMap(previous.Manifest)
	if err != nil {
		return err
	}
	status := pluginent.Status(previous.Status)
	if restart {
		status = pluginent.StatusInstalled
	}
	restored, err := m.client.Plugin.UpdateOne(current).
		SetName(previous.Name).
		SetVersion(previous.Version).
		SetAuthor(previous.Author).
		SetDescription(previous.Description).
		SetStatus(status).
		SetInstallPath(previous.InstallPath).
		SetManifestJSON(manifestJSON).
		ClearEnabledAt().
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		return err
	}
	if !restart {
		return nil
	}
	if err := m.startAndRegister(ctx, restored, previous); err != nil {
		return err
	}
	update := m.client.Plugin.UpdateOne(restored).
		SetStatus(pluginent.StatusEnabled).
		ClearErrorMessage()
	if previousEnabledAt != nil {
		update.SetEnabledAt(*previousEnabledAt)
	} else {
		update.SetEnabledAt(time.Now())
	}
	_, err = update.Save(ctx)
	return err
}

func (m *DefaultManager) Enable(ctx context.Context, pluginID string) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()
	return m.enableLocked(ctx, pluginID)
}

func (m *DefaultManager) enableLocked(ctx context.Context, pluginID string) error {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return err
	}
	info, err := recordToInfo(record)
	if err != nil {
		return err
	}
	if err := m.startAndRegister(ctx, record, info); err != nil {
		return err
	}
	_, err = m.client.Plugin.UpdateOne(record).
		SetStatus(pluginent.StatusEnabled).
		SetEnabledAt(time.Now()).
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		_ = m.opts.Runtime.Stop(context.Background(), pluginID)
		_ = m.opts.Registry.UnregisterPlugin(pluginID)
		return err
	}
	return nil
}

func (m *DefaultManager) RestoreEnabled(ctx context.Context) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	records, err := m.client.Plugin.Query().Where(pluginent.StatusEQ(pluginent.StatusEnabled)).All(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, record := range records {
		info, err := recordToInfo(record)
		if err != nil {
			_ = m.markError(ctx, record, err)
			errs = append(errs, fmt.Errorf("restore plugin %s: %w", record.PluginID, err))
			continue
		}
		if err := m.startAndRegister(ctx, record, info); err != nil {
			errs = append(errs, fmt.Errorf("restore plugin %s: %w", record.PluginID, err))
		}
	}
	return errors.Join(errs...)
}

func (m *DefaultManager) startAndRegister(ctx context.Context, record *ent.Plugin, info *cmfplugin.PluginInfo) error {
	if err := m.recoveryCtx.Err(); err != nil {
		return ErrManagerClosed
	}
	installPath, err := filepath.Abs(info.InstallPath)
	if err != nil {
		_ = m.markError(ctx, record, err)
		return ErrInvalidPath
	}
	if !safePluginTarget(m.opts.PluginRoot, installPath) || !resolvedWithinRoot(m.opts.PluginRoot, installPath) {
		_ = m.markError(ctx, record, ErrInvalidPath)
		return ErrInvalidPath
	}
	info = clonePluginInfoWithInstallPath(info, installPath)
	// Older hosts kept their TCP endpoint file below the version directory.
	// Runtime state now lives in TempRoot; remove the reserved legacy directory
	// before checking that the installed package tree is unchanged.
	if err := os.RemoveAll(filepath.Join(installPath, ".go-admin-runtime")); err != nil {
		_ = m.markError(ctx, record, err)
		return fmt.Errorf("%w: clean legacy runtime state: %v", ErrEnableFailed, err)
	}
	if err := verifyPluginIntegrity(installPath); err != nil {
		_ = m.markError(ctx, record, err)
		return fmt.Errorf("%w: %v", ErrEnableFailed, err)
	}
	contributions, err := pluginapi.LoadContributions(installPath)
	if err != nil {
		_ = m.markError(ctx, record, err)
		return fmt.Errorf("%w: %v", ErrEnableFailed, err)
	}
	if err := validateContributions(info.Manifest, contributions); err != nil {
		_ = m.markError(ctx, record, err)
		return fmt.Errorf("%w: %v", ErrEnableFailed, err)
	}
	_ = m.opts.Registry.UnregisterPlugin(info.ID)
	registered := false
	for _, point := range info.Manifest.ExtensionPoints {
		payload, err := contributionPayload(contributions, point)
		if err == nil {
			err = m.opts.Registry.Register(info.ID, point, payload)
		}
		if err != nil {
			if registered {
				_ = m.opts.Registry.UnregisterPlugin(info.ID)
			}
			_ = m.markError(ctx, record, err)
			return err
		}
		registered = true
	}
	if info.Manifest.Engine.Type == cmfplugin.EngineTypeProcess {
		if err := m.opts.Runtime.Start(ctx, *info); err != nil {
			_ = m.opts.Registry.UnregisterPlugin(info.ID)
			_ = m.markError(ctx, record, err)
			return ErrEnableFailed
		}
	}
	if err := m.opts.Runtime.Health(ctx, info.ID); err != nil {
		_ = m.opts.Runtime.Stop(ctx, info.ID)
		_ = m.opts.Registry.UnregisterPlugin(info.ID)
		_ = m.markError(ctx, record, err)
		return ErrEnableFailed
	}
	return nil
}

func clonePluginInfoWithInstallPath(info *cmfplugin.PluginInfo, installPath string) *cmfplugin.PluginInfo {
	clone := *info
	clone.InstallPath = installPath
	return &clone
}

func (m *DefaultManager) Disable(ctx context.Context, pluginID string) error {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()
	return m.disableLocked(ctx, pluginID)
}

func (m *DefaultManager) disableLocked(ctx context.Context, pluginID string) error {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return err
	}
	if record.Status != pluginent.StatusEnabled && record.Status != pluginent.StatusError {
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
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return err
	}
	if record.Status == pluginent.StatusEnabled || record.Status == pluginent.StatusError {
		if err := m.disableLocked(ctx, pluginID); err != nil {
			return err
		}
	}
	pluginDir := filepath.Join(m.opts.PluginRoot, record.PluginID)
	if !safePluginTarget(m.opts.PluginRoot, pluginDir) {
		return ErrInvalidPath
	}
	filesExist := true
	if _, err := os.Lstat(pluginDir); os.IsNotExist(err) {
		filesExist = false
	} else if err != nil || !resolvedWithinRoot(m.opts.PluginRoot, pluginDir) {
		return ErrInvalidPath
	}
	trashPath := pluginDir + fmt.Sprintf(".uninstall-%d", time.Now().UnixNano())
	if filesExist {
		if err := os.Rename(pluginDir, trashPath); err != nil {
			return ErrUninstallFailed
		}
	}
	if err := m.client.Plugin.DeleteOne(record).Exec(ctx); err != nil {
		if filesExist {
			_ = os.Rename(trashPath, pluginDir)
		}
		return err
	}
	if filesExist {
		if err := os.RemoveAll(trashPath); err != nil {
			return ErrUninstallFailed
		}
	}
	return nil
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

func (m *DefaultManager) Diagnostics(ctx context.Context, pluginID string) (*Diagnostics, error) {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	m.recoveryMu.Lock()
	_, restarting := m.recovering[pluginID]
	m.recoveryMu.Unlock()
	return &Diagnostics{
		ErrorMessage: record.ErrorMessage,
		InstalledAt:  record.InstalledAt,
		EnabledAt:    record.EnabledAt,
		UpdatedAt:    record.UpdatedAt,
		Restarting:   restarting,
	}, nil
}

func (m *DefaultManager) Authorize(ctx context.Context, pluginID string, method string, requestPath string) error {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return err
	}
	if record.Status != pluginent.StatusEnabled {
		return ErrNotEnabled
	}
	for _, extension := range m.opts.Registry.List(cmfplugin.ExtensionAdminRoutes) {
		if extension.PluginID != pluginID {
			continue
		}
		routes, ok := extension.Payload.([]pluginapi.AdminRouteContribution)
		if ok && routeContributionAllows(routes, method, requestPath) {
			return nil
		}
	}
	return ErrPermissionDenied
}

func (m *DefaultManager) ResolveEndpoint(ctx context.Context, pluginID string) (*Endpoint, error) {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	if record.Status != pluginent.StatusEnabled {
		return nil, ErrNotEnabled
	}
	info, err := recordToInfo(record)
	if err != nil {
		return nil, err
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	engine := info.Manifest.Engine
	transport := engine.Transport[platform]
	if transport == "" {
		transport = cmfplugin.TransportTCP
	}
	endpoint := &Endpoint{
		PluginID:  info.ID,
		Protocol:  engine.Protocol,
		Transport: transport,
		Address:   engine.Address[platform],
	}
	if resolver, ok := m.opts.Runtime.(runtimeEndpointResolver); ok {
		runtimeEndpoint, found := resolver.Endpoint(pluginID)
		if !found {
			_ = m.markError(ctx, record, cmfplugin.ErrRuntimeNotStarted)
			return nil, ErrNotEnabled
		}
		endpoint.Transport = runtimeEndpoint.Transport
		endpoint.Address = runtimeEndpoint.Address
		endpoint.Socket = runtimeEndpoint.Socket
		endpoint.Token = runtimeEndpoint.Token
		return endpoint, nil
	}
	if endpoint.Transport == cmfplugin.TransportTCP && endpoint.Address == "" {
		return nil, ErrInvalidPath
	}
	if endpoint.Transport == cmfplugin.TransportUnix {
		socket := engine.Socket[platform]
		if socket == "" {
			return nil, ErrInvalidPath
		}
		if filepath.IsAbs(socket) {
			return nil, ErrInvalidPath
		}
		endpoint.Socket = filepath.Join(info.InstallPath, socket)
		if !cmfplugin.IsWithinRoot(info.InstallPath, endpoint.Socket) {
			return nil, ErrInvalidPath
		}
	}
	return endpoint, nil
}

func (m *DefaultManager) Invoke(ctx context.Context, pluginID string, request *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error) {
	record, err := m.findRecord(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	if record.Status != pluginent.StatusEnabled {
		return nil, ErrNotEnabled
	}
	invoker, ok := m.opts.Runtime.(runtimeInvoker)
	if !ok {
		return nil, ErrUnsupportedTransport
	}
	response, err := invoker.Invoke(ctx, pluginID, request)
	if err != nil {
		if errors.Is(err, cmfplugin.ErrRuntimeNotStarted) {
			_ = m.markError(ctx, record, err)
		}
		return nil, err
	}
	return response, nil
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

func (m *DefaultManager) markRuntimeExited(pluginID string, cause error) {
	m.opsMu.Lock()
	defer m.opsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	record, err := m.findRecord(ctx, pluginID)
	if err != nil || record.Status != pluginent.StatusEnabled {
		return
	}
	if cause == nil {
		cause = cmfplugin.ErrRuntimeNotStarted
	}
	cmflog.Warn("插件运行时异常退出", zap.String("plugin_id", pluginID), zap.Error(cause))
	m.recoveryMu.Lock()
	if m.closed {
		m.recoveryMu.Unlock()
		return
	}
	if _, exists := m.recovering[pluginID]; exists {
		m.recoveryMu.Unlock()
		return
	}
	m.recovering[pluginID] = struct{}{}
	m.recoveryWG.Add(1)
	m.recoveryMu.Unlock()
	_ = m.markError(ctx, record, cause)
	go m.recoverRuntime(pluginID, cause)
}

func (m *DefaultManager) recoverRuntime(pluginID string, cause error) {
	defer func() {
		m.recoveryMu.Lock()
		delete(m.recovering, pluginID)
		m.recoveryMu.Unlock()
		m.recoveryWG.Done()
	}()

	lastErr := cause
	for attempt := 0; attempt < runtimeRestartAttempts; attempt++ {
		timer := time.NewTimer(runtimeRestartBaseDelay << attempt)
		select {
		case <-timer.C:
		case <-m.recoveryCtx.Done():
			timer.Stop()
			return
		}
		ctx, cancel := context.WithTimeout(m.recoveryCtx, runtimeRecoveryTimeout)

		m.opsMu.Lock()
		record, err := m.findRecord(ctx, pluginID)
		if err == nil && record.Status != pluginent.StatusEnabled && record.Status != pluginent.StatusError {
			m.opsMu.Unlock()
			cancel()
			return
		}
		if err == nil {
			var info *cmfplugin.PluginInfo
			info, err = recordToInfo(record)
			if err == nil {
				err = m.startAndRegister(ctx, record, info)
			}
			if err == nil {
				_, err = m.client.Plugin.UpdateOne(record).
					SetStatus(pluginent.StatusEnabled).
					SetEnabledAt(time.Now()).
					ClearErrorMessage().
					Save(ctx)
				if err != nil {
					_ = m.opts.Runtime.Stop(context.Background(), pluginID)
					_ = m.opts.Registry.UnregisterPlugin(pluginID)
				}
			}
		}
		m.opsMu.Unlock()
		cancel()

		if err == nil {
			cmflog.Info("插件运行时恢复成功", zap.String("plugin_id", pluginID), zap.Int("attempt", attempt+1))
			return
		}
		if errors.Is(err, ErrNotFound) {
			return
		}
		if errors.Is(err, ErrManagerClosed) || errors.Is(err, context.Canceled) {
			return
		}
		lastErr = err
		cmflog.Warn("插件运行时恢复失败", zap.String("plugin_id", pluginID), zap.Int("attempt", attempt+1), zap.Error(err))
	}

	ctx, cancel := context.WithTimeout(m.recoveryCtx, 3*time.Second)
	defer cancel()
	m.opsMu.Lock()
	defer m.opsMu.Unlock()
	record, err := m.findRecord(ctx, pluginID)
	if err == nil && (record.Status == pluginent.StatusEnabled || record.Status == pluginent.StatusError) {
		exhaustedErr := fmt.Errorf("runtime exited (%v); restart attempts exhausted: %w", cause, lastErr)
		_ = m.markError(ctx, record, exhaustedErr)
		cmflog.Error("插件运行时恢复次数耗尽", zap.String("plugin_id", pluginID), zap.Error(exhaustedErr))
	}
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
	if err := validateAdminManifest(manifest); err != nil {
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

func copyPluginPackage(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(target, io.LimitReader(source, maxPluginPackageSize+1))
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxPluginPackageSize {
		return errors.New("plugin package is too large")
	}
	return nil
}

func validatePluginArchive(packagePath string) error {
	reader, err := zip.OpenReader(packagePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxPluginFiles {
		return errors.New("invalid plugin file count")
	}
	var total uint64
	for _, file := range reader.File {
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin archive contains symlink %q", file.Name)
		}
		if file.UncompressedSize64 > maxPluginFileSize || total > maxPluginUnpackedSize-file.UncompressedSize64 {
			return errors.New("plugin archive is too large after extraction")
		}
		total += file.UncompressedSize64
	}
	return nil
}

func validateAdminManifest(manifest *cmfplugin.PluginManifest) error {
	if manifest == nil || !safePluginVersionPattern.MatchString(manifest.Version) {
		return errors.New("invalid plugin version")
	}
	if manifest.Compatibility.API != "" && manifest.Compatibility.API != "v1" {
		return fmt.Errorf("unsupported plugin API %q", manifest.Compatibility.API)
	}
	hostVersion, err := version.NewVersion(HostVersion)
	if err != nil {
		return fmt.Errorf("invalid host version %q", HostVersion)
	}
	if strings.TrimSpace(manifest.Compatibility.App) == "" {
		return errors.New("plugin app compatibility is required")
	}
	appConstraint := normalizeAppConstraint(manifest.Compatibility.App)
	constraints, err := version.NewConstraint(appConstraint)
	if err != nil {
		return fmt.Errorf("invalid plugin app compatibility %q: %w", manifest.Compatibility.App, err)
	}
	if !constraints.Check(hostVersion) {
		return fmt.Errorf("plugin requires app %q, host is %s", manifest.Compatibility.App, hostVersion)
	}
	if err := validateManifestCapabilities(manifest); err != nil {
		return err
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if manifest.Engine.Entry[platform] == "" {
		return fmt.Errorf("missing plugin entry for %s", platform)
	}
	transport := manifest.Engine.Transport[platform]
	if transport == "" {
		if manifest.Engine.Protocol == cmfplugin.ProtocolStdio {
			transport = cmfplugin.TransportStdio
		} else {
			transport = cmfplugin.TransportTCP
		}
	}
	switch manifest.Engine.Protocol {
	case cmfplugin.ProtocolHTTP, cmfplugin.ProtocolGRPC:
		if transport != cmfplugin.TransportTCP && transport != cmfplugin.TransportUnix {
			return fmt.Errorf("protocol %s does not support transport %s", manifest.Engine.Protocol, transport)
		}
		if transport == cmfplugin.TransportUnix && manifest.Engine.Socket[platform] == "" {
			return fmt.Errorf("missing plugin socket for %s", platform)
		}
	case cmfplugin.ProtocolStdio:
		if transport != cmfplugin.TransportStdio {
			return fmt.Errorf("stdio protocol requires stdio transport")
		}
	default:
		return fmt.Errorf("unsupported plugin protocol %q", manifest.Engine.Protocol)
	}
	return nil
}

func normalizeAppConstraint(value string) string {
	parts := strings.Split(value, ",")
	for index, part := range parts {
		parts[index] = appConstraintSeparatorPattern.ReplaceAllString(strings.TrimSpace(part), ", $1")
	}
	return strings.Join(parts, ", ")
}

func safePluginTarget(pluginRoot, target string) bool {
	absRoot, err := filepath.Abs(pluginRoot)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil || absTarget == absRoot {
		return false
	}
	return cmfplugin.IsWithinRoot(absRoot, absTarget)
}

func resolvedWithinRoot(root, target string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false
	}
	if samePath(resolvedRoot, resolvedTarget) {
		return false
	}
	return cmfplugin.IsWithinRoot(resolvedRoot, resolvedTarget)
}

func samePath(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}
