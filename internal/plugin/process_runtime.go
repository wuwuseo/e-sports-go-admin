package plugin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go-admin/pluginapi"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const (
	processStartupGrace  = 200 * time.Millisecond
	processStartTimeout  = 5 * time.Second
	processInvokeTimeout = 30 * time.Second
	processShutdownGrace = 2 * time.Second
	processStopTimeout   = 5 * time.Second
)

type RuntimeEndpoint struct {
	Transport    string
	Address      string
	Socket       string
	Token        string
	endpointFile string
	runtimeDir   string
}

type ProcessRuntime struct {
	mu          sync.Mutex
	opMu        sync.Mutex
	procs       map[string]*runningProcess
	exitHandler func(string, error)
	tempRoot    string
}

type runningProcess struct {
	pluginID      string
	cmd           *exec.Cmd
	done          chan struct{}
	waitErr       error
	waitMu        sync.Mutex
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	invokeMu      sync.Mutex
	grpcMu        sync.Mutex
	endpointMu    sync.RWMutex
	endpoint      RuntimeEndpoint
	protocol      string
	pluginVersion string
	apiVersion    string
	endpointFile  string
	grpcConn      *grpc.ClientConn
	expectedStop  atomic.Bool
}

func NewProcessRuntime() *ProcessRuntime {
	return newProcessRuntime(filepath.Join(os.TempDir(), "go-admin-plugins"))
}

func newProcessRuntime(tempRoot string) *ProcessRuntime {
	if absolute, err := filepath.Abs(tempRoot); err == nil {
		tempRoot = absolute
	}
	return &ProcessRuntime{procs: make(map[string]*runningProcess), tempRoot: tempRoot}
}

func (runtimeManager *ProcessRuntime) SetExitHandler(handler func(string, error)) {
	runtimeManager.mu.Lock()
	defer runtimeManager.mu.Unlock()
	runtimeManager.exitHandler = handler
}

