package service

import (
	"context"
	"fmt"
	"go-admin/internal/ent"
)

// ========== 查（List/Get）公共结构体 ==========

// PaginationRequest 分页请求公共结构体
type PaginationRequest struct {
	Page     int `query:"page" json:"page" validate:"min=0"`
	PageSize int `query:"page_size" json:"page_size" validate:"min=0,max=100"`
}

// ListRequest 带关键词的列表请求公共结构体
type ListRequest struct {
	PaginationRequest
	Keyword string `query:"keyword" json:"keyword"`
}

// ListResponse 列表响应公共结构体
type ListResponse[T any] struct {
	Total int `json:"total"`
	Page  int `json:"page"`
	Data  []T `json:"data"`
}

// ========== 删（Delete）公共结构体 ==========

// BatchDeleteRequest 批量删除请求公共结构体
type BatchDeleteRequest struct {
	IDs []int `json:"ids" validate:"required,min=1"`
}

// ========== 改（Update）公共操作结构体 ==========

// StatusRequest 通用状态更新请求结构体
type StatusRequest struct {
	Status int `json:"status" validate:"required"`
}

// EnableRequest 通用启用/禁用请求结构体
type EnableRequest struct {
	Enabled bool `json:"enabled" validate:"required"`
}

// ========== 可组合的 CRUD 接口 ==========

// Creator[C, R] 增 — 创建能力接口
// C: 创建请求类型
// R: 响应类型
type Creator[C, R any] interface {
	Create(ctx context.Context, req *C) (*R, error)
}

// Reader[R] 查 — 查询单个能力接口
// R: 响应类型
type Reader[R any] interface {
	Get(ctx context.Context, id int) (*R, error)
}

// Lister[L, R] 查 — 列表查询能力接口
// L: 列表请求类型
// R: 响应类型
type Lister[L, R any] interface {
	List(ctx context.Context, req *L) (*ListResponse[*R], error)
}

// Readable[L, R] 查 — 完整查询能力 = Get + List
type Readable[L, R any] interface {
	Reader[R]
	Lister[L, R]
}

// Updater[U] 改 — 更新能力接口
// U: 更新请求类型
type Updater[U any] interface {
	Update(ctx context.Context, id int, req *U) error
}

// Deleter 删 — 删除能力接口
type Deleter interface {
	Delete(ctx context.Context, id int) error
}

// BatchDeleter 删 — 批量删除能力接口
type BatchDeleter interface {
	BatchDelete(ctx context.Context, ids []int) error
}

// Deletable 删 — 完整删除能力 = Delete + BatchDelete
type Deletable interface {
	Deleter
	BatchDeleter
}

// CRUD[C, U, L, R] 标准增删改查组合接口 = Create + Get + List + Update + Delete
type CRUD[C, U, L, R any] interface {
	Creator[C, R]
	Reader[R]
	Lister[L, R]
	Updater[U]
	Deleter
}

// ========== CRUDBase：可组合的增删改查方法实现 ==========

// CRUDBase 提供通用的增删改查辅助方法
// 各 Service 通过嵌入 *CRUDBase 来复用通用逻辑
type CRUDBase struct {
	Client *ent.Client
}

// GetByID 通用按ID查询单个实体
// E: Ent 实体类型
// R: 响应类型
// queryFn: 查询函数，返回 (实体, error)
// mapper: 实体转响应的转换函数
// notFoundErr: 实体不存在时返回的错误
// label: 实体中文名（用于错误消息）
func GetByID[E, R any](
	ctx context.Context,
	queryFn func(ctx context.Context) (E, error),
	mapper func(E) *R,
	notFoundErr error,
	label string,
) (*R, error) {
	entity, err := queryFn(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, notFoundErr
		}
		return nil, fmt.Errorf("获取%s失败: %w", label, err)
	}
	return mapper(entity), nil
}

// DeleteByID 通用按ID删除实体
// E: Ent 实体类型
// findFn: 查找函数，返回 (实体, error)
// deleteFn: 删除函数，接收实体，返回 error
// notFoundErr: 实体不存在时返回的错误
// label: 实体中文名（用于错误消息）
func DeleteByID[E any](
	ctx context.Context,
	findFn func(ctx context.Context) (E, error),
	deleteFn func(E) error,
	notFoundErr error,
	label string,
) error {
	entity, err := findFn(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return notFoundErr
		}
		return fmt.Errorf("查询%s失败: %w", label, err)
	}
	if err := deleteFn(entity); err != nil {
		return fmt.Errorf("删除%s失败: %w", label, err)
	}
	return nil
}

// BuildList 将实体列表映射为分页 ListResponse
// E: Ent 实体类型
// R: 响应类型
func BuildList[E, R any](
	entities []E,
	total int,
	page int,
	mapper func(E) *R,
) *ListResponse[*R] {
	data := make([]*R, 0, len(entities))
	for _, e := range entities {
		data = append(data, mapper(e))
	}
	return &ListResponse[*R]{Total: total, Page: page, Data: data}
}

