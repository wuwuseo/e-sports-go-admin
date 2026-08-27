package middleware

import (
	"testing"
)

// -------------------------------------------------------------------
// 权限中间件纯函数边界条件补充测试
// 覆盖 hasRoleName / splitPermissionPath / fiberRouteSegmentMatchesRequest
//      / isFiberWildcardSegment / appendUniquePermissionObject
//      以及 normalizePermissionRoutePath / isSelfPermissionEndpoint 边界
// -------------------------------------------------------------------

func TestHasRoleName_FindsExactMatch(t *testing.T) {
	tests := []struct {
		name   string
		roles  []string
		target string
		want   bool
	}{
		{"空列表不匹配", nil, "admin", false},
		{"空切片不匹配", []string{}, "admin", false},
		{"单个匹配", []string{"admin"}, "admin", true},
		{"多个中命中", []string{"user", "admin", "auditor"}, "admin", true},
		{"大小写敏感", []string{"Admin"}, "admin", false},
		{"前缀不匹配", []string{"administrator"}, "admin", false},
		{"后缀不匹配", []string{"site-admin"}, "admin", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasRoleName(tt.roles, tt.target); got != tt.want {
				t.Fatalf("hasRoleName(%v, %q) = %v, want %v", tt.roles, tt.target, got, tt.want)
			}
		})
	}
}

func TestSplitPermissionPath_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		path string
		want int // 期望长度
	}{
		{"空字符串", "", 0},
		{"仅斜杠", "/", 0},
		{"多个斜杠", "///", 0},
		{"前后斜杠被清除", "/a/b/c/", 3},
		{"单段", "/api", 1},
		{"无首斜杠单段", "api", 1},
		{"中间空段视为段", "/a//b", 3}, // strings.Split 会生成 "" 段
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitPermissionPath(tt.path)
			if len(got) != tt.want {
				t.Fatalf("splitPermissionPath(%q) 长度 = %d, want %d, 内容=%v",
					tt.path, len(got), tt.want, got)
			}
		})
	}
}

func TestFiberRouteSegmentMatchesRequest_EdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		route string
		req   string
		want  bool
	}{
		{"完全相等静态段", "roles", "roles", true},
		{"大小写敏感", "Roles", "roles", false},
		{"参数名只有一个字符 :x 可匹配", ":x", "anything", true},
		{"参数段 :id 不接受空字符串", ":id", "", false},
		{"仅冒号不算参数", ":", "x", false}, // len(routeSegment) > 1 才是参数
		{"静态段完全不匹配", "abc", "abd", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fiberRouteSegmentMatchesRequest(tt.route, tt.req); got != tt.want {
				t.Fatalf("fiberRouteSegmentMatchesRequest(%q, %q) = %v, want %v",
					tt.route, tt.req, got, tt.want)
			}
		})
	}
}

func TestIsFiberWildcardSegment(t *testing.T) {
	tests := []struct {
		seg  string
		want bool
	}{
		{"*", true},
		{"*filepath", true}, // strings.HasPrefix(segment, "*")
		{"**", true},
		{":id", false},
		{"static", false},
		{"", false},
		{" */", false}, // 前缀匹配
	}
	for _, tt := range tests {
		t.Run(tt.seg, func(t *testing.T) {
			if got := isFiberWildcardSegment(tt.seg); got != tt.want {
				t.Fatalf("isFiberWildcardSegment(%q) = %v, want %v", tt.seg, got, tt.want)
			}
		})
	}
}

func TestAppendUniquePermissionObject_DedupAndEmpty(t *testing.T) {
	t.Run("空字符串被忽略", func(t *testing.T) {
		seen := make(map[string]struct{})
		got := appendUniquePermissionObject(nil, seen, "")
		if len(got) != 0 {
			t.Fatalf("期望空，got %v", got)
		}
	})
	t.Run("首次出现会追加", func(t *testing.T) {
		seen := make(map[string]struct{})
		got := appendUniquePermissionObject(nil, seen, "/a")
		if len(got) != 1 || got[0] != "/a" {
			t.Fatalf("期望 [/a], got %v", got)
		}
		if _, ok := seen["/a"]; !ok {
			t.Fatal("seen 未记录 /a")
		}
	})
	t.Run("重复值被去重", func(t *testing.T) {
		seen := make(map[string]struct{})
		var got []string
		got = appendUniquePermissionObject(got, seen, "/x")
		got = appendUniquePermissionObject(got, seen, "/y")
		got = appendUniquePermissionObject(got, seen, "/x") // 重复
		if len(got) != 2 {
			t.Fatalf("期望长度 2, got %d, 内容=%v", len(got), got)
		}
	})
	t.Run("nil 切片和空切片均正常", func(t *testing.T) {
		seen := make(map[string]struct{})
		a := appendUniquePermissionObject(nil, seen, "/p")
		b := appendUniquePermissionObject([]string{}, seen, "/q")
		// /p 在 seen 中，/q 首次出现
		if len(a) != 1 || a[0] != "/p" {
			t.Fatalf("a = %v", a)
		}
		if len(b) != 1 || b[0] != "/q" {
			t.Fatalf("b = %v", b)
		}
	})
}