func (runtimeManager *ProcessRuntime) Start(ctx context.Context, plugin cmfplugin.PluginInfo) error {
	// ponytail: plugin lifecycle changes are rare; one lock prevents all start/stop races.
	runtimeManager.opMu.Lock()
	defer runtimeManager.opMu.Unlock()

	runtimeManager.mu.Lock()
	if process := runtimeManager.procs[plugin.ID]; process != nil && processRunning(process) {
		runtimeManager.mu.Unlock()
		return nil
	}
	delete(runtimeManager.procs, plugin.ID)
	runtimeManager.mu.Unlock()

	runtimePlugin, endpoint, err := prepareRuntimePlugin(plugin, runtimeManager.tempRoot)
	if err != nil {
		return err
	}
	started := false
	defer func() {
		if !started && endpoint.runtimeDir != "" {
			_ = os.RemoveAll(endpoint.runtimeDir)
		}
	}()
	entry, err := cmfplugin.ResolveProcessEntry(runtimePlugin)
	if err != nil {
		return err
	}
	runtimeEnv, err := cmfplugin.ResolveProcessEnvironment(runtimePlugin)
	if err != nil {
		return err
	}
	if endpoint.Socket != "" {
		runtimeEnv = replaceProcessEnvironment(runtimeEnv, pluginapi.EnvSocket, endpoint.Socket)
	}
	apiVersion := runtimePlugin.Manifest.Compatibility.API
	if apiVersion == "" {
		apiVersion = pluginapi.APIVersion
	}
	runtimeEnv = append(runtimeEnv,
		pluginapi.EnvToken+"="+endpoint.Token,
		pluginapi.EnvPluginVersion+"="+runtimePlugin.Version,
		pluginapi.EnvAPIVersion+"="+apiVersion,
	)
	if endpoint.endpointFile != "" {
		runtimeEnv = append(runtimeEnv, pluginapi.EnvEndpointFile+"="+endpoint.endpointFile)
		_ = os.Remove(endpoint.endpointFile)
	}

	cmd := exec.Command(entry)
	cmd.Dir = runtimePlugin.InstallPath
	cmd.Env = append(safeProcessEnvironment(), runtimeEnv...)
	cmd.Stderr = os.Stderr

	process := &runningProcess{
		pluginID:      plugin.ID,
		cmd:           cmd,
		done:          make(chan struct{}),
		endpoint:      endpoint,
		protocol:      runtimePlugin.Manifest.Engine.Protocol,
		pluginVersion: runtimePlugin.Version,
		apiVersion:    apiVersion,
		endpointFile:  endpoint.endpointFile,
	}
	if process.protocol == cmfplugin.ProtocolStdio {
		process.stdin, err = cmd.StdinPipe()
		if err != nil {
			return err
		}
		process.stdout, err = cmd.StdoutPipe()
		if err != nil {
			_ = process.stdin.Close()
			return err
		}
	} else {
		cmd.Stdout = os.Stdout
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	started = true
	go func() {
		err := cmd.Wait()
		if process.endpoint.runtimeDir != "" {
			_ = os.RemoveAll(process.endpoint.runtimeDir)
		}
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.done)
		if !process.expectedStop.Load() {
			runtimeManager.mu.Lock()
			handler := runtimeManager.exitHandler
			runtimeManager.mu.Unlock()
			if handler != nil {
				handler(plugin.ID, err)
			}
		}
	}()

	runtimeManager.mu.Lock()
	runtimeManager.procs[plugin.ID] = process
	runtimeManager.mu.Unlock()

	timer := time.NewTimer(processStartupGrace)
	defer timer.Stop()
	select {
	case <-process.done:
		runtimeManager.remove(plugin.ID, process)
		return fmt.Errorf("plugin process exited during startup: %w", processError(process))
	case <-ctx.Done():
		_ = runtimeManager.stopProcess(context.Background(), plugin.ID, process)
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (runtimeManager *ProcessRuntime) Stop(ctx context.Context, pluginID string) error {
	runtimeManager.opMu.Lock()
	defer runtimeManager.opMu.Unlock()

	runtimeManager.mu.Lock()
	process := runtimeManager.procs[pluginID]
	if process != nil {
		process.expectedStop.Store(true)
	}
	delete(runtimeManager.procs, pluginID)
	runtimeManager.mu.Unlock()
	if process == nil {
		return nil
	}
	return runtimeManager.stopProcess(ctx, pluginID, process)
}

func (runtimeManager *ProcessRuntime) StopAll(ctx context.Context) error {
	runtimeManager.mu.Lock()
	pluginIDs := make([]string, 0, len(runtimeManager.procs))
	for pluginID := range runtimeManager.procs {
		pluginIDs = append(pluginIDs, pluginID)
	}
	runtimeManager.mu.Unlock()

	var errs []error
	for _, pluginID := range pluginIDs {
		if err := runtimeManager.Stop(ctx, pluginID); err != nil {
			errs = append(errs, fmt.Errorf("stop plugin %s: %w", pluginID, err))
		}
	}
	return errors.Join(errs...)
}

func (runtimeManager *ProcessRuntime) Health(ctx context.Context, pluginID string) error {
	process, err := runtimeManager.process(pluginID)
	if err != nil {
		return err
	}
	healthCtx, cancel := context.WithTimeout(ctx, processStartTimeout)
	defer cancel()

	var lastErr error
	for {
		if !processRunning(process) {
			return fmt.Errorf("plugin process exited: %w", processError(process))
		}
		if err := runtimeManager.checkReady(healthCtx, process); err == nil {
			return nil
		} else {
			lastErr = err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-healthCtx.Done():
			timer.Stop()
			return fmt.Errorf("plugin readiness failed: %w", lastErr)
		case <-process.done:
			timer.Stop()
			return fmt.Errorf("plugin process exited: %w", processError(process))
		case <-timer.C:
		}
	}
}

func (runtimeManager *ProcessRuntime) Invoke(ctx context.Context, pluginID string, request *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error) {
	if request == nil {
		return nil, errors.New("plugin request is nil")
	}
	process, err := runtimeManager.process(pluginID)
	if err != nil {
		return nil, err
	}
	if !processRunning(process) {
		return nil, fmt.Errorf("plugin process exited: %w", processError(process))
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, processInvokeTimeout)
		defer cancel()
	}
	request = proto.Clone(request).(*pluginapi.ProxyRequest)
	request.Token = endpointSnapshot(process).Token
	return runtimeManager.invokeProcess(ctx, process, request)
}

func (runtimeManager *ProcessRuntime) invokeProcess(ctx context.Context, process *runningProcess, request *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error) {
	switch process.protocol {
	case cmfplugin.ProtocolGRPC:
		connection, err := runtimeManager.grpcConnection(process)
		if err != nil {
			return nil, err
		}
		response, err := pluginapi.NewPluginClient(connection).Proxy(ctx, request,
			grpc.MaxCallRecvMsgSize(pluginapi.MaxMessageSize), grpc.MaxCallSendMsgSize(pluginapi.MaxMessageSize))
		if err != nil {
			return nil, err
		}
		if response.GetError() != "" {
			return nil, errors.New(response.GetError())
		}
		return response, nil
	case cmfplugin.ProtocolStdio:
		return runtimeManager.invokeStdio(ctx, process, request)
	default:
		return nil, fmt.Errorf("protocol %q does not support runtime invocation", process.protocol)
	}
}

func (runtimeManager *ProcessRuntime) Endpoint(pluginID string) (RuntimeEndpoint, bool) {
	process, err := runtimeManager.process(pluginID)
	if err != nil || !processRunning(process) {
		return RuntimeEndpoint{}, false
	}
	endpoint := endpointSnapshot(process)
	if endpoint.Transport == cmfplugin.TransportTCP && !resolvedTCPAddress(endpoint.Address) {
		return RuntimeEndpoint{}, false
	}
	return endpoint, true
}

func (runtimeManager *ProcessRuntime) checkReady(ctx context.Context, process *runningProcess) error {
	if err := resolveTCPEndpoint(process); err != nil {
		return err
	}
	endpoint := endpointSnapshot(process)
	request := &pluginapi.ProxyRequest{Method: http.MethodGet, Path: pluginapi.HandshakePath, Token: endpoint.Token}
	switch process.protocol {
	case cmfplugin.ProtocolHTTP:
		client, baseURL, err := runtimeHTTPClient(endpoint)
		if err != nil {
			return err
		}
		healthRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+pluginapi.HandshakePath, nil)
		if err != nil {
			return err
		}
		healthRequest.Header.Set(pluginapi.TokenHeader, endpoint.Token)
		response, err := client.Do(healthRequest)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent || response.Header.Get(pluginapi.TokenHeader) != endpoint.Token {
			return fmt.Errorf("unexpected HTTP handshake response %d", response.StatusCode)
		}
		return validateRuntimeIdentity(response.Header, process)
	case cmfplugin.ProtocolGRPC, cmfplugin.ProtocolStdio:
		response, err := runtimeManager.invokeProcess(ctx, process, request)
		if err != nil {
			return err
		}
		if response.GetStatusCode() != http.StatusNoContent {
			return fmt.Errorf("unexpected %s handshake response %d", process.protocol, response.GetStatusCode())
		}
		return validateRuntimeIdentity(pluginHeaders(response.GetHeaders()), process)
	default:
		return fmt.Errorf("unsupported plugin protocol %q", process.protocol)
	}
}

