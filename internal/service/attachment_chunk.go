package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/wuwuseo/cmf/filesystem"
	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// ChunkInitRequest 分片上传初始化请求
type ChunkInitRequest struct {
	Filename    string `json:"filename" validate:"required"`
	FileSize    int64  `json:"file_size" validate:"required,min=1"`
	ChunkSize   int64  `json:"chunk_size" validate:"required,min=1"`
	TotalChunks int    `json:"total_chunks" validate:"required,min=1"`
	MimeType    string `json:"mime_type"`
	GroupID     *int   `json:"group_id"`
	Directory   string `json:"directory"`
	UserID      string `json:"user_id"` // 会话属主，写入 session.Owner；为空则不做属主校验
}

// ChunkInitResponse 分片上传初始化响应
type ChunkInitResponse struct {
	UploadID       string `json:"upload_id"`
	TotalChunks    int    `json:"total_chunks"`
	ChunkSize      int64  `json:"chunk_size"`
	UploadedChunks []int  `json:"uploaded_chunks"` // 已上传的分片索引
}

// ChunkUploadResponse 分片上传响应
type ChunkUploadResponse struct {
	ChunkIndex int  `json:"chunk_index"`
	Received   bool `json:"received"`
}

// ChunkMergeRequest 分片合并请求
type ChunkMergeRequest struct {
	UploadID string `json:"upload_id" validate:"required"`
}

// ChunkStatusResponse 分片上传状态响应
type ChunkStatusResponse struct {
	UploadID       string `json:"upload_id"`
	Filename       string `json:"filename"`
	FileSize       int64  `json:"file_size"`
	TotalChunks    int    `json:"total_chunks"`
	ChunkSize      int64  `json:"chunk_size"`
	UploadedChunks []int  `json:"uploaded_chunks"`
	MimeType       string `json:"mime_type"`
}

// UnifiedUploadRequest 统一上传请求（简单上传 + 分片上传共用）
type UnifiedUploadRequest struct {
	FileHeader  *multipart.FileHeader
	UploadID    string // 分片上传会话ID，简单上传或首次分片时为空
	ChunkIndex  int    // 分片索引（0-based）
	TotalChunks int    // 总片数，0 表示简单上传
	Filename    string // 文件名
	FileSize    int64  // 文件总大小（首次分片时必填）
	ChunkSize   int64  // 单片大小（首次分片时必填）
	MimeType    string
	GroupID     *int
	Directory   string
	UserID      string
}

// UnifiedUploadResponse 统一上传响应
type UnifiedUploadResponse struct {
	// 简单上传或分片合并后的附件信息
	Attachment *AttachmentResponse `json:"attachment,omitempty"`
	// 分片确认（非最后一片时返回）
	UploadID    string `json:"upload_id,omitempty"`
	ChunkIndex  int    `json:"chunk_index,omitempty"`
	Received    bool   `json:"received,omitempty"`
	TotalChunks int    `json:"total_chunks,omitempty"`
}

// chunkSession 分片上传会话
type chunkSession struct {
	UploadID    string
	Owner       string // 会话属主（上传者身份）；为空表示遗留未归属会话，不做属主校验
	Filename    string
	FileSize    int64
	ChunkSize   int64
	TotalChunks int
	MimeType    string
	GroupID     *int
	Uploaded    map[int]bool // 已上传的分片索引
	Directory   string
	CreatedAt   time.Time
	ExpiresAt   time.Time // 会话过期时间
	mu          sync.Mutex
	deleted     atomic.Bool   // LoadAndDelete 后标记，阻止并发 UploadChunk 写入
	sem         chan struct{} // 并发控制信号量，限制同一会话的并发分片上传数
}

// ensureSessionOwner 校验分片会话属主。
// 不匹配时返回与会话不存在相同的错误，避免向调用者泄露他人会话的存在性；
// Owner 为空的遗留会话（旧接口 InitChunkUpload 创建）保持原有行为不做限制。
func ensureSessionOwner(session *chunkSession, callerID string) error {
	if session.Owner != "" && callerID != session.Owner {
		return ErrChunkSessionNotFound
	}
	return nil
}

