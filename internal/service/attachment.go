package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/attachment"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/google/uuid"
	"github.com/wuwuseo/cmf/filesystem"
)

var (
	ErrAttachmentNotFound     = errors.New("附件不存在")
	ErrAttachmentUploadFailed = errors.New("附件上传失败")
	ErrAttachmentDeleteFailed = errors.New("附件删除失败")
)

// AttachmentServiceInterface 附件服务接口
type AttachmentServiceInterface interface {
	Upload(ctx context.Context, fileHeader *multipart.FileHeader, groupID *int, userID string) (*AttachmentResponse, error)
	Delete(ctx context.Context, id int) error
	BatchDelete(ctx context.Context, ids []int) error
	Get(ctx context.Context, id int) (*AttachmentResponse, error)
	List(ctx context.Context, req *ListAttachmentsRequest) (*ListAttachmentsResponse, error)
	GetFileData(ctx context.Context, id int) ([]byte, string, string, error)
	MoveToGroup(ctx context.Context, id int, groupID *int) error
}

// AttachmentService 附件服务
type AttachmentService struct {
	client     *ent.Client
	filesystem *filesystem.Filesystem
}

// NewAttachmentService 创建附件服务实例
func NewAttachmentService(fs *filesystem.Filesystem) *AttachmentService {
	return &AttachmentService{
		client:     global.ORM,
		filesystem: fs,
	}
}

