package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/attachment"
	"go-admin/internal/ent/esportsagecheck"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/filesystem"
)

var (
	ErrAttachmentNotFound           = errors.New("附件不存在")
	ErrAttachmentUploadFailed       = errors.New("附件上传失败")
	ErrAttachmentDeleteFailed       = errors.New("附件删除失败")
	ErrAttachmentStorageUnavailable = errors.New("附件存储未初始化")
)

const AttachmentAccessTTL = 10 * time.Minute

type AttachmentServiceInterface interface {
	Upload(ctx context.Context, fileHeader *multipart.FileHeader, groupID *int, userID string, directory ...string) (*AttachmentResponse, error)
	Delete(ctx context.Context, id int) error
	BatchDelete(ctx context.Context, ids []int) error
	Get(ctx context.Context, id int) (*AttachmentResponse, error)
	List(ctx context.Context, req *ListAttachmentsRequest) (*ListAttachmentsResponse, error)
	GetFileData(ctx context.Context, id int) ([]byte, string, string, error)
	MoveToGroup(ctx context.Context, id int, groupID *int) error
}

type AttachmentService struct {
	*CRUDBase
	filesystem *filesystem.Filesystem
}

var (
	_ Reader[AttachmentResponse]                         = (*AttachmentService)(nil)
	_ Lister[ListAttachmentsRequest, AttachmentResponse] = (*AttachmentService)(nil)
	_ Deletable                                          = (*AttachmentService)(nil)
)

func NewAttachmentService(fs *filesystem.Filesystem) *AttachmentService {
	return &AttachmentService{
		CRUDBase:   &CRUDBase{Client: global.ORM},
		filesystem: fs,
	}
}