// ChunkUploadError 分片上传错误，携带错误码供 Handler 层通过 errors.As 精准映射
// 替代原有的 strings.Contains 字符串匹配，消除 Service↔Handler 间的隐式契约
type ChunkUploadError struct {
	Code    int
	Message string
}

func (e *ChunkUploadError) Error() string {
	return e.Message
}

// 分片上传预定义哨兵错误
var (
	ErrChunkSessionNotFound    = &ChunkUploadError{global.ErrCodeChunkUploadNotFound, "分片上传会话不存在"}
	ErrChunkSessionEnded       = &ChunkUploadError{global.ErrCodeChunkUploadNotFound, "分片上传会话已结束"}
	ErrChunkSessionExpired     = &ChunkUploadError{global.ErrCodeChunkUploadExpired, "分片上传会话已过期"}
	ErrChunkInvalidIndex       = &ChunkUploadError{global.ErrCodeChunkUploadInvalidChunk, "无效的分片索引"}
	ErrChunkMergeIncomplete    = &ChunkUploadError{global.ErrCodeChunkUploadMergeFailed, "部分分片尚未上传"}
	ErrChunkConcurrencyLimit   = &ChunkUploadError{global.ErrCodeUploadConcurrencyLimit, "上传并发数超限，请稍后重试"}
	ErrFileSizeExceeded        = &ChunkUploadError{global.ErrCodeFileSizeExceeded, "文件大小超过限制"}
	ErrChunkedFileSizeExceeded = &ChunkUploadError{global.ErrCodeFileSizeExceeded, "文件总大小超过限制"}
	ErrChunkSizeExceeded       = &ChunkUploadError{global.ErrCodeChunkSizeExceeded, "单分片大小超过限制"}
	ErrMimeTypeNotAllowed      = &ChunkUploadError{global.ErrCodeMimeTypeNotAllowed, "不支持的文件类型"}
	ErrSVGUploadBlocked        = &ChunkUploadError{global.ErrCodeMimeTypeNotAllowed, "不支持上传SVG文件：SVG可内嵌脚本，回显时存在存储型XSS风险"}
)

// chunkStore 管理所有分片上传会话
var chunkStore = sync.Map{} // upload_id → *chunkSession

// chunkCleanupCancel 分片会话清理协程的取消函数
var chunkCleanupCancel context.CancelFunc

// chunkBaseDir 分片存储的基础目录（相对执行目录）
const chunkBaseDir = "./data/storage/chunks"

// 上传安全限制常量
const (
	maxSimpleFileSize             = 100 * 1024 * 1024      // 简单上传最大 100MB
	maxChunkedFileSize            = 5 * 1024 * 1024 * 1024 // 分片上传总文件最大 5GB
	maxChunkSize                  = 50 * 1024 * 1024       // 单分片最大 50MB
	maxConcurrentChunksPerSession = 3                      // 同一会话最大并发分片数
	sessionTTL                    = 24 * time.Hour         // 分片会话过期时间
	cleanupInterval               = 1 * time.Hour          // 过期会话清理间隔
)

// allowedMimeTypes 允许上传的 MIME 类型白名单
var allowedMimeTypes = map[string]bool{
	// 图片（不含 image/svg+xml：SVG 可内嵌脚本，构成存储型 XSS，见 rejectBlockedUpload）
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
	"image/tiff": true,
	// 文档
	"application/pdf":  true,
	"text/plain":       true,
	"text/csv":         true,
	"text/html":        true,
	"text/xml":         true,
	"application/json": true,
	"application/xml":  true,
	// Office 文档
	"application/msword": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
	"application/vnd.ms-excel": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	"application/vnd.ms-powerpoint":                                             true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	// 压缩包
	"application/zip":              true,
	"application/x-rar-compressed": true,
	"application/x-7z-compressed":  true,
	"application/gzip":             true,
	"application/x-tar":            true,
	// 音视频
	"audio/mpeg":      true,
	"audio/wav":       true,
	"audio/ogg":       true,
	"audio/aac":       true,
	"video/mp4":       true,
	"video/webm":      true,
	"video/x-msvideo": true,
	"video/quicktime": true,
	// 通用
	// S4: 移除 application/octet-stream——它是无法识别格式时的默认回退值，
	// 放行会允许任意无魔数二进制文件通过 MIME 校验，削弱防护效果。
	// 合法文件应匹配到具体 MIME 类型。
}

