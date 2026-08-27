package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"

	"go-admin/internal/ent"
	"go-admin/internal/ent/department"
	"go-admin/internal/global"
)

var (
	ErrDepartmentNotFound      = errors.New("department not found")
	ErrDepartmentDisabled      = errors.New("department is disabled")
	ErrDepartmentCycle         = errors.New("department parent cannot be itself or its child")
	ErrInvalidDepartmentParent = errors.New("department parent id is invalid")
	ErrDepartmentHasChildren   = errors.New("department has children")
)

type DepartmentServiceInterface interface {
	Create(ctx context.Context, op *EffectiveDataScope, req *CreateDepartmentRequest) (*DepartmentResponse, error)
	Update(ctx context.Context, op *EffectiveDataScope, id int, req *UpdateDepartmentRequest) error
	Delete(ctx context.Context, op *EffectiveDataScope, id int) error
	Get(ctx context.Context, op *EffectiveDataScope, id int) (*DepartmentResponse, error)
	Tree(ctx context.Context, req *ListDepartmentsRequest) ([]*DepartmentTreeNode, error)
	TreeInScope(ctx context.Context, op *EffectiveDataScope, req *ListDepartmentsRequest) ([]*DepartmentTreeNode, error)
	ChildIDs(ctx context.Context, id int, includeSelf bool) ([]int, error)
}

type DepartmentService struct {
	client *ent.Client
}

var _ DepartmentServiceInterface = (*DepartmentService)(nil)

func NewDepartmentService() *DepartmentService {
	return &DepartmentService{client: global.ORM}
}