func (runtimeManager *ProcessRuntime) invokeStdio(ctx context.Context, process *runningProcess, request *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error) {
	process.invokeMu.Lock()
	defer process.invokeMu.Unlock()

	type result struct {
		response *pluginapi.ProxyResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		if err := pluginapi.WriteDelimited(process.stdin, request); err != nil {
			resultCh <- result{err: err}
			return
		}
		response := new(pluginapi.ProxyResponse)
		if err := pluginapi.ReadDelimited(process.stdout, response); err != nil {
			resultCh <- result{err: err}
			return
		}
		if response.GetError() != "" {
			resultCh <- result{err: errors.New(response.GetError())}
			return
		}
		resultCh <- result{response: response}
	}()

	select {
	case result := <-resultCh:
		return result.response, result.err
	case <-ctx.Done():
		_ = process.cmd.Process.Kill()
		return nil, ctx.Err()
	case <-process.done:
		return nil, fmt.Errorf("plugin process exited: %w", processError(process))
	}
}

func (runtimeManager *ProcessRuntime) grpcConnection(process *runningProcess) (*grpc.ClientConn, error) {
	process.grpcMu.Lock()
	defer process.grpcMu.Unlock()
	if process.grpcConn != nil {
		return process.grpcConn, nil
	}

	options := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(pluginapi.MaxMessageSize), grpc.MaxCallSendMsgSize(pluginapi.MaxMessageSize)),
	}
	endpoint := endpointSnapshot(process)
	target := "passthrough:///" + endpoint.Address
	if endpoint.Transport == cmfplugin.TransportUnix {
		socket := endpoint.Socket
		target = "passthrough:///plugin.sock"
		options = append(options, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}))
	}
	connection, err := grpc.NewClient(target, options...)
	if err != nil {
		return nil, err
	}
	process.grpcConn = connection
	return connection, nil
}