// isMimeTypeAllowed 检查 MIME 类型是否在白名单中
func isMimeTypeAllowed(mimeType string) bool {
	if mimeType == "" {
		return true // 未指定 MIME 时放行，后续会默认设为 application/octet-stream
	}
	// 去除参数（如 charset=utf-8）
	if idx := strings.Index(mimeType, ";"); idx > 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	// 精确匹配
	if allowedMimeTypes[mimeType] {
		return true
	}
	// 通配符匹配（如 image/*）
	if idx := strings.Index(mimeType, "/"); idx > 0 {
		wildcard := mimeType[:idx] + "/*"
		if allowedMimeTypes[wildcard] {
			return true
		}
	}
	return false
}

// getExt 获取文件扩展名（不含点）
func getExt(filename string) string {
	ext := filepath.Ext(filename)
	if ext != "" {
		return ext[1:] // 去掉前导点
	}
	return ""
}

// generateDeterministicUploadID 生成确定性 upload_id，使并发首片请求幂等
// 基于 filename + fileSize + userID 的 SHA1 哈希，同一用户对同一文件多次发起首片请求得到相同 ID
func generateDeterministicUploadID(filename string, fileSize int64, userID string) string {
	h := sha1.New()
	h.Write([]byte(filename))
	h.Write([]byte(strconv.FormatInt(fileSize, 10)))
	h.Write([]byte(userID))
	return hex.EncodeToString(h.Sum(nil))
}

