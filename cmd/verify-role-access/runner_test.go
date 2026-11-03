package main

import (
	"net/http"
	"testing"
	"time"
)

// 菜单比对最容易踩的两类误判：父菜单被自动补齐后当成越权、非 esports 父节点被补进来
// 当成缺菜单。这里把祖先闭包的边界钉死。
func TestWithMenuAncestors(t *testing.T) {
	byCode := map[string]permissionDef{
		"esports:operations:menu": {ID: 10, ParentID: 0, Code: "esports:operations:menu", Type: "menu", MenuPath: "/esports/operations"},
		"esports:orders:menu":     {ID: 11, ParentID: 10, Code: "esports:orders:menu", Type: "menu", MenuPath: "/esports/orders"},
		"esports:orders:list":     {ID: 12, ParentID: 11, Code: "esports:orders:list", Type: "api", Path: "/api/v1/esports/admin/orders"},
		"esports:reports:menu":    {ID: 20, ParentID: 0, Code: "esports:reports:menu", Type: "menu", MenuPath: "/esports/reports"},
		"system:menu":             {ID: 1, ParentID: 0, Code: "system:menu", Type: "menu", MenuPath: "/system"},
	}
	byID := make(map[int]permissionDef, len(byCode))
	for _, def := range byCode {
		byID[def.ID] = def
	}

	t.Run("补齐父级菜单", func(t *testing.T) {
		granted := map[string]string{"/esports/orders": "esports:orders:menu"}
		got := withMenuAncestors(granted, byCode, byID)
		if got["/esports/operations"] != "esports:operations:menu" {
			t.Fatalf("未补齐父级菜单，实际: %#v", got)
		}
		if len(got) != 2 {
			t.Fatalf("期望 2 个菜单，实际 %d: %#v", len(got), got)
		}
	})

	t.Run("不补非菜单节点", func(t *testing.T) {
		granted := map[string]string{"/esports/orders": "esports:orders:menu"}
		got := withMenuAncestors(granted, byCode, byID)
		if _, ok := got[""]; ok {
			t.Fatalf("api 节点不应被当成菜单补入: %#v", got)
		}
	})

	t.Run("已授权父级不重复计入", func(t *testing.T) {
		granted := map[string]string{
			"/esports/operations": "esports:operations:menu",
			"/esports/orders":     "esports:orders:menu",
		}
		got := withMenuAncestors(granted, byCode, byID)
		if len(got) != 2 {
			t.Fatalf("期望 2 个菜单，实际 %d: %#v", len(got), got)
		}
	})

	t.Run("父链缺失时安全终止", func(t *testing.T) {
		orphan := map[string]permissionDef{
			"esports:orphan:menu": {ID: 30, ParentID: 999, Code: "esports:orphan:menu", Type: "menu", MenuPath: "/esports/orphan"},
		}
		got := withMenuAncestors(map[string]string{"/esports/orphan": "esports:orphan:menu"}, orphan, map[int]permissionDef{})
		if len(got) != 1 {
			t.Fatalf("父链缺失不应补出额外菜单，实际: %#v", got)
		}
	})
}

func TestRetryAfterDuration(t *testing.T) {
	t.Run("按 Retry-After 秒数", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", "7")
		if got := retryAfterDuration(h); got != 7*time.Second {
			t.Fatalf("期望 7s，实际 %s", got)
		}
	})

	t.Run("缺失时回退默认值", func(t *testing.T) {
		if got := retryAfterDuration(nil); got != 15*time.Second {
			t.Fatalf("期望 15s，实际 %s", got)
		}
	})

	t.Run("非法值回退默认值", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", "soon")
		if got := retryAfterDuration(h); got != 15*time.Second {
			t.Fatalf("期望 15s，实际 %s", got)
		}
	})
}

// normalizeMenuPath 决定「同一个菜单的两种写法」能否对上，写错会直接产假失败。
func TestNormalizeMenuPath(t *testing.T) {
	cases := map[string]string{
		"/esports/orders":       "/esports/orders",
		"/esports/orders/":      "/esports/orders",
		"/esports/orders?x=1":   "/esports/orders",
		" /esports/orders#top ": "/esports/orders",
		"":                      "/",
		"/":                     "/",
	}
	for in, want := range cases {
		if got := normalizeMenuPath(in); got != want {
			t.Fatalf("normalizeMenuPath(%q) = %q，期望 %q", in, got, want)
		}
	}
}
