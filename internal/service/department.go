package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/department"
	"go-admin/internal/global"
)

var (
	ErrDepartmentNotFound = errors.New("department not found")
	ErrDepartmentDisabled = errors.New("department is disabled")
	ErrDepartmentCycle    = errors.New("department parent cannot be itself or its child")
)

type DepartmentServiceInterface interface {
	Create(ctx context.Context, req *CreateDepartmentRequest) (*DepartmentResponse, error)
	Update(ctx context.Context, id int, req *UpdateDepartmentRequest) error
	Delete(ctx context.Context, id int) error
	Get(ctx context.Context, id int) (*DepartmentResponse, error)
	Tree(ctx context.Context, req *ListDepartmentsRequest) ([]*DepartmentTreeNode, error)
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
	Name     string `json:"name" validate:"max=100"`
	ParentID *int   `json:"parent_id"`
	Sort     *int   `json:"sort"`
	Status   string `json:"status"`
	Leader   string `json:"leader"`
	Phone    string `json:"phone"`
	Email    string `json:"email"`
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

func (s *DepartmentService) Create(ctx context.Context, req *CreateDepartmentRequest) (*DepartmentResponse, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("start department transaction failed: %w", err)
	}
	defer rollbackUnlessCommitted(tx)

	parentPath := ""
	if req.ParentID > 0 {
		parent, err := tx.Department.Query().
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
	created, err := tx.Department.Create().
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
	created, err = tx.Department.UpdateOne(created).
		SetPath(path).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("update department path failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit department transaction failed: %w", err)
	}

	return s.toDepartmentResponse(created), nil
}

func (s *DepartmentService) Update(ctx context.Context, id int, req *UpdateDepartmentRequest) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("start department transaction failed: %w", err)
	}
	defer rollbackUnlessCommitted(tx)

	current, err := tx.Department.Query().
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
		if *req.ParentID == id {
			return ErrDepartmentCycle
		}

		if *req.ParentID > 0 {
			parent, err := tx.Department.Query().
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

	update := tx.Department.UpdateOne(current).
		SetLeader(req.Leader).
		SetPhone(req.Phone).
		SetEmail(req.Email)
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

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("update department failed: %w", err)
	}

	if parentChanged {
		if err := s.rewriteDescendantPaths(ctx, tx, oldPath, newPath); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit department transaction failed: %w", err)
	}
	return nil
}

func (s *DepartmentService) Delete(ctx context.Context, id int) error {
	dept, err := s.client.Department.Query().
		Where(department.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrDepartmentNotFound
		}
		return fmt.Errorf("query department failed: %w", err)
	}

	hasChildren, err := s.client.Department.Query().
		Where(department.ParentIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("check department children failed: %w", err)
	}
	if hasChildren {
		return errors.New("department has children")
	}

	if err := s.client.Department.DeleteOne(dept).Exec(ctx); err != nil {
		return fmt.Errorf("delete department failed: %w", err)
	}
	return nil
}

func (s *DepartmentService) Get(ctx context.Context, id int) (*DepartmentResponse, error) {
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
	query := s.client.Department.Query()
	if req != nil {
		if req.Keyword != "" {
			query = query.Where(department.NameContains(req.Keyword))
		}
		if req.Status != "" {
			query = query.Where(department.StatusEQ(department.Status(req.Status)))
		}
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

func rollbackUnlessCommitted(tx *ent.Tx) {
	_ = tx.Rollback()
}