func (runtimeManager *ProcessRuntime) process(pluginID string) (*runningProcess, error) {
	runtimeManager.mu.Lock()
	defer runtimeManager.mu.Unlock()
	process := runtimeManager.procs[pluginID]
	if process == nil {
		return nil, cmfplugin.ErrRuntimeNotStarted
	}
	return process, nil
}

func (runtimeManager *ProcessRuntime) remove(pluginID string, expected *runningProcess) {
	runtimeManager.mu.Lock()
	defer runtimeManager.mu.Unlock()
	if runtimeManager.procs[pluginID] == expected {
		delete(runtimeManager.procs, pluginID)
	}
}

func (runtimeManager *ProcessRuntime) stopProcess(ctx context.Context, pluginID string, process *runningProcess) error {
	process.expectedStop.Store(true)
	runtimeManager.remove(pluginID, process)
	if processRunning(process) {
		shutdownCtx, cancel := context.WithTimeout(ctx, processShutdownGrace)
		shutdownErr := runtimeManager.requestShutdown(shutdownCtx, process)
		cancel()
		if shutdownErr == nil {
			select {
			case <-process.done:
				runtimeManager.closeProcessConnections(process)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(processShutdownGrace):
			}
		}
	}
	runtimeManager.closeProcessConnections(process)
	if process.cmd == nil || process.cmd.Process == nil || !processRunning(process) {
		return nil
	}
	if err := process.cmd.Process.Kill(); err != nil && processRunning(process) {
		return err
	}
	timer := time.NewTimer(processStopTimeout)
	defer timer.Stop()
	select {
	case <-process.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("stop plugin process: timeout")
	}
}