type CreateDepartmentRequest struct {
	Name     string `json:"name" validate:"required,max=100"`
	ParentID int    `json:"parent_id"`
	Sort     int    `json:"sort"`
	Status   string `json:"status"`
	Leader   string `json:"leader"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
}

type UpdateDepartmentRequest struct {
	Name     string  `json:"name" validate:"max=100"`
	ParentID *int    `json:"parent_id"`
	Sort     *int    `json:"sort"`
	Status   string  `json:"status"`
	Leader   *string `json:"leader"`
	Phone    *string `json:"phone"`
	Email    *string `json:"email"`
}

type ListDepartmentsRequest struct {
	Keyword string `query:"keyword" json:"keyword"`
	Status  string `query:"status" json:"status"`
}

type DepartmentResponse struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	ParentID   int    `json:"parent_id"`
	Path       string `json:"path"`
	Sort       int    `json:"sort"`
	Status     string `json:"status"`
	Leader     string `json:"leader"`
	Phone      string `json:"phone"`
	Email      string `json:"email"`
	CreateTime string `json:"created_at"`
	UpdateTime string `json:"updated_at"`
}

type DepartmentTreeNode struct {
	*DepartmentResponse
	Children []*DepartmentTreeNode `json:"children"`
}

func (s *DepartmentService) Create(ctx context.Context, op *EffectiveDataScope, req *CreateDepartmentRequest) (*DepartmentResponse, error) {
	if req.ParentID < 0 {
		return nil, ErrInvalidDepartmentParent
	}
	// 数据范围校验：指定父部门时要求父部门在范围内；
	// 顶级部门（ParentID=0）仅超管/All 范围可建（isDepartmentInScope 对 0 返回 false）
	if err := ensureDepartmentInScope(op, req.ParentID); err != nil {
		return nil, err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("start department transaction failed: %w", err)
	}
	mtx := NewManagedTx(tx)
	defer mtx.RollbackIfNotCommitted()

	parentPath := ""
	if req.ParentID > 0 {
		parent, err := mtx.Tx().Department.Query().
			Where(department.IDEQ(req.ParentID)).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return nil, ErrDepartmentNotFound
			}
			return nil, fmt.Errorf("query parent department failed: %w", err)
		}
		parentPath = parent.Path
	}

	status := department.StatusActive
	if req.Status != "" {
		status = department.Status(req.Status)
	}
	created, err := mtx.Tx().Department.Create().
		SetName(req.Name).
		SetParentID(req.ParentID).
		SetSort(req.Sort).
		SetStatus(status).
		SetLeader(req.Leader).
		SetPhone(req.Phone).
		SetEmail(req.Email).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create department failed: %w", err)
	}

	path := fmt.Sprintf("/%d", created.ID)
	if req.ParentID > 0 {
		path = fmt.Sprintf("%s/%d", parentPath, created.ID)
	}
	created, err = mtx.Tx().Department.UpdateOne(created).
		SetPath(path).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update department path failed: %w", err)
	}

	if err := mtx.Commit(); err != nil {
		return nil, fmt.Errorf("commit department transaction failed: %w", err)
	}

	// 部门树新增节点会扩大 DeptAndChild 角色的子树展开，整体失效数据范围缓存
	InvalidateDataScopeByDeptTree()

	return s.toDepartmentResponse(created), nil
}

func (s *DepartmentService) Update(ctx context.Context, op *EffectiveDataScope, id int, req *UpdateDepartmentRequest) error {
	if err := ensureDepartmentInScope(op, id); err != nil {
		return err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("start department transaction failed: %w", err)
	}
	mtx := NewManagedTx(tx)
	defer mtx.RollbackIfNotCommitted()

	current, err := mtx.Tx().Department.Query().
		Where(department.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrDepartmentNotFound
		}
		return fmt.Errorf("query department failed: %w", err)
	}

	oldPath := current.Path
	newPath := current.Path
	parentChanged := false
	if req.ParentID != nil {
		if *req.ParentID < 0 {
			return ErrInvalidDepartmentParent
		}
		if *req.ParentID == id {
			return ErrDepartmentCycle
		}

		if *req.ParentID > 0 {
			parent, err := mtx.Tx().Department.Query().
				Where(department.IDEQ(*req.ParentID)).
				Only(ctx)
			if err != nil {
				if ent.IsNotFound(err) {
					return ErrDepartmentNotFound
				}
				return fmt.Errorf("query parent department failed: %w", err)
			}
			if parent.Path == oldPath || strings.HasPrefix(parent.Path, oldPath+"/") {
				return ErrDepartmentCycle
			}
			newPath = fmt.Sprintf("%s/%d", parent.Path, id)
		} else {
			newPath = fmt.Sprintf("/%d", id)
		}
		parentChanged = *req.ParentID != current.ParentID
	}
	if parentChanged {
		// ParentID=0 表示移动为根部门；这同样是一个需要授权的目标位置。
		if err := ensureDepartmentInScope(op, *req.ParentID); err != nil {
			return err
		}
		// 移动会重写整个子树的 path，因此子孙部门也必须全部在操作者范围内。
		descendantIDs, err := mtx.Tx().Department.Query().
			Where(department.PathHasPrefix(oldPath + "/")).
			IDs(ctx)
		if err != nil {
			return fmt.Errorf("query descendant departments failed: %w", err)
		}
		for _, descendantID := range descendantIDs {
			if err := ensureDepartmentInScope(op, descendantID); err != nil {
				return err
			}
		}
	}

	update := mtx.Tx().Department.UpdateOne(current)
	if req.Name != "" {
		update.SetName(req.Name)
	}
	if req.ParentID != nil {
		update.SetParentID(*req.ParentID)
		update.SetPath(newPath)
	}
	if req.Sort != nil {
		update.SetSort(*req.Sort)
	}
	if req.Status != "" {
		update.SetStatus(department.Status(req.Status))
	}
	if req.Leader != nil {
		update.SetLeader(*req.Leader)
	}
	if req.Phone != nil {
		update.SetPhone(*req.Phone)
	}
	if req.Email != nil {
		update.SetEmail(*req.Email)
	}

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("update department failed: %w", err)
	}

	if parentChanged {
		if err := s.rewriteDescendantPaths(ctx, mtx.Tx(), oldPath, newPath); err != nil {
			return err
		}
	}

	if err := mtx.Commit(); err != nil {
		return fmt.Errorf("commit department transaction failed: %w", err)
	}

	// 移动子树/改名会改变 DeptAndChild 展开结果，整体失效数据范围缓存
	InvalidateDataScopeByDeptTree()
	return nil
}

func (s *DepartmentService) Delete(ctx context.Context, op *EffectiveDataScope, id int) error {
	if err := ensureDepartmentInScope(op, id); err != nil {
		return err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("start department transaction failed: %w", err)
	}
	mtx := NewManagedTx(tx)
	defer mtx.RollbackIfNotCommitted()

	dept, err := mtx.Tx().Department.Query().
		Where(department.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrDepartmentNotFound
		}
		return fmt.Errorf("query department failed: %w", err)
	}

	hasChildren, err := mtx.Tx().Department.Query().
		Where(department.ParentIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("check department children failed: %w", err)
	}
	if hasChildren {
		return ErrDepartmentHasChildren
	}

	if err := mtx.Tx().Department.DeleteOne(dept).Exec(ctx); err != nil {
		return fmt.Errorf("delete department failed: %w", err)
	}
	if err := mtx.Commit(); err != nil {
		return fmt.Errorf("commit department transaction failed: %w", err)
	}

	// 删除部门会收窄 DeptAndChild 展开结果，整体失效数据范围缓存
	InvalidateDataScopeByDeptTree()
	return nil
}

func (s *DepartmentService) Get(ctx context.Context, op *EffectiveDataScope, id int) (*DepartmentResponse, error) {
	if err := ensureDepartmentInScope(op, id); err != nil {
		return nil, err
	}
	dept, err := s.client.Department.Query().
		Where(department.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrDepartmentNotFound
		}
		return nil, fmt.Errorf("query department failed: %w", err)
	}
	return s.toDepartmentResponse(dept), nil
}

func (s *DepartmentService) Tree(ctx context.Context, req *ListDepartmentsRequest) ([]*DepartmentTreeNode, error) {
	return s.tree(ctx, req, nil, false)
}

func (s *DepartmentService) TreeInScope(ctx context.Context, op *EffectiveDataScope, req *ListDepartmentsRequest) ([]*DepartmentTreeNode, error) {
	allowedIDs, scoped := departmentIDsForScope(op)
	return s.tree(ctx, req, allowedIDs, scoped)
}

func departmentIDsForScope(op *EffectiveDataScope) ([]int, bool) {
	if op == nil || op.IsSuperAdmin || op.Scope == DataScopeAll {
		return nil, false
	}
	switch op.Scope {
	case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
		return op.DepartmentIDs, true
	case DataScopeSelf:
		return nil, true
	default:
		return nil, true
	}
}

func (s *DepartmentService) tree(ctx context.Context, req *ListDepartmentsRequest, allowedIDs []int, scoped bool) ([]*DepartmentTreeNode, error) {
	if scoped {
		allowedIDs = uniquePositiveInts(allowedIDs)
		if len(allowedIDs) == 0 {
			return []*DepartmentTreeNode{}, nil
		}
	}

	query := s.client.Department.Query()
	if req != nil {
		if req.Keyword != "" {
			query = query.Where(department.NameContains(req.Keyword))
		}
		if req.Status != "" {
			query = query.Where(department.StatusEQ(department.Status(req.Status)))
		}
	}
	if scoped {
		query = query.Where(department.IDIn(allowedIDs...))
	}

	departments, err := query.
		Order(ent.Asc(department.FieldSort), ent.Asc(department.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query department tree failed: %w", err)
	}

	nodeByID := make(map[int]*DepartmentTreeNode, len(departments))
	for _, dept := range departments {
		nodeByID[dept.ID] = &DepartmentTreeNode{
			DepartmentResponse: s.toDepartmentResponse(dept),
			Children:           make([]*DepartmentTreeNode, 0),
		}
	}

	roots := make([]*DepartmentTreeNode, 0)
	for _, dept := range departments {
		node := nodeByID[dept.ID]
		if dept.ParentID == 0 {
			roots = append(roots, node)
			continue
		}
		parent, ok := nodeByID[dept.ParentID]
		if !ok {
			roots = append(roots, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}

	return roots, nil
}

func uniquePositiveInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// ChildIDs 返回部门自身（可选）及全部子孙部门 ID，基于物化路径前缀一次查询、无递归。
// 有意不过滤 status：禁用部门仍计入数据范围，避免挂在其下的管理员突然从上级管理范围消失；
// 如业务要求"禁用=不可见"，需在查询处加 StatusActive 过滤并同步收窄相关管理入口
func (s *DepartmentService) ChildIDs(ctx context.Context, id int, includeSelf bool) ([]int, error) {
	dept, err := s.client.Department.Query().
		Where(department.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrDepartmentNotFound
		}
		return nil, fmt.Errorf("query department failed: %w", err)
	}

	descendants, err := s.client.Department.Query().
		Where(department.PathHasPrefix(dept.Path+"/")).
		Order(ent.Asc(department.FieldSort), ent.Asc(department.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query child departments failed: %w", err)
	}

	ids := make([]int, 0, len(descendants)+1)
	if includeSelf {
		ids = append(ids, dept.ID)
	}
	for _, child := range descendants {
		ids = append(ids, child.ID)
	}
	return ids, nil
}

func (s *DepartmentService) rewriteDescendantPaths(ctx context.Context, tx *ent.Tx, oldPath, newPath string) error {
	descendants, err := tx.Department.Query().
		Where(department.PathHasPrefix(oldPath + "/")).
		All(ctx)
	if err != nil {
		return fmt.Errorf("query descendant departments failed: %w", err)
	}

	for _, descendant := range descendants {
		updatedPath := newPath + strings.TrimPrefix(descendant.Path, oldPath)
		if err := tx.Department.UpdateOne(descendant).
			SetPath(updatedPath).
			Exec(ctx); err != nil {
			return fmt.Errorf("update descendant department path failed: %w", err)
		}
	}
	return nil
}

func (s *DepartmentService) toDepartmentResponse(dept *ent.Department) *DepartmentResponse {
	return &DepartmentResponse{
		ID:         dept.ID,
		Name:       dept.Name,
		ParentID:   dept.ParentID,
		Path:       dept.Path,
		Sort:       dept.Sort,
		Status:     string(dept.Status),
		Leader:     dept.Leader,
		Phone:      dept.Phone,
		Email:      dept.Email,
		CreateTime: dept.CreateTime.Format("2006-01-02 15:04:05"),
		UpdateTime: dept.UpdateTime.Format("2006-01-02 15:04:05"),
	}
}

// ManagedTx 包装 ent.Tx，跟踪事务提交状态，确保仅在未提交时执行回滚
type ManagedTx struct {
	tx        *ent.Tx
	committed bool
}

// NewManagedTx 创建托管事务
func NewManagedTx(tx *ent.Tx) *ManagedTx {
	return &ManagedTx{tx: tx}
}

// Commit 提交事务并标记为已提交
func (m *ManagedTx) Commit() error {
	if m.committed {
		return fmt.Errorf("transaction already committed")
	}
	if err := m.tx.Commit(); err != nil {
		return err
	}
	m.committed = true
	return nil
}

// RollbackIfNotCommitted 仅在事务未提交时执行回滚，并记录回滚错误（用于 defer）
func (m *ManagedTx) RollbackIfNotCommitted() {
	if m.committed {
		return
	}
	if err := m.tx.Rollback(); err != nil {
		log.Error("事务回滚失败", zap.Error(err))
	}
}

// Tx 返回底层事务对象，供业务代码使用
func (m *ManagedTx) Tx() *ent.Tx {
	return m.tx
}
