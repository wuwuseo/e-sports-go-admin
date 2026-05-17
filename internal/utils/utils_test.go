package utils

import (
	"testing"
)

func TestNormalizePagination_Defaults(t *testing.T) {
	page, pageSize := NormalizePagination(0, 0)

	if page != 1 {
		t.Errorf("期望 page=1, 实际 page=%d", page)
	}

	if pageSize != 0 {
		t.Errorf("期望 pageSize=0 (不分页保持不变), 实际 pageSize=%d", pageSize)
	}
}

func TestNormalizePagination_NegativePage(t *testing.T) {
	page, pageSize := NormalizePagination(-5, 10)

	if page != 1 {
		t.Errorf("期望 page=1, 实际 page=%d", page)
	}

	if pageSize != 10 {
		t.Errorf("期望 pageSize=10, 实际 pageSize=%d", pageSize)
	}
}

func TestNormalizePagination_ZeroPage(t *testing.T) {
	page, pageSize := NormalizePagination(0, 20)

	if page != 1 {
		t.Errorf("期望 page=1, 实际 page=%d", page)
	}

	if pageSize != 20 {
		t.Errorf("期望 pageSize=20, 实际 pageSize=%d", pageSize)
	}
}

func TestNormalizePagination_NegativePageSize(t *testing.T) {
	page, pageSize := NormalizePagination(2, -1)

	if page != 2 {
		t.Errorf("期望 page=2, 实际 page=%d", page)
	}

	if pageSize != 10 {
		t.Errorf("期望 pageSize=10 (负值使用默认值), 实际 pageSize=%d", pageSize)
	}
}

func TestNormalizePagination_ZeroPageSizeMeansNoPagination(t *testing.T) {
	page, pageSize := NormalizePagination(1, 0)

	if page != 1 {
		t.Errorf("期望 page=1, 实际 page=%d", page)
	}

	if pageSize != 0 {
		t.Errorf("期望 pageSize=0 (不分页), 实际 pageSize=%d", pageSize)
	}
}

func TestNormalizePagination_PageSizeExceedsMax(t *testing.T) {
	page, pageSize := NormalizePagination(1, 200)

	if page != 1 {
		t.Errorf("期望 page=1, 实际 page=%d", page)
	}

	if pageSize != 100 {
		t.Errorf("期望 pageSize=100 (超过限制截断), 实际 pageSize=%d", pageSize)
	}
}

func TestNormalizePagination_PageSizeAtMax(t *testing.T) {
	page, pageSize := NormalizePagination(3, 100)

	if page != 3 {
		t.Errorf("期望 page=3, 实际 page=%d", page)
	}

	if pageSize != 100 {
		t.Errorf("期望 pageSize=100, 实际 pageSize=%d", pageSize)
	}
}

func TestNormalizePagination_ValidInput(t *testing.T) {
	page, pageSize := NormalizePagination(2, 50)

	if page != 2 {
		t.Errorf("期望 page=2, 实际 page=%d", page)
	}

	if pageSize != 50 {
		t.Errorf("期望 pageSize=50, 实际 pageSize=%d", pageSize)
	}
}

func TestBuildCacheKey_NoParts(t *testing.T) {
	key := BuildCacheKey("prefix")

	if key != "prefix" {
		t.Errorf("期望 key=prefix, 实际 key=%s", key)
	}
}

func TestBuildCacheKey_SinglePart(t *testing.T) {
	key := BuildCacheKey("user", "menu")

	if key != "user:menu" {
		t.Errorf("期望 key=user:menu, 实际 key=%s", key)
	}
}

func TestBuildCacheKey_MultipleParts(t *testing.T) {
	key := BuildCacheKey("user", "menu", "uid123", "default")

	if key != "user:menu:uid123:default" {
		t.Errorf("期望 key=user:menu:uid123:default, 实际 key=%s", key)
	}
}

func TestBuildCacheKey_EmptyStringPart(t *testing.T) {
	key := BuildCacheKey("role", "perms", "", "default")

	if key != "role:perms::default" {
		t.Errorf("期望 key=role:perms::default, 实际 key=%s", key)
	}
}

func TestGetResponseMsg_KnownCode(t *testing.T) {
	msg := GetResponseMsg(1)

	if msg != "成功" {
		t.Errorf("期望 msg=成功, 实际 msg=%s", msg)
	}
}

func TestGetResponseMsg_ErrorCode(t *testing.T) {
	msg := GetResponseMsg(0)

	if msg != "失败" {
		t.Errorf("期望 msg=失败, 实际 msg=%s", msg)
	}
}

func TestGetResponseMsg_Unauthorized(t *testing.T) {
	msg := GetResponseMsg(401)

	if msg != "未授权" {
		t.Errorf("期望 msg=未授权, 实际 msg=%s", msg)
	}
}

func TestGetResponseMsg_UnknownCode(t *testing.T) {
	msg := GetResponseMsg(99999)

	if msg != "" {
		t.Errorf("期望 msg 为空字符串, 实际 msg=%s", msg)
	}
}

func TestNormalizePagination_AllEdges(t *testing.T) {
	tests := []struct {
		name             string
		page, pageSize   int
		expectedPage     int
		expectedPageSize int
	}{
		{"负数page应重置为1", -100, 10, 1, 10},
		{"page=0应重置为1", 0, 50, 1, 50},
		{"pageSize=0不分页", 3, 0, 3, 0},
		{"pageSize负数使用默认值10", 1, -5, 1, 10},
		{"pageSize超过100截断", 1, 500, 1, 100},
		{"pageSize=100允许通过", 5, 100, 5, 100},
		{"正常输入", 2, 30, 2, 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, pageSize := NormalizePagination(tt.page, tt.pageSize)
			if page != tt.expectedPage || pageSize != tt.expectedPageSize {
				t.Errorf("NormalizePagination(%d, %d) = (%d, %d), 期望 (%d, %d)",
					tt.page, tt.pageSize, page, pageSize, tt.expectedPage, tt.expectedPageSize)
			}
		})
	}
}
