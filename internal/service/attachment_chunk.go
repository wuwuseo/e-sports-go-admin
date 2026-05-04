package service

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// ChunkInitRequest 分片上传初始化请求
type ChunkInitRequest struct {
	Filename    string `json:"filename" validate:"required"`
	FileSize    int64  `json:"file_size" validate:"required,min=1"`
	ChunkSize   int64  `json:"chunk_size" validate:"required,min=1"`
	TotalChunks int    `json:"total_chunks" validate:"required,min=1"`
	MimeType    string `json:"mime_type"`
	GroupID     *int   `json:"group_id"`
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
	Filename    string
	FileSize    int64
	ChunkSize   int64
	TotalChunks int
	MimeType    string
	GroupID     *int
	Uploaded    map[int]bool // 已上传的分片索引
	CreatedAt   time.Time
	mu          sync.Mutex
	deleted     atomic.Bool // LoadAndDelete 后标记，阻止并发 UploadChunk 写入
}

// chunkStore 管理所有分片上传会话
var chunkStore = sync.Map{} // upload_id → *chunkSession

// chunkBaseDir 分片存储的基础目录（相对执行目录）
const chunkBaseDir = "./data/storage/chunks"

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
	session := &chunkSession{
		UploadID:    uploadID,
		Filename:    req.Filename,
		FileSize:    req.FileSize,
		ChunkSize:   req.ChunkSize,
		TotalChunks: req.TotalChunks,
		MimeType:    req.MimeType,
		GroupID:     req.GroupID,
		Uploaded:    make(map[int]bool),
		CreatedAt:   time.Now(),
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

// UploadChunk 上传单个分片
func (s *AttachmentService) UploadChunk(ctx context.Context, uploadID string, chunkIndex int, fileHeader *multipart.FileHeader) (*ChunkUploadResponse, error) {
	// 查找会话
	val, ok := chunkStore.Load(uploadID)
	if !ok {
		return nil, fmt.Errorf("分片上传会话不存在")
	}
	session, ok := val.(*chunkSession)
	if !ok {
		return nil, fmt.Errorf("分片上传会话类型错误")
	}

	// 检查会话是否已被合并/取消标记删除
	if session.deleted.Load() {
		return nil, fmt.Errorf("分片上传会话已结束")
	}

	// 校验分片索引
	if chunkIndex < 0 || chunkIndex >= session.TotalChunks {
		return nil, fmt.Errorf("无效的分片索引: %d", chunkIndex)
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
		return nil, fmt.Errorf("分片上传会话已结束")
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
func (s *AttachmentService) MergeChunks(ctx context.Context, uploadID string, userID string) (*AttachmentResponse, error) {
	// 原子化获取并删除会话，防止并发重复合并
	val, ok := chunkStore.LoadAndDelete(uploadID)
	if !ok {
		return nil, fmt.Errorf("分片上传会话不存在或已合并")
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
			return nil, fmt.Errorf("分片 %d 尚未上传", i)
		}
	}
	session.mu.Unlock()

	return s.mergeChunks(ctx, session, chunkDir, userID)
}

// mergeChunks 合并分片的内部实现（session 必须已从 chunkStore 中移除）
// 使用 bytes.Buffer 逐片累积，避免临时文件 + os.ReadFile 的双重 I/O 和内存开销
func (s *AttachmentService) mergeChunks(ctx context.Context, session *chunkSession, chunkDir string, userID string) (*AttachmentResponse, error) {
	// 生成最终存储 key
	ext := getExt(session.Filename)
	now := time.Now()
	key := fmt.Sprintf("attachments/%d/%02d/%s.%s", now.Year(), now.Month(), uuid.New().String(), ext)

	// 逐片读取并累积到 buffer（避免 os.ReadFile 全量读入的双重内存问题）
	// 注意：filesystem.Set 接口要求 []byte，大文件仍有内存压力，后续可改造 filesystem 支持 io.Reader
	var buf bytes.Buffer
	var totalSize int64

	for i := 0; i < session.TotalChunks; i++ {
		chunkPath := filepath.Join(chunkDir, strconv.Itoa(i))
		chunkData, err := os.ReadFile(chunkPath)
		if err != nil {
			return nil, fmt.Errorf("读取分片 %d 失败: %w", i, err)
		}
		n, err := buf.Write(chunkData)
		if err != nil {
			return nil, fmt.Errorf("写入合并缓冲区失败: %w", err)
		}
		totalSize += int64(n)
	}

	// 存储到文件系统
	if err := s.filesystem.Set(key, buf.Bytes(), 0); err != nil {
		return nil, fmt.Errorf("存储文件失败: %w", err)
	}

	// 确定 MIME 类型
	mimeType := session.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// 创建数据库记录
	builder := s.client.Attachment.Create().
		SetName(session.Filename).
		SetOriginalName(session.Filename).
		SetPath(key).
		SetMimeType(mimeType).
		SetExtension(ext).
		SetSize(totalSize).
		SetNillableGroupID(session.GroupID).
		SetUploadedBy(userID)

	a, err := builder.Save(ctx)
	if err != nil {
		_ = s.filesystem.Delete(key)
		return nil, fmt.Errorf("保存附件记录失败: %w", err)
	}

	return s.toAttachmentResponse(a), nil
}

// UnifiedUpload 统一上传入口：自动识别简单上传/分片上传
// - TotalChunks == 0：简单上传，直接存储并创建附件
// - TotalChunks > 0 && UploadID == ""：首次分片，使用确定性 upload_id 实现幂等初始化
// - TotalChunks > 0 && UploadID != ""：后续分片，追加到已有会话
// - ChunkIndex == TotalChunks - 1：最后一片，自动合并并返回附件信息
func (s *AttachmentService) UnifiedUpload(ctx context.Context, req *UnifiedUploadRequest) (*UnifiedUploadResponse, error) {
	// 简单上传
	if req.TotalChunks == 0 {
		attachment, err := s.Upload(ctx, req.FileHeader, req.GroupID, req.UserID)
		if err != nil {
			return nil, err
		}
		return &UnifiedUploadResponse{Attachment: attachment}, nil
	}

	// 分片上传
	uploadID := req.UploadID
	isFirstChunk := uploadID == ""

	// 跟踪当前请求是否赢得了 LoadOrStore 竞态（只有赢家负责失败回滚清理）
	var initWon bool

	// 首次分片：使用确定性 upload_id + LoadOrStore 实现原子化初始化
	if isFirstChunk {
		uploadID = generateDeterministicUploadID(req.Filename, req.FileSize, req.UserID)

		// 预创建会话对象
		session := &chunkSession{
			UploadID:    uploadID,
			Filename:    req.Filename,
			FileSize:    req.FileSize,
			ChunkSize:   req.ChunkSize,
			TotalChunks: req.TotalChunks,
			MimeType:    req.MimeType,
			GroupID:     req.GroupID,
			Uploaded:    make(map[int]bool),
			CreatedAt:   time.Now(),
		}

		// 原子化：LoadOrStore 保证仅一个协程执行 I/O 初始化（修复 TOCTOU）
		if _, loaded := chunkStore.LoadOrStore(uploadID, session); !loaded {
			initWon = true

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
	_, err := s.UploadChunk(ctx, uploadID, req.ChunkIndex, req.FileHeader)
	if err != nil {
		// 仅当本请求赢得了初始化竞态时才清理孤儿会话（修复回滚误删并发会话 #3）
		if isFirstChunk && initWon {
			chunkStore.Delete(uploadID)
			_ = os.RemoveAll(filepath.Join(chunkBaseDir, uploadID))
		}
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

// GetChunkStatus 获取分片上传状态
func (s *AttachmentService) GetChunkStatus(uploadID string) (*ChunkStatusResponse, error) {
	val, ok := chunkStore.Load(uploadID)
	if !ok {
		return nil, fmt.Errorf("分片上传会话不存在")
	}
	session, ok := val.(*chunkSession)
	if !ok {
		return nil, fmt.Errorf("分片上传会话类型错误")
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
// 先原子化移除会话（阻止并发 UploadChunk），再清理磁盘目录
func (s *AttachmentService) CancelChunkUpload(uploadID string) error {
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

// 确保 AttachmentService 实现了分片上传接口（编译期检查）
var _ ChunkUploadService = (*AttachmentService)(nil)

// ChunkUploadService 分片上传服务接口
type ChunkUploadService interface {
	// UnifiedUpload 统一上传入口（简单上传 + 分片上传）
	UnifiedUpload(ctx context.Context, req *UnifiedUploadRequest) (*UnifiedUploadResponse, error)
	// 分片上传状态与取消
	GetChunkStatus(uploadID string) (*ChunkStatusResponse, error)
	CancelChunkUpload(uploadID string) error
}
