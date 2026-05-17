package utils

import (
	"testing"
)

func TestNewResponse_Defaults(t *testing.T) {
	r := NewResponse()

	if r.Code != 0 {
		t.Errorf("期望 Code=0, 实际 Code=%d", r.Code)
	}

	if r.Message != "" {
		t.Errorf("期望 Message为空, 实际 Message=%s", r.Message)
	}

	if r.Data != nil {
		t.Errorf("期望 Data=nil, 实际 Data=%v", r.Data)
	}
}

func TestResponse_Success_NoOptions(t *testing.T) {
	r := NewResponse()
	r.Success()

	if r.Code != 1 {
		t.Errorf("期望 Code=1 (ResponseSuccessCode), 实际 Code=%d", r.Code)
	}

	if r.Message != "成功" {
		t.Errorf("期望 Message=成功, 实际 Message=%s", r.Message)
	}

	if r.Data != nil {
		t.Errorf("期望 Data=nil, 实际 Data=%v", r.Data)
	}
}

func TestResponse_Success_WithDataOnly(t *testing.T) {
	r := NewResponse()
	data := map[string]string{"key": "value"}
	r.Success(data)

	if r.Code != 1 {
		t.Errorf("期望 Code=1, 实际 Code=%d", r.Code)
	}

	if r.Message != "成功" {
		t.Errorf("期望 Message=成功, 实际 Message=%s", r.Message)
	}

	if r.Data == nil {
		t.Fatal("期望 Data 不为 nil")
	}
}

func TestResponse_Success_WithDataAndCode(t *testing.T) {
	r := NewResponse()
	data := "hello"
	r.Success(data, 2)

	if r.Code != 2 {
		t.Errorf("期望 Code=2 (ResponseSuccessActionCode), 实际 Code=%d", r.Code)
	}

	if r.Message != "成功" {
		t.Errorf("期望 Message=成功 (Message 在 Code 变更前已设置), 实际 Message=%s", r.Message)
	}
}

func TestResponse_Success_WithAllOptions(t *testing.T) {
	r := NewResponse()
	data := map[string]int{"count": 42}
	r.Success(data, 200, "自定义消息")

	if r.Code != 200 {
		t.Errorf("期望 Code=200, 实际 Code=%d", r.Code)
	}

	if r.Message != "自定义消息" {
		t.Errorf("期望 Message=自定义消息, 实际 Message=%s", r.Message)
	}
}

func TestResponse_Success_Chaining(t *testing.T) {
	r := NewResponse().Success("result")

	if r.Code != 1 {
		t.Errorf("期望 Code=1, 实际 Code=%d", r.Code)
	}

	if r.Message != "成功" {
		t.Errorf("期望 Message=成功, 实际 Message=%s", r.Message)
	}
}

func TestResponse_Fail_NoOptions(t *testing.T) {
	r := NewResponse()
	r.Fail()

	if r.Code != 0 {
		t.Errorf("期望 Code=0 (ResponseErrorCode), 实际 Code=%d", r.Code)
	}

	if r.Message != "失败" {
		t.Errorf("期望 Message=失败, 实际 Message=%s", r.Message)
	}
}

func TestResponse_Fail_WithCodeOnly(t *testing.T) {
	r := NewResponse()
	r.Fail(401)

	if r.Code != 401 {
		t.Errorf("期望 Code=401, 实际 Code=%d", r.Code)
	}

	if r.Message != "失败" {
		t.Errorf("期望 Message=失败 (code=0时的默认消息), 实际 Message=%s", r.Message)
	}
}

func TestResponse_Fail_WithCodeAndMessage(t *testing.T) {
	r := NewResponse()
	r.Fail(500, "服务器错误")

	if r.Code != 500 {
		t.Errorf("期望 Code=500, 实际 Code=%d", r.Code)
	}

	if r.Message != "服务器错误" {
		t.Errorf("期望 Message=服务器错误, 实际 Message=%s", r.Message)
	}
}

func TestResponse_Fail_Chaining(t *testing.T) {
	r := NewResponse().Fail(403, "禁止访问")

	if r.Code != 403 {
		t.Errorf("期望 Code=403, 实际 Code=%d", r.Code)
	}

	if r.Message != "禁止访问" {
		t.Errorf("期望 Message=禁止访问, 实际 Message=%s", r.Message)
	}
}

func TestResponse_SuccessThenFail(t *testing.T) {
	r := NewResponse()
	r.Success("ok")
	r.Fail(500, "错误")

	if r.Code != 500 {
		t.Errorf("期望 Code=500, 实际 Code=%d", r.Code)
	}

	if r.Message != "错误" {
		t.Errorf("期望 Message=错误, 实际 Message=%s", r.Message)
	}

	if r.Data != "ok" {
		t.Errorf("期望 Data 保持为 ok, 实际 Data=%v", r.Data)
	}
}