type AttachmentResponse struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Path         string `json:"path"`
	Disk         string `json:"disk,omitempty"`
	Driver       string `json:"driver,omitempty"`
	Directory    string `json:"directory,omitempty"`
	ContentHash  string `json:"content_hash,omitempty"`
	MimeType     string `json:"mime_type"`
	Extension    string `json:"extension"`
	Size         int64  `json:"size"`
	GroupID      int    `json:"group_id,omitempty"`
	UploadedBy   string `json:"uploaded_by,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type AttachmentAccessURL struct {
	URL       string `json:"url"`
	ExpiresIn int64  `json:"expires_in"`
	Mode      string `json:"mode"`
	Public    bool   `json:"public"`
	Disk      string `json:"disk"`
	Driver    string `json:"driver"`
}

type AttachmentXAccelRedirect struct {
	Path     string
	MimeType string
	Filename string
}

type ListAttachmentsRequest struct {
	ListRequest
	GroupID  *int   `query:"group_id"`
	MimeType string `query:"mime_type"`
}

type ListAttachmentsResponse = ListResponse[*AttachmentResponse]

type MoveAttachmentRequest struct {
	GroupID *int `json:"group_id"`
}

// isBlockedUploadExtension / isBlockedUploadMime 判定是否为被禁止上传的
// 高风险文件类型。SVG 可内嵌脚本，回显端一旦以 inline 方式输出即构成
// 存储型 XSS，因此按扩展名与声明 MIME 双重拦截。
func isBlockedUploadExtension(ext string) bool {
	return strings.EqualFold(strings.TrimSpace(ext), "svg")
}

func isBlockedUploadMime(mimeType string) bool {
	return strings.Contains(strings.ToLower(mimeType), "svg")
}

// rejectBlockedUpload 统一拦截入口：任一命中即返回 ErrSVGUploadBlocked。
// 仅依赖 MIME 白名单不够——SVG 内容经 http.DetectContentType 判定为
// text/xml（在白名单内），而落库 MIME 取客户端声明，可绕过白名单。
func rejectBlockedUpload(originalName, declaredMime string) error {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(originalName), "."))
	if isBlockedUploadExtension(ext) || isBlockedUploadMime(declaredMime) {
		return ErrSVGUploadBlocked
	}
	return nil
}

func (s *AttachmentService) Upload(ctx context.Context, fileHeader *multipart.FileHeader, groupID *int, userID string, directory ...string) (*AttachmentResponse, error) {
	if s.filesystem == nil {
		return nil, ErrAttachmentStorageUnavailable
	}
	if fileHeader == nil {
		return nil, fmt.Errorf("上传文件不能为空")
	}

	originalName, err := filesystem.SafeOriginalName(fileHeader.Filename)
	if err != nil {
		return nil, err
	}
	if err := rejectBlockedUpload(originalName, fileHeader.Header.Get("Content-Type")); err != nil {
		return nil, err
	}
	uploadDirectory, err := resolveAttachmentDirectory(firstString(directory))
	if err != nil {
		return nil, err
	}

	file, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("打开上传文件失败: %w", err)
	}
	contentHash, size, err := filesystem.ContentHashReader(file)
	closeErr := file.Close()
	if err != nil {
		return nil, fmt.Errorf("计算文件哈希失败: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("关闭上传文件失败: %w", closeErr)
	}

	originalNameHash := filesystem.HashString(originalName)
	if duplicate, err := s.findDuplicateAttachment(ctx, contentHash, size, originalNameHash); err == nil {
		return s.toAttachmentResponse(duplicate), nil
	} else if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("查询重复附件失败: %w", err)
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(originalName), "."))
	key, err := filesystem.BuildContentAddressedKey(uploadDirectory, contentHash, originalNameHash, ext)
	if err != nil {
		return nil, err
	}

	file, err = fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer file.Close()

	mimeType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	var body io.Reader = file
	// 客户端未声明类型时按内容嗅探，避免 S3 对象一律落成 octet-stream
	if mimeType == "" || mimeType == "application/octet-stream" {
		buffered := bufio.NewReader(file)
		if head, _ := buffered.Peek(512); len(head) > 0 {
			mimeType = http.DetectContentType(head)
		}
		body = buffered
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if err := s.filesystem.SetReaderWithContentType(key, body, mimeType, 0); err != nil {
		return nil, fmt.Errorf("存储文件失败: %w", err)
	}

	diskInfo := filesystem.DefaultDiskInfo(s.filesystem.Config)

	builder := s.Client.Attachment.Create().
		SetName(originalName).
		SetOriginalName(originalName).
		SetPath(key).
		SetDisk(diskInfo.Disk).
		SetDriver(diskInfo.Driver).
		SetDirectory(uploadDirectory).
		SetContentHash(contentHash).
		SetOriginalNameHash(originalNameHash).
		SetMimeType(mimeType).
		SetExtension(ext).
		SetSize(size).
		SetNillableGroupID(groupID).
		SetUploadedBy(userID)

	a, err := builder.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			if duplicate, dupErr := s.findDuplicateAttachment(ctx, contentHash, size, originalNameHash); dupErr == nil {
				return s.toAttachmentResponse(duplicate), nil
			}
		}
		_ = s.filesystem.Delete(key)
		return nil, fmt.Errorf("保存附件记录失败: %w", err)
	}

	return s.toAttachmentResponse(a), nil
}

// attachmentInUse 判断附件是否被业务单据引用。当前仅有陪玩报单的年龄核验证据
// 引用附件；后续新增引用方时应扩展为引用方登记，而不是逐个硬编码。
func (s *AttachmentService) attachmentInUse(ctx context.Context, ids []int) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	return s.Client.EsportsAgeCheck.Query().
		Where(esportsagecheck.EvidenceAttachmentIDIn(ids...)).Exist(ctx)
}

func (s *AttachmentService) Delete(ctx context.Context, id int) error {
	a, err := s.Client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAttachmentNotFound
		}
		return fmt.Errorf("查询附件失败: %w", err)
	}

	if inUse, err := s.attachmentInUse(ctx, []int{id}); err != nil {
		return fmt.Errorf("检查附件引用失败: %w", err)
	} else if inUse {
		return ErrAttachmentDeleteFailed
	}

	if fs, fsErr := s.resolveFilesystem(a.Disk); fsErr == nil {
		_ = fs.Delete(a.Path)
	}

	err = s.Client.Attachment.DeleteOneID(id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除附件记录失败: %w", err)
	}

	return nil
}

func (s *AttachmentService) BatchDelete(ctx context.Context, ids []int) error {
	attachments, err := s.Client.Attachment.Query().
		Where(attachment.IDIn(ids...)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("查询附件失败: %w", err)
	}

	if inUse, err := s.attachmentInUse(ctx, ids); err != nil {
		return fmt.Errorf("检查附件引用失败: %w", err)
	} else if inUse {
		return ErrAttachmentDeleteFailed
	}

	if s.filesystem != nil {
		for _, a := range attachments {
			if fs, fsErr := s.resolveFilesystem(a.Disk); fsErr == nil {
				_ = fs.Delete(a.Path)
			}
		}
	}

	_, err = s.Client.Attachment.Delete().
		Where(attachment.IDIn(ids...)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("批量删除附件记录失败: %w", err)
	}

	return nil
}

func (s *AttachmentService) Get(ctx context.Context, id int) (*AttachmentResponse, error) {
	return GetByID(ctx,
		func(ctx context.Context) (*ent.Attachment, error) {
			return s.Client.Attachment.Query().Where(attachment.IDEQ(id)).Only(ctx)
		}, s.toAttachmentResponse, ErrAttachmentNotFound, "附件")
}

func (s *AttachmentService) List(ctx context.Context, req *ListAttachmentsRequest) (*ListAttachmentsResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.Attachment.Query()
	if req.Keyword != "" {
		query = query.Where(
			attachment.Or(
				attachment.NameContains(req.Keyword),
				attachment.OriginalNameContains(req.Keyword),
			),
		)
	}
	if req.GroupID != nil {
		query = query.Where(attachment.GroupIDEQ(*req.GroupID))
	}
	if req.MimeType != "" {
		query = query.Where(attachment.MimeTypeHasPrefix(req.MimeType))
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取附件总数失败: %w", err)
	}

	query = query.Order(ent.Desc(attachment.FieldCreatedAt))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	attachments, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取附件列表失败: %w", err)
	}

	return BuildList(attachments, total, req.Page, s.toAttachmentResponse), nil
}

func (s *AttachmentService) GetFileData(ctx context.Context, id int) ([]byte, string, string, error) {
	a, err := s.Client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, "", "", ErrAttachmentNotFound
		}
		return nil, "", "", fmt.Errorf("查询附件失败: %w", err)
	}

	fs, err := s.resolveFilesystem(a.Disk)
	if err != nil {
		return nil, "", "", err
	}

	data, err := fs.Get(a.Path)
	if err != nil {
		return nil, "", "", fmt.Errorf("获取文件数据失败: %w", err)
	}
	if data == nil {
		return nil, "", "", ErrAttachmentNotFound
	}

	return data, a.MimeType, a.OriginalName, nil
}

func (s *AttachmentService) MoveToGroup(ctx context.Context, id int, groupID *int) error {
	a, err := s.Client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAttachmentNotFound
		}
		return fmt.Errorf("查询附件失败: %w", err)
	}

	update := s.Client.Attachment.UpdateOne(a)
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

func (s *AttachmentService) CreateAccessURL(ctx context.Context, id int, basePath string, ttl time.Duration, secret string) (string, error) {
	_, err := s.Client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", ErrAttachmentNotFound
		}
		return "", fmt.Errorf("查询附件失败: %w", err)
	}

	return createAttachmentTokenAccessURL(id, basePath, ttl, secret)
}

func (s *AttachmentService) BuildAccessURL(ctx context.Context, id int, basePath string, ttl time.Duration, secret string) (*AttachmentAccessURL, error) {
	a, err := s.Client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAttachmentNotFound
		}
		return nil, fmt.Errorf("查询附件失败: %w", err)
	}

	disk, driver := s.attachmentDiskDriver(a)
	public := filesystem.IsPublicDirectory(attachmentDirectory(a))
	var mode string // 所有 switch 分支都会赋值，无需初值
	expiresIn := int64(0)
	var accessURL string

	switch strings.ToLower(driver) {
	case "s3":
		fs, fsErr := s.resolveFilesystem(disk)
		if fsErr != nil {
			return nil, fsErr
		}
		if public {
			accessURL, err = filesystem.S3PublicURL(fs.Config, disk, a.Path)
			mode = "public_s3"
		} else {
			accessURL, err = filesystem.S3PresignedGetURL(ctx, fs.Config, disk, a.Path, ttl)
			mode = "private_s3"
			expiresIn = int64(ttl / time.Second)
		}
	default:
		if public {
			fs, fsErr := s.resolveFilesystem(disk)
			if fsErr != nil {
				return nil, fsErr
			}
			accessURL, err = filesystem.LocalPublicURL(fs.Config, disk, a.Path)
			mode = "public_local"
		} else {
			accessURL, err = createAttachmentTokenAccessURL(id, basePath, ttl, secret)
			mode = "private_local"
			expiresIn = int64(ttl / time.Second)
		}
	}
	if err != nil {
		return nil, err
	}

	return &AttachmentAccessURL{
		URL:       accessURL,
		ExpiresIn: expiresIn,
		Mode:      mode,
		Public:    public,
		Disk:      disk,
		Driver:    driver,
	}, nil
}

func (s *AttachmentService) BuildXAccelRedirect(ctx context.Context, id int) (*AttachmentXAccelRedirect, error) {
	if s.filesystem == nil {
		return nil, ErrAttachmentStorageUnavailable
	}

	a, err := s.Client.Attachment.Query().
		Where(attachment.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAttachmentNotFound
		}
		return nil, fmt.Errorf("查询附件失败: %w", err)
	}

	disk, driver := s.attachmentDiskDriver(a)
	if strings.ToLower(driver) != "local" {
		return nil, nil
	}

	fs, err := s.resolveFilesystem(disk)
	if err != nil {
		return nil, err
	}
	redirectPath, err := filesystem.XAccelRedirectPath(fs.Config, a.Path)
	if err != nil {
		return nil, err
	}
	return &AttachmentXAccelRedirect{
		Path:     redirectPath,
		MimeType: a.MimeType,
		Filename: a.OriginalName,
	}, nil
}

func createAttachmentTokenAccessURL(id int, basePath string, ttl time.Duration, secret string) (string, error) {
	token, err := crypto.SignExpiringToken(secret, attachmentAccessSubject(id), time.Now().Add(ttl))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%d/content?token=%s", strings.TrimRight(basePath, "/"), id, url.QueryEscape(token)), nil
}

func (s *AttachmentService) ValidateAccessToken(id int, token string, secret string, now time.Time) error {
	subject, err := crypto.VerifyExpiringToken(secret, token, now)
	if err != nil {
		return err
	}
	if subject != attachmentAccessSubject(id) {
		return fmt.Errorf("附件访问凭证不匹配")
	}
	return nil
}

func (s *AttachmentService) toAttachmentResponse(a *ent.Attachment) *AttachmentResponse {
	return &AttachmentResponse{
		ID:           a.ID,
		Name:         a.Name,
		OriginalName: a.OriginalName,
		Path:         a.Path,
		Disk:         a.Disk,
		Driver:       a.Driver,
		Directory:    a.Directory,
		ContentHash:  a.ContentHash,
		MimeType:     a.MimeType,
		Extension:    a.Extension,
		Size:         a.Size,
		GroupID:      a.GroupID,
		UploadedBy:   a.UploadedBy,
		CreatedAt:    a.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:    a.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

func attachmentAccessSubject(id int) string {
	return "attachment:" + strconv.Itoa(id)
}

// resolveFilesystem 返回附件记录实际所在磁盘的文件系统。
// 切换默认磁盘（如 local → s3）后，历史记录仍按自身 disk 字段读取；
// 记录引用了已下线的磁盘配置时回退默认磁盘，与旧行为保持一致。
func (s *AttachmentService) resolveFilesystem(disk string) (*filesystem.Filesystem, error) {
	if s.filesystem == nil {
		return nil, ErrAttachmentStorageUnavailable
	}
	name := strings.TrimSpace(disk)
	if name == "" || name == filesystem.DefaultDiskInfo(s.filesystem.Config).Disk {
		return s.filesystem, nil
	}
	if _, ok := s.filesystem.Config.Filesystem.Disks[name]; !ok {
		return s.filesystem, nil
	}
	return filesystem.Disk(&s.filesystem.Config, name)
}

// attachmentDiskDriver 解析记录实际使用的磁盘与驱动。
// 早期记录可能没有 disk/driver 字段（文件都落在本地），此时不能直接
// 套用当前默认磁盘——否则默认磁盘切到 S3 后旧文件会被误读。
func (s *AttachmentService) attachmentDiskDriver(a *ent.Attachment) (string, string) {
	disk := strings.TrimSpace(a.Disk)
	driver := strings.TrimSpace(a.Driver)

	if disk != "" {
		if driver == "" && s.filesystem != nil {
			for name, configured := range s.filesystem.Config.Filesystem.Disks {
				if name == disk {
					driver = strings.TrimSpace(configured.Driver)
					break
				}
			}
			if driver == "" {
				driver = filesystem.DefaultDiskInfo(s.filesystem.Config).Driver
			}
		}
		if driver == "" {
			driver = "local"
		}
		return disk, driver
	}

	if driver == "" {
		// 最早期记录没有任何存储信息：按历史事实回落本地磁盘。
		if s.filesystem != nil {
			defaultInfo := filesystem.DefaultDiskInfo(s.filesystem.Config)
			if strings.EqualFold(defaultInfo.Driver, "local") {
				return defaultInfo.Disk, defaultInfo.Driver
			}
		}
		return "local", "local"
	}

	// 只有 driver：优先当前默认磁盘，其次查找同驱动的磁盘配置。
	if s.filesystem != nil {
		defaultInfo := filesystem.DefaultDiskInfo(s.filesystem.Config)
		if strings.EqualFold(defaultInfo.Driver, driver) {
			return defaultInfo.Disk, defaultInfo.Driver
		}
		for name, configured := range s.filesystem.Config.Filesystem.Disks {
			if strings.EqualFold(strings.TrimSpace(configured.Driver), driver) {
				return name, driver
			}
		}
	}
	return "local", driver
}

func attachmentDirectory(a *ent.Attachment) string {
	if strings.TrimSpace(a.Directory) != "" {
		return a.Directory
	}
	index := strings.LastIndex(a.Path, "/")
	if index < 0 {
		return ""
	}
	return a.Path[:index]
}

func (s *AttachmentService) findDuplicateAttachment(ctx context.Context, contentHash string, size int64, originalNameHash string) (*ent.Attachment, error) {
	return s.Client.Attachment.Query().
		Where(
			attachment.ContentHashEQ(contentHash),
			attachment.SizeEQ(size),
			attachment.OriginalNameHashEQ(originalNameHash),
		).
		Only(ctx)
}

func resolveAttachmentDirectory(directory string) (string, error) {
	if strings.TrimSpace(directory) == "" {
		now := time.Now()
		return fmt.Sprintf("attachments/%d/%02d", now.Year(), now.Month()), nil
	}
	return filesystem.NormalizeDirectory(directory)
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
