package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 错误页面、服务器失败和失效登录均不能作为权限校验通过的证据。
func TestAccessChecksRequireExpectedResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   int
		allow  bool
		deny   bool
	}{
		{"成功", 200, 1, true, false},
		{"未登录", 200, 401, false, false},
		{"权限拒绝", 200, 403, false, true},
		{"HTTP权限拒绝", 403, 403, false, true},
		{"路由缺失", 404, 404, false, false},
		{"服务器失败", 500, 500, false, false},
		{"错误HTTP中的成功码", 500, 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"code":%d,"data":[]}`, tc.code)
			}))
			defer server.Close()
			path := "/api/v1/esports/admin/customers"
			r := &runner{
				perms:    map[string]permissionDef{"customers": {Code: "customers", Type: "api", Path: path, Method: "GET"}},
				policies: map[string][]policyRule{"allowed": {{Object: path, Action: "GET", Type: "api"}}},
			}
			acc := &account{Role: roleInfo{Name: "allowed"}, cli: newAPIClient(server.URL, time.Second)}
			if got := r.checkAllowPermission(acc); got.Passed != tc.allow {
				t.Fatalf("放行误判: %+v", got)
			}
			acc.Role.Name = "denied"
			if got := r.checkDenyPermission(acc); got.Passed != tc.deny {
				t.Fatalf("拒绝误判: %+v", got)
			}
		})
	}
}

func TestMenuCheckIncludesSystemRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"code":1,"data":[{"code":"system","menu_path":"/system","children":[{"code":"system:admin","menu_path":"/system/admin"}]}]}`)
	}))
	defer server.Close()
	r := &runner{
		perms: map[string]permissionDef{
			"system":       {ID: 1, Code: "system", Type: "menu", MenuPath: "/system"},
			"system:admin": {ID: 2, ParentID: 1, Code: "system:admin", Type: "menu", MenuPath: "/system/admin"},
		},
		menuCode: map[string]string{"/system/admin": "system:admin"},
		policies: map[string][]policyRule{"normal_admin": {{Object: "/system/admin", Action: "menu", Type: "menu"}}},
	}
	r.permByID = map[int]permissionDef{1: r.perms["system"], 2: r.perms["system:admin"]}
	acc := &account{Role: roleInfo{Name: "normal_admin"}, cli: newAPIClient(server.URL, time.Second)}
	if got := r.checkMenu(acc); !got.Passed {
		t.Fatalf("系统菜单与自动补齐的父级应参与比对: %+v", got)
	}
	delete(r.policies, "normal_admin")
	if got := r.checkMenu(acc); got.Passed {
		t.Fatal("未授权的系统菜单必须报越权")
	}
}

func TestCustomerVisibilityRejectsLeakedRowsAndInvalidSessions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rows      string
		otherCode int
		ownCode   int
		passed    bool
	}{
		{"仅本人且可读本人档案", `[{"id":1,"owner_admin_id":7}]`, 403, 1, true},
		{"混入他人客户", `[{"id":1,"owner_admin_id":7},{"id":2,"owner_admin_id":8}]`, 403, 1, false},
		{"混入未归属客户", `[{"id":1,"owner_admin_id":7},{"id":3}]`, 403, 1, false},
		{"他人档案可读", `[{"id":1,"owner_admin_id":7}]`, 1, 1, false},
		{"未登录不能证明隔离", `[{"id":1,"owner_admin_id":7}]`, 401, 1, false},
		{"本人档案不可读", `[{"id":1,"owner_admin_id":7}]`, 403, 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/api/v1/esports/admin/customers":
					fmt.Fprintf(w, `{"code":1,"data":%s}`, tc.rows)
				case "/api/v1/esports/admin/customers/1/profile":
					fmt.Fprintf(w, `{"code":%d,"data":{}}`, tc.ownCode)
				default:
					fmt.Fprintf(w, `{"code":%d,"data":{}}`, tc.otherCode)
				}
			}))
			defer server.Close()
			acc := &account{AdminID: 7, cli: newAPIClient(server.URL, time.Second)}
			if passed, detail := checkCustomerVisibility(acc, 1, 2); passed != tc.passed {
				t.Fatalf("数据隔离误判: passed=%v, %s", passed, detail)
			}
		})
	}
}