func (runtimeManager *ProcessRuntime) requestShutdown(ctx context.Context, process *runningProcess) error {
	endpoint := endpointSnapshot(process)
	request := &pluginapi.ProxyRequest{Method: http.MethodPost, Path: pluginapi.ShutdownPath, Token: endpoint.Token}
	if process.protocol == cmfplugin.ProtocolHTTP {
		client, baseURL, err := runtimeHTTPClient(endpoint)
		if err != nil {
			return err
		}
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+pluginapi.ShutdownPath, nil)
		if err != nil {
			return err
		}
		httpRequest.Header.Set(pluginapi.TokenHeader, endpoint.Token)
		response, err := client.Do(httpRequest)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			return fmt.Errorf("unexpected HTTP shutdown response %d", response.StatusCode)
		}
		return nil
	}
	response, err := runtimeManager.invokeProcess(ctx, process, request)
	if err != nil {
		if !processRunning(process) {
			return nil
		}
		return err
	}
	if response.GetStatusCode() != http.StatusNoContent {
		return fmt.Errorf("unexpected %s shutdown response %d", process.protocol, response.GetStatusCode())
	}
	return nil
}

func (runtimeManager *ProcessRuntime) closeProcessConnections(process *runningProcess) {
	process.grpcMu.Lock()
	connection := process.grpcConn
	process.grpcConn = nil
	process.grpcMu.Unlock()
	if connection != nil {
		_ = connection.Close()
	}
	if process.stdin != nil {
		_ = process.stdin.Close()
	}
}

func validateRuntimeIdentity(headers http.Header, process *runningProcess) error {
	if headers.Get(pluginapi.APIHeader) != process.apiVersion {
		return fmt.Errorf("plugin API mismatch: got %q, want %q", headers.Get(pluginapi.APIHeader), process.apiVersion)
	}
	if headers.Get(pluginapi.PluginIDHeader) != process.pluginID {
		return fmt.Errorf("plugin ID mismatch: got %q, want %q", headers.Get(pluginapi.PluginIDHeader), process.pluginID)
	}
	if headers.Get(pluginapi.PluginVersionHeader) != process.pluginVersion {
		return fmt.Errorf("plugin version mismatch: got %q, want %q", headers.Get(pluginapi.PluginVersionHeader), process.pluginVersion)
	}
	return nil
}

func pluginHeaders(headers []*pluginapi.Header) http.Header {
	result := make(http.Header, len(headers))
	for _, header := range headers {
		if header != nil {
			for _, value := range header.GetValues() {
				result.Add(header.GetName(), value)
			}
		}
	}
	return result
}

func prepareRuntimePlugin(plugin cmfplugin.PluginInfo, tempRoot string) (cmfplugin.PluginInfo, RuntimeEndpoint, error) {
	if plugin.Manifest == nil {
		return plugin, RuntimeEndpoint{}, errors.New("plugin manifest is nil")
	}
	manifest := *plugin.Manifest
	manifest.Engine = cloneEngine(plugin.Manifest.Engine)
	plugin.Manifest = &manifest

	platform := runtime.GOOS + "-" + runtime.GOARCH
	transport := manifest.Engine.Transport[platform]
	if transport == "" {
		if manifest.Engine.Protocol == cmfplugin.ProtocolStdio {
			transport = cmfplugin.TransportStdio
		} else {
			transport = cmfplugin.TransportTCP
		}
	}
	manifest.Engine.Transport[platform] = transport

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return plugin, RuntimeEndpoint{}, err
	}
	endpoint := RuntimeEndpoint{Transport: transport, Token: base64.RawURLEncoding.EncodeToString(tokenBytes)}
	switch transport {
	case cmfplugin.TransportTCP:
		runtimeDir, err := createRuntimeDir(tempRoot, tokenBytes)
		if err != nil {
			return plugin, RuntimeEndpoint{}, err
		}
		endpoint.runtimeDir = runtimeDir
		endpoint.Address = "127.0.0.1:0"
		endpoint.endpointFile = filepath.Join(runtimeDir, "endpoint")
		manifest.Engine.Address[platform] = endpoint.Address
	case cmfplugin.TransportUnix:
		if manifest.Engine.Socket[platform] == "" {
			return plugin, RuntimeEndpoint{}, errors.New("plugin Unix socket is missing")
		}
		runtimeDir, err := createRuntimeDir(tempRoot, tokenBytes)
		if err != nil {
			return plugin, RuntimeEndpoint{}, err
		}
		endpoint.runtimeDir = runtimeDir
		endpoint.Socket = filepath.Join(runtimeDir, "plugin.sock")
	case cmfplugin.TransportStdio:
	default:
		return plugin, RuntimeEndpoint{}, fmt.Errorf("unsupported plugin transport %q", transport)
	}
	return plugin, endpoint, nil
}