// initChunkSession 初始化分片上传会话（内部方法，支持外部指定 uploadID 实现幂等）
func (s *AttachmentService) initChunkSession(uploadID string, req *ChunkInitRequest) (*ChunkInitResponse, error) {
	now := time.Now()
	session := &chunkSession{
		UploadID:    uploadID,
		Owner:       req.UserID,
		Filename:    req.Filename,
		FileSize:    req.FileSize,
		ChunkSize:   req.ChunkSize,
		TotalChunks: req.TotalChunks,
		MimeType:    req.MimeType,
		GroupID:     req.GroupID,
		Directory:   req.Directory,
		Uploaded:    make(map[int]bool),
		CreatedAt:   now,
		ExpiresAt:   now.Add(sessionTTL),
		sem:         make(chan struct{}, maxConcurrentChunksPerSession),
	}

	// 确保分片目录存在
	chunkDir := filepath.Join(chunkBaseDir, uploadID)
	if err := os.MkdirAll(chunkDir, 0755); err != nil {
		return nil, fmt.Errorf("创建分片目录失败: %w", err)
	}

	// 扫描已有的分片（断点续传场景）
	entries, err := os.ReadDir(chunkDir)
	if err != nil {
		return nil, fmt.Errorf("读取分片目录失败: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		idx, convErr := strconv.Atoi(entry.Name())
		if convErr == nil && idx >= 0 && idx < req.TotalChunks {
			session.Uploaded[idx] = true
		}
	}

	chunkStore.Store(uploadID, session)

	uploadedList := make([]int, 0, len(session.Uploaded))
	for idx := range session.Uploaded {
		uploadedList = append(uploadedList, idx)
	}
	sort.Ints(uploadedList)

	return &ChunkInitResponse{
		UploadID:       uploadID,
		TotalChunks:    req.TotalChunks,
		ChunkSize:      req.ChunkSize,
		UploadedChunks: uploadedList,
	}, nil
}

// InitChunkUpload 初始化分片上传（生成随机 uploadID）
func (s *AttachmentService) InitChunkUpload(ctx context.Context, req *ChunkInitRequest) (*ChunkInitResponse, error) {
	return s.initChunkSession(uuid.New().String(), req)
}

// UploadChunk 上传单个分片（callerID 为调用者身份，用于会话属主校验）
func (s *AttachmentService) UploadChunk(ctx context.Context, callerID string, uploadID string, chunkIndex int, fileHeader *multipart.FileHeader) (*ChunkUploadResponse, error) {
	// 查找会话
	val, ok := chunkStore.Load(uploadID)
	if !ok {
		return nil, ErrChunkSessionNotFound
	}
	session, ok := val.(*chunkSession)
	if !ok {
		return nil, fmt.Errorf("分片上传会话类型错误")
	}

	// 属主校验：非本人不得向会话追加分片
	if err := ensureSessionOwner(session, callerID); err != nil {
		return nil, err
	}

	// 检查会话是否已被合并/取消标记删除
	if session.deleted.Load() {
		return nil, ErrChunkSessionEnded
	}

	// 检查会话是否已过期
	if time.Now().After(session.ExpiresAt) {
		return nil, ErrChunkSessionExpired
	}

	// 校验分片索引
	if chunkIndex < 0 || chunkIndex >= session.TotalChunks {
		return nil, fmt.Errorf("%w: %d", ErrChunkInvalidIndex, chunkIndex)
	}

	// W1: 用 fileHeader.Size 做实际大小校验，防止客户端声明 ChunkSize 绕过限制导致 OOM
	// 放在信号量之前——fileHeader.Size 来自请求元数据，无需信号量保护，提前拒绝避免浪费并发槽位
	if fileHeader.Size > maxChunkSize {
		return nil, fmt.Errorf("%w（最大 %dMB）", ErrChunkSizeExceeded, maxChunkSize/(1024*1024))
	}

	// 并发控制：通过信号量限制同一会话的并发分片数
	select {
	case session.sem <- struct{}{}:
		defer func() { <-session.sem }()
	case <-ctx.Done():
		return nil, fmt.Errorf("上传已取消")
	default:
		return nil, ErrChunkConcurrencyLimit
	}

	// 二次检查：获取信号量后再次确认会话未被删除
	if session.deleted.Load() {
		return nil, ErrChunkSessionEnded
	}

	// W3: 二次检查过期时间——防止 CleanupExpiredSessions 在信号量获取间隙标记删除后的 TOCTOU
	if time.Now().After(session.ExpiresAt) {
		return nil, ErrChunkSessionExpired
	}

	// 打开上传的分片
	file, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("打开分片文件失败: %w", err)
	}
	defer file.Close()

	// 读取分片数据
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("读取分片数据失败: %w", err)
	}

	// 写入分片到磁盘
	chunkDir := filepath.Join(chunkBaseDir, uploadID)
	if err := os.MkdirAll(chunkDir, 0755); err != nil {
		return nil, fmt.Errorf("创建分片目录失败: %w", err)
	}
	chunkPath := filepath.Join(chunkDir, strconv.Itoa(chunkIndex))
	if err := os.WriteFile(chunkPath, data, 0644); err != nil {
		return nil, fmt.Errorf("写入分片失败: %w", err)
	}

	// 写入后二次检查：防止写入期间 session 被 Cancel/Merge 删除导致孤儿分片
	// 不主动删除已写入的 chunk 文件——MergeChunks 的 defer RemoveAll 会统一清理目录
	if session.deleted.Load() {
		return nil, ErrChunkSessionEnded
	}

	// 标记分片已上传
	session.mu.Lock()
	session.Uploaded[chunkIndex] = true
	session.mu.Unlock()

	return &ChunkUploadResponse{
		ChunkIndex: chunkIndex,
		Received:   true,
	}, nil
}

// MergeChunks 合并分片并创建附件记录（使用 LoadAndDelete 原子化防止并发重复合并）
// userID 同时作为会话属主校验依据
func (s *AttachmentService) MergeChunks(ctx context.Context, uploadID string, userID string) (*AttachmentResponse, error) {
	// 属主预检：必须在 LoadAndDelete 之前，避免非本人调用消耗掉他人的会话
	if val, ok := chunkStore.Load(uploadID); ok {
		if session, ok := val.(*chunkSession); ok {
			if err := ensureSessionOwner(session, userID); err != nil {
				return nil, err
			}
		}
	}

	// 原子化获取并删除会话，防止并发重复合并
	val, ok := chunkStore.LoadAndDelete(uploadID)
	if !ok {
		return nil, ErrChunkSessionNotFound
	}
	session, ok := val.(*chunkSession)
	if !ok {
		return nil, fmt.Errorf("分片上传会话类型错误")
	}

	// 标记会话已删除，阻止尚在执行的并发 UploadChunk 继续写入
	session.deleted.Store(true)

	// 提前注册清理：无论完整性检查通过与否，都要清理分片目录
	chunkDir := filepath.Join(chunkBaseDir, uploadID)
	defer func() {
		_ = os.RemoveAll(chunkDir)
	}()

	// 检查所有分片是否已上传
	session.mu.Lock()
	for i := 0; i < session.TotalChunks; i++ {
		if !session.Uploaded[i] {
			session.mu.Unlock()
			return nil, fmt.Errorf("%w: 分片 %d", ErrChunkMergeIncomplete, i)
		}
	}
	session.mu.Unlock()

	return s.mergeChunksWithDedupe(ctx, session, chunkDir, userID)
}

