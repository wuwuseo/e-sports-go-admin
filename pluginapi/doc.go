// Package pluginapi defines the wire contract shared by go-admin and plugin
// executables.
//
// A plugin selects "http", "grpc", or "stdio" in plugin.yaml and calls the
// matching ServeHTTP, ServeGRPC, or ServeStdio helper from main. The host
// supplies the private transport address and per-start authentication token
// through PLUGIN_* environment variables; plugins must not bind a manifest
// address directly.
//
//go:generate protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative plugin.proto
package pluginapi
