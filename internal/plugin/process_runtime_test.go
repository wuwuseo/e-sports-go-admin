package plugin

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go-admin/pluginapi"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

const runtimeHelperPluginID = "runtime.helper"

func TestMain(m *testing.M) {
	if os.Getenv("PLUGIN_ID") == runtimeHelperPluginID {
		os.Exit(runPluginRuntimeHelper())
	}
	os.Exit(m.Run())
}

func TestProcessRuntimeSupportsHTTPGRPCAndStdio(t *testing.T) {
	for _, protocol := range []string{cmfplugin.ProtocolHTTP, cmfplugin.ProtocolGRPC, cmfplugin.ProtocolStdio} {
		t.Run(protocol, func(t *testing.T) {
			testProcessRuntimeProtocol(t, protocol)
		})
	}
}

func TestProcessRuntimeReportsUnexpectedExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	processRuntime := NewProcessRuntime()
	exited := make(chan error, 1)
	processRuntime.SetExitHandler(func(pluginID string, err error) {
		if pluginID == runtimeHelperPluginID {
			exited <- err
		}
	})
	manifest := &cmfplugin.PluginManifest{
		ID: runtimeHelperPluginID, Name: "Runtime helper", Version: "1.0.0",
		ExtensionPoints: []string{cmfplugin.ExtensionAdminRoutes},
		Engine: cmfplugin.PluginEngine{
			Type: cmfplugin.EngineTypeProcess, Protocol: cmfplugin.ProtocolStdio,
			Transport: map[string]string{platform: cmfplugin.TransportStdio},
			Entry:     map[string]string{platform: filepath.Base(executable)},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info := cmfplugin.PluginInfo{
		ID: runtimeHelperPluginID, InstallPath: filepath.Dir(executable), Manifest: manifest,
	}
	if err := processRuntime.Start(ctx, info); err != nil {
		t.Fatal(err)
	}
	process, err := processRuntime.process(runtimeHelperPluginID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatal("unexpected plugin exit was not reported")
	}
	if err := processRuntime.Start(ctx, info); err != nil {
		t.Fatalf("restart after unexpected exit: %v", err)
	}
	if err := processRuntime.Health(ctx, runtimeHelperPluginID); err != nil {
		t.Fatalf("health after restart: %v", err)
	}
	if err := processRuntime.Stop(ctx, runtimeHelperPluginID); err != nil {
		t.Fatalf("stop restarted runtime: %v", err)
	}
}

func testProcessRuntimeProtocol(t *testing.T, protocol string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	transport := cmfplugin.TransportTCP
	if protocol == cmfplugin.ProtocolStdio {
		transport = cmfplugin.TransportStdio
	}
	manifest := &cmfplugin.PluginManifest{
		ID:              runtimeHelperPluginID,
		Name:            "Runtime helper",
		Version:         "1.0.0",
		ExtensionPoints: []string{cmfplugin.ExtensionAdminRoutes},
		Engine: cmfplugin.PluginEngine{
			Type:      cmfplugin.EngineTypeProcess,
			Protocol:  protocol,
			Transport: map[string]string{platform: transport},
			Entry:     map[string]string{platform: filepath.Base(executable)},
		},
	}
	processRuntime := NewProcessRuntime()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := processRuntime.Start(ctx, cmfplugin.PluginInfo{
		ID: runtimeHelperPluginID, InstallPath: filepath.Dir(executable), Manifest: manifest,
	}); err != nil {
		t.Fatalf("Start(%s): %v", protocol, err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer stopCancel()
		if err := processRuntime.Stop(stopCtx, runtimeHelperPluginID); err != nil {
			t.Errorf("Stop(%s): %v", protocol, err)
		}
	})
	if err := processRuntime.Health(ctx, runtimeHelperPluginID); err != nil {
		t.Fatalf("Health(%s): %v", protocol, err)
	}
	process, err := processRuntime.process(runtimeHelperPluginID)
	if err != nil {
		t.Fatal(err)
	}

	endpoint, ok := processRuntime.Endpoint(runtimeHelperPluginID)
	if !ok || endpoint.Token == "" {
		t.Fatalf("Endpoint(%s) missing runtime token", protocol)
	}
	if transport == cmfplugin.TransportTCP && !resolvedTCPAddress(endpoint.Address) {
		t.Fatalf("Endpoint(%s) did not publish a bound TCP address: %q", protocol, endpoint.Address)
	}
	if protocol == cmfplugin.ProtocolHTTP {
		client, baseURL, err := runtimeHTTPClient(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/echo?debug=1", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(pluginapi.TokenHeader, endpoint.Token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusCreated || string(body) != "POST /echo?debug=1" {
			t.Fatalf("HTTP response = %d %q", response.StatusCode, body)
		}
	} else {
		response, err := processRuntime.Invoke(ctx, runtimeHelperPluginID, &pluginapi.ProxyRequest{
			Method: http.MethodPost, Path: "/echo", Query: "debug=1", Body: []byte("payload"),
		})
		if err != nil {
			t.Fatalf("Invoke(%s): %v", protocol, err)
		}
		if response.GetStatusCode() != http.StatusCreated || string(response.GetBody()) != "POST /echo?debug=1 payload" {
			t.Fatalf("%s response = %d %q", protocol, response.GetStatusCode(), response.GetBody())
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCancel()
	if err := processRuntime.Stop(stopCtx, runtimeHelperPluginID); err != nil {
		t.Fatalf("Stop(%s): %v", protocol, err)
	}
	process.waitMu.Lock()
	waitErr := process.waitErr
	process.waitMu.Unlock()
	if waitErr != nil {
		t.Fatalf("Stop(%s) was not graceful: %v", protocol, waitErr)
	}
	if process.endpointFile != "" {
		if _, err := os.Stat(process.endpointFile); !os.IsNotExist(err) {
			t.Fatalf("endpoint file still exists after Stop(%s): %v", protocol, err)
		}
	}
	if process.endpoint.runtimeDir != "" {
		if _, err := os.Stat(process.endpoint.runtimeDir); !os.IsNotExist(err) {
			t.Fatalf("runtime directory still exists after Stop(%s): %v", protocol, err)
		}
	}
}

func TestResolveTCPEndpointRejectsNonLoopbackAddress(t *testing.T) {
	endpointFile := filepath.Join(t.TempDir(), "endpoint")
	if err := os.WriteFile(endpointFile, []byte("192.0.2.1:1234"), 0600); err != nil {
		t.Fatal(err)
	}
	process := &runningProcess{endpoint: RuntimeEndpoint{
		Transport: cmfplugin.TransportTCP, Address: "127.0.0.1:0", endpointFile: endpointFile,
	}}
	if err := resolveTCPEndpoint(process); err == nil {
		t.Fatal("non-loopback plugin endpoint was accepted")
	}
}

func runPluginRuntimeHelper() int {
	handler := func(_ context.Context, request *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error) {
		body := request.GetMethod() + " " + request.GetPath()
		if request.GetQuery() != "" {
			body += "?" + request.GetQuery()
		}
		if len(request.GetBody()) != 0 {
			body += " " + string(request.GetBody())
		}
		return &pluginapi.ProxyResponse{StatusCode: http.StatusCreated, Body: []byte(body)}, nil
	}

	var err error
	switch os.Getenv(pluginapi.EnvProtocol) {
	case cmfplugin.ProtocolHTTP:
		err = pluginapi.ServeHTTP(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, request.Method+" "+request.URL.RequestURI())
		}))
	case cmfplugin.ProtocolGRPC:
		err = pluginapi.ServeGRPC(handler)
	case cmfplugin.ProtocolStdio:
		err = pluginapi.ServeStdio(context.Background(), handler)
	default:
		return 2
	}
	if err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		return 1
	}
	return 0
}