// UnifiedUpload 统一上传入口：自动识别简单上传/分片上传
// - TotalChunks == 0：简单上传，直接存储并创建附件
// - TotalChunks > 0 && UploadID == ""：首次分片，使用确定性 upload_id 实现幂等初始化
// - TotalChunks > 0 && UploadID != ""：后续分片，追加到已有会话
// - ChunkIndex == TotalChunks - 1：最后一片，自动合并并返回附件信息
func (s *AttachmentService) mergeChunksWithDedupe(ctx context.Context, session *chunkSession, chunkDir string, userID string) (*AttachmentResponse, error) {
	if s.filesystem == nil {
		return nil, ErrAttachmentStorageUnavailable
	}

	originalName, err := filesystem.SafeOriginalName(session.Filename)
	if err != nil {
		return nil, err
	}
	// 合并前纵深拦截：即使首片校验被绕过，落盘合并时仍按扩展名/MIME 拒绝
	if err := rejectBlockedUpload(originalName, session.MimeType); err != nil {
		return nil, err
	}
	uploadDirectory, err := resolveAttachmentDirectory(session.Directory)
	if err != nil {
		return nil, err
	}

	hashReaders, hashFiles, err := openChunkReaders(session, chunkDir)
	if err != nil {
		return nil, err
	}
	contentHash, totalSize, err := filesystem.ContentHashReader(io.MultiReader(hashReaders...))
	closeFiles(hashFiles)
	if err != nil {
		return nil, fmt.Errorf("计算分片文件哈希失败: %w", err)
	}

	originalNameHash := filesystem.HashString(originalName)
	if duplicate, err := s.findDuplicateAttachment(ctx, contentHash, totalSize, originalNameHash); err == nil {
		return s.toAttachmentResponse(duplicate), nil
	} else if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("查询重复附件失败: %w", err)
	}

	ext := getExt(originalName)
	key, err := filesystem.BuildContentAddressedKey(uploadDirectory, contentHash, originalNameHash, ext)
	if err != nil {
		return nil, err
	}

	readers, files, err := openChunkReaders(session, chunkDir)
	if err != nil {
		return nil, err
	}
	if err := s.filesystem.SetReader(key, io.MultiReader(readers...), 0); err != nil {
		closeFiles(files)
		return nil, fmt.Errorf("存储文件失败: %w", err)
	}
	closeFiles(files)

	mimeType := session.MimeType
	if mimeType == "" {
		firstChunkPath := filepath.Join(chunkDir, "0")
		firstChunkData, err := os.ReadFile(firstChunkPath)
		if err == nil && len(firstChunkData) > 0 {
			detectSize := 512
			if len(firstChunkData) < detectSize {
				detectSize = len(firstChunkData)
			}
			mimeType = http.DetectContentType(firstChunkData[:detectSize])
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
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
		SetSize(totalSize).
		SetNillableGroupID(session.GroupID).
		SetUploadedBy(userID)

	a, err := builder.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			if duplicate, dupErr := s.findDuplicateAttachment(ctx, contentHash, totalSize, originalNameHash); dupErr == nil {
				return s.toAttachmentResponse(duplicate), nil
			}
		}
		_ = s.filesystem.Delete(key)
		return nil, fmt.Errorf("保存附件记录失败: %w", err)
	}

	return s.toAttachmentResponse(a), nil
}