func createRuntimeDir(tempRoot string, token []byte) (string, error) {
	if tempRoot == "" {
		return "", errors.New("plugin runtime temporary directory is missing")
	}
	runtimeDir := filepath.Join(tempRoot, base64.RawURLEncoding.EncodeToString(token[:12]))
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		return "", err
	}
	return runtimeDir, nil
}

func replaceProcessEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	for index, item := range environment {
		if strings.HasPrefix(item, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

func cloneEngine(engine cmfplugin.PluginEngine) cmfplugin.PluginEngine {
	engine.Transport = cloneStringMap(engine.Transport)
	engine.Address = cloneStringMap(engine.Address)
	engine.Socket = cloneStringMap(engine.Socket)
	engine.Entry = cloneStringMap(engine.Entry)
	return engine
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func endpointSnapshot(process *runningProcess) RuntimeEndpoint {
	process.endpointMu.RLock()
	defer process.endpointMu.RUnlock()
	return process.endpoint
}

func resolveTCPEndpoint(process *runningProcess) error {
	endpoint := endpointSnapshot(process)
	if endpoint.Transport != cmfplugin.TransportTCP || resolvedTCPAddress(endpoint.Address) {
		return nil
	}
	file, err := os.Open(endpoint.endpointFile)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 257))
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(data) == 0 || len(data) > 256 {
		return errors.New("invalid plugin endpoint file")
	}
	address := strings.TrimSpace(string(data))
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("invalid plugin endpoint address")
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(portText)
	if ip == nil || !ip.IsLoopback() || err != nil || port < 1 || port > 65535 {
		return errors.New("plugin endpoint must be a loopback TCP address")
	}
	process.endpointMu.Lock()
	process.endpoint.Address = net.JoinHostPort(ip.String(), strconv.Itoa(port))
	process.endpointMu.Unlock()
	return nil
}

func resolvedTCPAddress(address string) bool {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port > 0 && port <= 65535
}

func runtimeHTTPClient(endpoint RuntimeEndpoint) (*http.Client, string, error) {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	transport := &http.Transport{}
	baseURL := "http://" + endpoint.Address
	switch endpoint.Transport {
	case cmfplugin.TransportTCP:
		transport.DialContext = dialer.DialContext
	case cmfplugin.TransportUnix:
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", endpoint.Socket)
		}
		baseURL = "http://unix"
	default:
		return nil, "", fmt.Errorf("unsupported HTTP transport %q", endpoint.Transport)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   750 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, baseURL, nil
}

func safeProcessEnvironment() []string {
	allowed := map[string]struct{}{
		"APPDATA": {}, "COMSPEC": {}, "HOME": {}, "LANG": {}, "LC_ALL": {},
		"LOCALAPPDATA": {}, "PATH": {}, "PATHEXT": {}, "SYSTEMROOT": {},
		"TEMP": {}, "TMP": {}, "TMPDIR": {}, "TZ": {}, "USERPROFILE": {}, "WINDIR": {},
	}
	result := make([]string, 0, len(allowed))
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if _, ok := allowed[strings.ToUpper(key)]; ok {
			result = append(result, item)
		}
	}
	return result
}

func processRunning(process *runningProcess) bool {
	select {
	case <-process.done:
		return false
	default:
		return true
	}
}

func processError(process *runningProcess) error {
	process.waitMu.Lock()
	defer process.waitMu.Unlock()
	if process.waitErr != nil {
		return process.waitErr
	}
	return cmfplugin.ErrRuntimeNotStarted
}