func TestNormalizePermissionRoutePath_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"空字符串不变", "", ""},
		{"无参数路径不变", "/api/v1/health", "/api/v1/health"},
		{"单参数", "/user/:id", "/user/{id}"},
		{"多个连续参数", "/:a/:b/:c", "/{a}/{b}/{c}"},
		{"静态与参数混合", "/api/:ver/entity/:id/action", "/api/{ver}/entity/{id}/action"},
		{"只有冒号无名称不替换", "/a/:/b", "/a/:/b"},   // len(part) > 1 才处理，":" 长度=1
		{"双冒号开头", "/a/::id/b", "/a/{:id}/b"}, // TrimPrefix(":") 后剩 ":id" -> {:id}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePermissionRoutePath(tt.in); got != tt.want {
				t.Fatalf("normalizePermissionRoutePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsSelfPermissionEndpoint_MethodAndPathBoundaries(t *testing.T) {
	// 方法必须是 GET
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		if isSelfPermissionEndpoint("/api/v1/admin/auth/user/permissions", m) {
			t.Fatalf("method %s 不应通过 self-endpoint 豁免", m)
		}
	}
	// 路径必须精确匹配
	cases := []string{
		"/api/v1/admin/auth/user/permission",         // 少了 s
		"/api/v1/admin/auth/user/permissions/",       // 多了尾斜杠
		"/api/v2/admin/auth/user/permissions",        // 版本不对
		"/prefix/api/v1/admin/auth/user/permissions", // 多了前缀
		"api/v1/admin/auth/user/permissions",         // 缺首斜杠
	}
	for _, p := range cases {
		if isSelfPermissionEndpoint(p, "GET") {
			t.Fatalf("非精确匹配路径 %q 不应被豁免", p)
		}
	}
}

func TestFiberRoutePathMatchesRequest_MoreEdges(t *testing.T) {
	t.Run("路由段比请求段多 → false", func(t *testing.T) {
		got := fiberRoutePathMatchesRequest("/a/b/c", "/a/b")
		if got {
			t.Fatal("路由更长不应匹配")
		}
	})
	t.Run("请求段比路由段多 → false", func(t *testing.T) {
		got := fiberRoutePathMatchesRequest("/a/b", "/a/b/c")
		if got {
			t.Fatal("请求更长不应匹配（除非路由有通配符）")
		}
	})
	t.Run("通配符在中间位置提前匹配剩余", func(t *testing.T) {
		// 实现中：遇到通配段直接 return true，不考虑后续段
		got := fiberRoutePathMatchesRequest("/a/*/c", "/a/x/y/z")
		if !got {
			// ponytail: 按当前实现遇到 '*' 即返回 true，中间段通配符亦生效
			t.Fatal("中间通配段应使整体判定为 true")
		}
	})
	t.Run("两段静态路径完全相同 → true", func(t *testing.T) {
		if !fiberRoutePathMatchesRequest("/health/live", "/health/live") {
			t.Fatal("完全相同静态段应匹配")
		}
	})
	t.Run("两段静态路径末尾差异 → false", func(t *testing.T) {
		if fiberRoutePathMatchesRequest("/health/live", "/health/ready") {
			t.Fatal("末尾不同不应匹配")
		}
	})
	t.Run("参数段接收 URL 编码的值", func(t *testing.T) {
		got := fiberRoutePathMatchesRequest("/user/:name", "/user/hello%20world")
		if !got {
			t.Fatal("参数段应接受任何非空字符串，包括 URL 编码")
		}
	})
}