func openChunkReaders(session *chunkSession, chunkDir string) ([]io.Reader, []*os.File, error) {
	readers := make([]io.Reader, 0, session.TotalChunks)
	files := make([]*os.File, 0, session.TotalChunks)
	for i := 0; i < session.TotalChunks; i++ {
		chunkPath := filepath.Join(chunkDir, strconv.Itoa(i))
		file, err := os.Open(chunkPath)
		if err != nil {
			closeFiles(files)
			return nil, nil, fmt.Errorf("打开分片 %d 失败: %w", i, err)
		}
		files = append(files, file)
		readers = append(readers, file)
	}
	return readers, files, nil
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func (s *AttachmentService) UnifiedUpload(ctx context.Context, req *UnifiedUploadRequest) (*UnifiedUploadResponse, error) {
	// 判断是否为文件头分片或简单上传（只有这两种情况需要做二进制 MIME 检测）
	// isFileHeaderChunk 要求 ChunkIndex==0 且 UploadID 为空，防止乱序首片绕过文件头魔数检测
	isFileHeaderChunk := req.TotalChunks > 0 && req.ChunkIndex == 0 && req.UploadID == ""
	isSimpleUpload := req.TotalChunks == 0

	// 安全校验 1：MIME 类型白名单
	// 仅简单上传和首个分片做 http.DetectContentType 二进制检测（读取文件头魔数）
	// 后续分片的前 512 字节来自文件中部，不含魔数，DetectContentType 会误判为 application/octet-stream
	if isSimpleUpload || isFileHeaderChunk {
		mimeType := req.MimeType
		if mimeType == "" && req.FileHeader != nil {
			mimeType = req.FileHeader.Header.Get("Content-Type")
		}
		// 通过 http.DetectContentType 二次检测真实 MIME（读取文件头，不信任客户端声明）
		if req.FileHeader != nil {
			file, err := req.FileHeader.Open()
			if err == nil {
				buf := make([]byte, 512)
				n, readErr := io.ReadFull(file, buf)
				file.Close()
				// S6: 区分 EOF/UnexpectedEOF（正常短读/读取不足）与其他 I/O 错误
				if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
					// 真实 I/O 错误，回退到客户端声明校验而非静默放行
					if !isMimeTypeAllowed(mimeType) {
						return nil, fmt.Errorf("%w: %s", ErrMimeTypeNotAllowed, mimeType)
					}
					// I/O 错误仍记录日志供排查
					log.Warn("MIME 检测读取文件头失败，回退到客户端声明校验", zap.Error(readErr))
				}
				if n > 0 {
					detected := http.DetectContentType(buf[:n])
					if !isMimeTypeAllowed(detected) {
						return nil, fmt.Errorf("%w: %s", ErrMimeTypeNotAllowed, detected)
					}
					// 将检测到的真实 MIME 回传给 req，确保首片 session 存储正确的 MIME 类型
					// 而非依赖客户端可能未填的 mime_type 字段（为空时默认 application/octet-stream）
					if req.MimeType == "" {
						req.MimeType = detected
					}
				} else {
					// 空文件无法检测真实MIME，回退到客户端声明校验
					if !isMimeTypeAllowed(mimeType) {
						return nil, fmt.Errorf("%w: %s", ErrMimeTypeNotAllowed, mimeType)
					}
				}
			} else {
				// 文件打开失败，回退到客户端声明校验
				if !isMimeTypeAllowed(mimeType) {
					return nil, fmt.Errorf("%w: %s", ErrMimeTypeNotAllowed, mimeType)
				}
			}
		} else if !isMimeTypeAllowed(mimeType) {
			return nil, fmt.Errorf("%w: %s", ErrMimeTypeNotAllowed, mimeType)
		}
	}
	// 后续分片：MIME 已在首片验证过，跳过二进制检测

	// 安全校验 2 & 3：文件大小上限 & 分片大小上限
	if req.TotalChunks == 0 {
		// 简单上传：限制单文件大小
		if req.FileHeader != nil && req.FileHeader.Size > maxSimpleFileSize {
			return nil, fmt.Errorf("%w（最大 %dMB）", ErrFileSizeExceeded, maxSimpleFileSize/(1024*1024))
		}
		attachment, err := s.Upload(ctx, req.FileHeader, req.GroupID, req.UserID, req.Directory)
		if err != nil {
			return nil, err
		}
		return &UnifiedUploadResponse{Attachment: attachment}, nil
	}

	// 分片上传：校验总文件大小 & 单片大小
	if req.FileSize > maxChunkedFileSize {
		return nil, fmt.Errorf("%w（最大 %dGB）", ErrChunkedFileSizeExceeded, maxChunkedFileSize/(1024*1024*1024))
	}
	if req.ChunkSize > maxChunkSize {
		return nil, fmt.Errorf("%w（最大 %dMB）", ErrChunkSizeExceeded, maxChunkSize/(1024*1024))
	}

	// 分片上传
	uploadID := req.UploadID
	isFirstChunk := uploadID == ""

	// 首次分片：使用确定性 upload_id + LoadOrStore 实现原子化初始化
	if isFirstChunk {
		// SVG 双重拦截（分片路径不经过 Upload）：按扩展名与声明 MIME 拒绝
		declaredMime := req.MimeType
		if declaredMime == "" && req.FileHeader != nil {
			declaredMime = req.FileHeader.Header.Get("Content-Type")
		}
		if err := rejectBlockedUpload(req.Filename, declaredMime); err != nil {
			return nil, err
		}

		uploadID = generateDeterministicUploadID(req.Filename, req.FileSize, req.UserID)

		// 预创建会话对象
		now := time.Now()
		session := &chunkSession{
			UploadID:    uploadID,
			Owner:       req.UserID,
			Filename:    req.Filename,
			FileSize:    req.FileSize,
			ChunkSize:   req.ChunkSize,
			TotalChunks: req.TotalChunks,
			MimeType:    req.MimeType,
			GroupID:     req.GroupID,
			Directory:   req.Directory,
			Uploaded:    make(map[int]bool),
			CreatedAt:   now,
			ExpiresAt:   now.Add(sessionTTL),
			sem:         make(chan struct{}, maxConcurrentChunksPerSession),
		}

		// 原子化：LoadOrStore 保证仅一个协程执行 I/O 初始化（修复 TOCTOU）
		if _, loaded := chunkStore.LoadOrStore(uploadID, session); !loaded {
			// 创建分片目录
			chunkDir := filepath.Join(chunkBaseDir, uploadID)
			if err := os.MkdirAll(chunkDir, 0755); err != nil {
				chunkStore.Delete(uploadID)
				return nil, fmt.Errorf("初始化分片上传失败: %w", err)
			}

			// 扫描已有分片（断点续传场景）
			entries, err := os.ReadDir(chunkDir)
			if err != nil {
				chunkStore.Delete(uploadID)
				return nil, fmt.Errorf("初始化分片上传失败: %w", err)
			}
			// 持锁写入 Uploaded map，防止与并发 UploadChunk 冲突
			session.mu.Lock()
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				if idx, convErr := strconv.Atoi(entry.Name()); convErr == nil && idx >= 0 && idx < req.TotalChunks {
					session.Uploaded[idx] = true
				}
			}
			session.mu.Unlock()
		}
	}

	// 上传分片
	_, err := s.UploadChunk(ctx, req.UserID, uploadID, req.ChunkIndex, req.FileHeader)
	if err != nil {
		// 孤儿会话交由 sessionTTL 定期清理，避免误删并发成功的分片数据
		return nil, err
	}

	// 最后一片：自动合并（MergeChunks 使用 LoadAndDelete 原子化）
	if req.ChunkIndex == req.TotalChunks-1 {
		attachment, err := s.MergeChunks(ctx, uploadID, req.UserID)
		if err != nil {
			return nil, err
		}
		return &UnifiedUploadResponse{Attachment: attachment}, nil
	}

	// 非最后一片：返回分片确认
	return &UnifiedUploadResponse{
		UploadID:    uploadID,
		ChunkIndex:  req.ChunkIndex,
		Received:    true,
		TotalChunks: req.TotalChunks,
	}, nil
}

