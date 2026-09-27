package handler

import (
	"testing"
)

func TestProtectedProxyPath(t *testing.T) {
	tests := []struct {
		name   string
		prefix any
		path   string
		want   bool
	}{
		{name: "empty path", prefix: nil, path: "", want: false},
		{name: "admin/auth exact match", prefix: nil, path: "admin/auth", want: true},
		{name: "admin/auth with prefix", prefix: "/api", path: "admin/auth", want: true},
		{name: "admin/auth subpath", prefix: nil, path: "admin/auth/login", want: true},
		{name: "admin/auth subpath with prefix", prefix: "/plugin", path: "admin/auth/token", want: true},
		{name: "public namespace", prefix: nil, path: "public/feed", want: true},
		{name: "user namespace", prefix: nil, path: "user/auth/orders", want: true},
		{name: "encoded public namespace", prefix: nil, path: "%2570ublic/feed", want: true},
		{name: "admin/auth with leading slash", prefix: nil, path: "/admin/auth", want: true},
		{name: "admin/auth with trailing slash", prefix: nil, path: "admin/auth/", want: true},
		{name: "other path", prefix: nil, path: "api/user", want: false},
		{name: "admin path without auth", prefix: nil, path: "admin/profile", want: false},
		{name: "auth without admin", prefix: nil, path: "auth/login", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := protectedProxyPath(tt.prefix, tt.path); got != tt.want {
				t.Errorf("protectedProxyPath(%v, %q) = %v, want %v", tt.prefix, tt.path, got, tt.want)
			}
		})
	}
}

func TestProxyPath(t *testing.T) {
	tests := []struct {
		name   string
		prefix any
		path   string
		want   string
	}{
		{name: "empty prefix and empty path", prefix: nil, path: "", want: "/"},
		{name: "empty prefix", prefix: nil, path: "api/v1", want: "/api/v1"},
		{name: "empty path", prefix: "/prefix", path: "", want: "/prefix"},
		{name: "both non-empty", prefix: "/admin", path: "auth", want: "/admin/auth"},
		{name: "prefix with trailing slash", prefix: "/admin/", path: "/auth", want: "/admin/auth"},
		{name: "path with leading slash", prefix: "/admin", path: "/auth/login", want: "/admin/auth/login"},
		{name: "only prefix", prefix: "/api", path: "", want: "/api"},
		{name: "only path", prefix: nil, path: "/users", want: "/users"},
		{name: "empty string prefix", prefix: "", path: "api", want: "/api"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := proxyPath(tt.prefix, tt.path); got != tt.want {
				t.Errorf("proxyPath(%v, %q) = %q, want %q", tt.prefix, tt.path, got, tt.want)
			}
		})
	}
}

func TestFilteredProxyHeader(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "authorization", key: "Authorization", want: true},
		{name: "cookie", key: "Cookie", want: true},
		{name: "set-cookie", key: "Set-Cookie", want: true},
		{name: "host", key: "Host", want: true},
		{name: "connection", key: "Connection", want: true},
		{name: "x-forwarded-for", key: "X-Forwarded-For", want: true},
		{name: "x-forwarded-proto", key: "X-Forwarded-Proto", want: true},
		{name: "x-forwarded-host", key: "X-Forwarded-Host", want: true},
		{name: "content-type", key: "Content-Type", want: false},
		{name: "accept", key: "Accept", want: false},
		{name: "x-custom", key: "X-Custom", want: false},
		{name: "lowercase authorization", key: "authorization", want: true},
		{name: "lowercase x-forwarded-for", key: "x-forwarded-for", want: true},
		{name: "spoofed user identity", key: "X-Go-Admin-Plugin-User-ID", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filteredProxyHeader(tt.key); got != tt.want {
				t.Errorf("filteredProxyHeader(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}