// AttachmentResponse 附件响应
type AttachmentResponse struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Path         string `json:"path"`
	MimeType     string `json:"mime_type"`
	Extension    string `json:"extension"`
	Size         int64  `json:"size"`
	GroupID      int    `json:"group_id,omitempty"`
	UploadedBy   string `json:"uploaded_by,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// ListAttachmentsRequest 附件列表请求
type ListAttachmentsRequest struct {
	Page     int    `query:"page"`
	PageSize int    `query:"page_size"`
	Keyword  string `query:"keyword"`
	GroupID  *int   `query:"group_id"`
	MimeType string `query:"mime_type"`
}

// ListAttachmentsResponse 附件列表响应
type ListAttachmentsResponse struct {
	Total int                   `json:"total"`
	Page  int                   `json:"page"`
	Data  []*AttachmentResponse `json:"data"`
}

// BatchDeleteRequest 批量删除请求
type BatchDeleteRequest struct {
	IDs []int `json:"ids" validate:"required,min=1"`
}

// MoveAttachmentRequest 移动附件请求
type MoveAttachmentRequest struct {
	GroupID *int `json:"group_id"`
}

// Upload 上传附件
func (s *AttachmentService) Upload(ctx context.Context, fileHeader *multipart.FileHeader, groupID *int, userID string) (*AttachmentResponse, error) {
	// 打开上传文件
	file, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer file.Close()

	// 读取文件数据
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("读取文件数据失败: %w", err)
	}

	// 获取文件信息
	originalName := fileHeader.Filename
	ext := strings.ToLower(filepath.Ext(originalName))
	if ext != "" {
		ext = ext[1:] // 去掉前导点
	}
	mimeType := fileHeader.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	size := fileHeader.Size

	// 生成存储 key
	now := time.Now()
	key := fmt.Sprintf("attachments/%d/%02d/%s.%s", now.Year(), now.Month(), uuid.New().String(), ext)

	// 存储文件
	if err := s.filesystem.Set(key, data, 0); err != nil {
		return nil, fmt.Errorf("存储文件失败: %w", err)
	}

	// 创建数据库记录
	builder := s.client.Attachment.Create().
		SetName(originalName).
		SetOriginalName(originalName).
		SetPath(key).
		SetMimeType(mimeType).
		SetExtension(ext).
		SetSize(size).
		SetNillableGroupID(groupID).
		SetUploadedBy(userID)

	a, err := builder.Save(ctx)
	if err != nil {
		// 如果数据库保存失败，尝试清理已存储的文件
		_ = s.filesystem.Delete(key)
		return nil, fmt.Errorf("保存附件记录失败: %w", err)
	}

	return s.toAttachmentResponse(a), nil
}

// Delete 删除附件
func (s *AttachmentService) Delete(ctx context.Context, id int) error {
	a, err := s.client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAttachmentNotFound
		}
		return fmt.Errorf("查询附件失败: %w", err)
	}

	// 删除存储文件
	_ = s.filesystem.Delete(a.Path)

	// 删除数据库记录
	err = s.client.Attachment.DeleteOneID(id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除附件记录失败: %w", err)
	}

	return nil
}

// BatchDelete 批量删除附件
func (s *AttachmentService) BatchDelete(ctx context.Context, ids []int) error {
	// 查询所有附件获取路径
	attachments, err := s.client.Attachment.Query().
		Where(attachment.IDIn(ids...)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("查询附件失败: %w", err)
	}

	// 删除存储文件
	for _, a := range attachments {
		_ = s.filesystem.Delete(a.Path)
	}

	// 批量删除数据库记录
	_, err = s.client.Attachment.Delete().
		Where(attachment.IDIn(ids...)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("批量删除附件记录失败: %w", err)
	}

	return nil
}

// Get 获取附件详情
func (s *AttachmentService) Get(ctx context.Context, id int) (*AttachmentResponse, error) {
	a, err := s.client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAttachmentNotFound
		}
		return nil, fmt.Errorf("查询附件失败: %w", err)
	}

	return s.toAttachmentResponse(a), nil
}

// List 获取附件列表
func (s *AttachmentService) List(ctx context.Context, req *ListAttachmentsRequest) (*ListAttachmentsResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.client.Attachment.Query()

	// 关键字搜索
	if req.Keyword != "" {
		query = query.Where(
			attachment.Or(
				attachment.NameContains(req.Keyword),
				attachment.OriginalNameContains(req.Keyword),
			),
		)
	}

	// 分组筛选
	if req.GroupID != nil {
		query = query.Where(attachment.GroupIDEQ(*req.GroupID))
	}

	// MIME 类型筛选
	if req.MimeType != "" {
		query = query.Where(attachment.MimeTypeHasPrefix(req.MimeType))
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取附件总数失败: %w", err)
	}

	offset := (req.Page - 1) * req.PageSize
	attachments, err := query.
		Offset(offset).
		Limit(req.PageSize).
		Order(ent.Desc(attachment.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取附件列表失败: %w", err)
	}

	data := make([]*AttachmentResponse, 0, len(attachments))
	for _, a := range attachments {
		data = append(data, s.toAttachmentResponse(a))
	}

	return &ListAttachmentsResponse{
		Total: total,
		Page:  req.Page,
		Data:  data,
	}, nil
}

// GetFileData 获取文件数据（用于预览/下载）
func (s *AttachmentService) GetFileData(ctx context.Context, id int) ([]byte, string, string, error) {
	a, err := s.client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, "", "", ErrAttachmentNotFound
		}
		return nil, "", "", fmt.Errorf("查询附件失败: %w", err)
	}

	data, err := s.filesystem.Get(a.Path)
	if err != nil {
		return nil, "", "", fmt.Errorf("获取文件数据失败: %w", err)
	}

	return data, a.MimeType, a.OriginalName, nil
}

// MoveToGroup 移动附件到分组
func (s *AttachmentService) MoveToGroup(ctx context.Context, id int, groupID *int) error {
	a, err := s.client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAttachmentNotFound
		}
		return fmt.Errorf("查询附件失败: %w", err)
	}

	update := s.client.Attachment.UpdateOne(a)
	if groupID != nil {
		update.SetGroupID(*groupID)
	} else {
		update.ClearGroupID()
	}

	_, err = update.Save(ctx)
	if err != nil {
		return fmt.Errorf("移动附件失败: %w", err)
	}

	return nil
}

// toAttachmentResponse 将实体转换为响应格式
func (s *AttachmentService) toAttachmentResponse(a *ent.Attachment) *AttachmentResponse {
	return &AttachmentResponse{
		ID:           a.ID,
		Name:         a.Name,
		OriginalName: a.OriginalName,
		Path:         a.Path,
		MimeType:     a.MimeType,
		Extension:    a.Extension,
		Size:         a.Size,
		GroupID:      a.GroupID,
		UploadedBy:   a.UploadedBy,
		CreatedAt:    a.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:    a.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}