// GetChunkStatus 获取分片上传状态（callerID 为调用者身份，用于会话属主校验）
func (s *AttachmentService) GetChunkStatus(callerID string, uploadID string) (*ChunkStatusResponse, error) {
	val, ok := chunkStore.Load(uploadID)
	if !ok {
		return nil, ErrChunkSessionNotFound
	}
	session, ok := val.(*chunkSession)
	if !ok {
		return nil, fmt.Errorf("分片上传会话类型错误")
	}

	// 属主校验：非本人查询视为会话不存在，不泄露他人会话进度
	if err := ensureSessionOwner(session, callerID); err != nil {
		return nil, err
	}

	session.mu.Lock()
	uploadedList := make([]int, 0, len(session.Uploaded))
	for idx := range session.Uploaded {
		uploadedList = append(uploadedList, idx)
	}
	session.mu.Unlock()
	sort.Ints(uploadedList)

	return &ChunkStatusResponse{
		UploadID:       uploadID,
		Filename:       session.Filename,
		FileSize:       session.FileSize,
		TotalChunks:    session.TotalChunks,
		ChunkSize:      session.ChunkSize,
		UploadedChunks: uploadedList,
		MimeType:       session.MimeType,
	}, nil
}

// CancelChunkUpload 取消分片上传并清理数据
// 先校验属主并原子化移除会话（阻止并发 UploadChunk），再清理磁盘目录
func (s *AttachmentService) CancelChunkUpload(callerID string, uploadID string) error {
	// 属主预检：必须在 LoadAndDelete 之前，避免非本人调用取消他人的会话
	if val, ok := chunkStore.Load(uploadID); ok {
		if session, ok := val.(*chunkSession); ok {
			if err := ensureSessionOwner(session, callerID); err != nil {
				return err
			}
		}
	}

	// 原子化移除会话，防止并发 UploadChunk 获取 session 后写入已删除目录
	val, loaded := chunkStore.LoadAndDelete(uploadID)
	if !loaded {
		// 会话已被 MergeChunks 消费，由 MergeChunks 的 defer RemoveAll 负责清理
		return nil
	}
	if session, ok := val.(*chunkSession); ok {
		session.deleted.Store(true)
	}

	// 仅当成功获取会话时才清理目录，避免与 MergeChunks 的并发 I/O 竞态
	chunkDir := filepath.Join(chunkBaseDir, uploadID)
	if err := os.RemoveAll(chunkDir); err != nil {
		return fmt.Errorf("清理分片目录失败: %w", err)
	}
	return nil
}

// StartChunkSessionCleanup 启动分片会话过期清理协程
// 定期扫描 chunkStore，清理过期的会话及其磁盘分片目录
// 应在应用启动时作为后台 goroutine 调用
func StartChunkSessionCleanup() {
	ctx, cancel := context.WithCancel(context.Background())
	chunkCleanupCancel = cancel

	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Info("分片会话清理协程已停止")
				return
			case <-ticker.C:
				CleanupExpiredSessions()
			}
		}
	}()
}

// StopChunkSessionCleanup 停止分片会话清理协程
func StopChunkSessionCleanup() {
	if chunkCleanupCancel != nil {
		chunkCleanupCancel()
	}
}

// CleanupExpiredSessions 清理所有过期会话（导出供优雅关闭前最后调用一次）
func CleanupExpiredSessions() {
	now := time.Now()
	var expiredIDs []string

	chunkStore.Range(func(key, value any) bool {
		session, ok := value.(*chunkSession)
		if !ok {
			return true
		}
		if now.After(session.ExpiresAt) {
			expiredIDs = append(expiredIDs, key.(string))
		}
		return true
	})

	for _, uploadID := range expiredIDs {
		// 原子化移除会话
		val, loaded := chunkStore.LoadAndDelete(uploadID)
		if !loaded {
			continue
		}
		if session, ok := val.(*chunkSession); ok {
			session.deleted.Store(true)
		}
		// 清理磁盘目录
		chunkDir := filepath.Join(chunkBaseDir, uploadID)
		if err := os.RemoveAll(chunkDir); err != nil {
			log.Error("清理过期分片目录失败", zap.String("upload_id", uploadID), zap.Error(err))
		} else {
			log.Info("已清理过期分片会话", zap.String("upload_id", uploadID))
		}
	}
}

// 确保 AttachmentService 实现了分片上传接口（编译期检查）
var _ ChunkUploadService = (*AttachmentService)(nil)

// ChunkUploadService 分片上传服务接口
type ChunkUploadService interface {
	// UnifiedUpload 统一上传入口（简单上传 + 分片上传）
	UnifiedUpload(ctx context.Context, req *UnifiedUploadRequest) (*UnifiedUploadResponse, error)
	// 分片上传状态与取消（callerID 为调用者身份，用于会话属主校验）
	GetChunkStatus(callerID string, uploadID string) (*ChunkStatusResponse, error)
	CancelChunkUpload(callerID string, uploadID string) error
}
