package cron

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/cronjob"

	robfigcron "github.com/robfig/cron/v3"
	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"
)

// CronManager 定时任务管理器
type CronManager struct {
	cron      *robfigcron.Cron
	client    *ent.Client
	logger    *JobLogger
	executors map[string]Executor
	jobs      sync.Map // jobID(int) -> robfigcron.EntryID
}

// NewCronManager 创建定时任务管理器
func NewCronManager(client *ent.Client, logDir string, retention time.Duration) *CronManager {
	return &CronManager{
		cron:   robfigcron.New(robfigcron.WithSeconds()),
		client: client,
		logger: NewJobLogger(logDir, retention),
		executors: map[string]Executor{
			"shell": &CommandExecutor{},
			"curl":  &CurlExecutor{},
			"task":  &TaskExecutor{},
		},
	}
}

// Start 启动定时任务管理器
func (m *CronManager) Start() error {
	// 从数据库加载所有 enabled=true 的 CronJob
	jobs, err := m.client.CronJob.Query().
		Where(cronjob.EnabledEQ(true)).
		All(context.Background())
	if err != nil {
		return fmt.Errorf("加载定时任务失败: %w", err)
	}

	for _, job := range jobs {
		if err := m.AddJob(job); err != nil {
			log.Error("注册定时任务失败",
				zap.Int("job_id", job.ID),
				zap.String("job_name", job.Name),
				zap.Error(err),
			)
		}
	}

	// 注册内部日志清理任务（每天凌晨2点执行）
	_, err = m.cron.AddFunc("0 0 2 * * *", func() {
		if cleanErr := m.logger.CleanOldLogs(); cleanErr != nil {
			log.Error("清理过期日志失败", zap.Error(cleanErr))
		} else {
			log.Info("过期日志清理完成")
		}
	})
	if err != nil {
		log.Error("注册日志清理任务失败", zap.Error(err))
	}

	// 启动调度器
	m.cron.Start()
	log.Info("定时任务管理器已启动", zap.Int("loaded_jobs", len(jobs)))

	return nil
}

// Stop 优雅停止定时任务管理器
func (m *CronManager) Stop() {
	ctx := m.cron.Stop()
	<-ctx.Done()
}

// AddJob 注册一个定时任务到调度器
func (m *CronManager) AddJob(job *ent.CronJob) error {
	executor, ok := m.executors[string(job.Type)]
	if !ok {
		return fmt.Errorf("未知的任务类型: %s", job.Type)
	}

	jobID := job.ID
	jobName := job.Name
	command := job.Command
	expression := job.Expression

	entryID, err := m.cron.AddFunc(expression, func() {
		m.executeJob(jobID, jobName, command, executor)
	})
	if err != nil {
		return fmt.Errorf("注册cron任务失败: %w", err)
	}

	m.jobs.Store(jobID, entryID)

	// 计算并更新下次执行时间
	m.updateNextRunAt(jobID, entryID)

	log.Info("定时任务已注册",
		zap.Int("job_id", jobID),
		zap.String("job_name", jobName),
		zap.String("expression", expression),
	)

	return nil
}

// RemoveJob 从调度器中移除一个定时任务
func (m *CronManager) RemoveJob(jobID int) {
	if entryIDVal, ok := m.jobs.Load(jobID); ok {
		m.cron.Remove(entryIDVal.(robfigcron.EntryID))
		m.jobs.Delete(jobID)
		log.Info("定时任务已移除", zap.Int("job_id", jobID))
	}
}

// RunNow 立即执行一个定时任务
func (m *CronManager) RunNow(jobID int) error {
	job, err := m.client.CronJob.Query().
		Where(cronjob.IDEQ(jobID)).
		Only(context.Background())
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("定时任务不存在")
		}
		return fmt.Errorf("查询定时任务失败: %w", err)
	}

	executor, ok := m.executors[string(job.Type)]
	if !ok {
		return fmt.Errorf("未知的任务类型: %s", job.Type)
	}

	go m.executeJob(job.ID, job.Name, job.Command, executor)

	return nil
}

// GetJobLogger 返回内部的 JobLogger
func (m *CronManager) GetJobLogger() *JobLogger {
	return m.logger
}

// executeJob 执行任务的核心逻辑
func (m *CronManager) executeJob(jobID int, jobName string, command string, executor Executor) {
	// 创建日志文件
	logFile, _, err := m.logger.CreateLogFile(jobID)
	if err != nil {
		log.Error("创建任务日志文件失败",
			zap.Int("job_id", jobID),
			zap.Error(err),
		)
		return
	}
	defer logFile.Close()

	startTime := time.Now()
	fmt.Fprintf(logFile, "========================================\n")
	fmt.Fprintf(logFile, "任务: %s (ID: %d)\n", jobName, jobID)
	fmt.Fprintf(logFile, "开始时间: %s\n", startTime.Format(time.DateTime))
	fmt.Fprintf(logFile, "========================================\n\n")

	// 执行任务
	ctx := context.Background()
	execErr := executor.Execute(ctx, command, logFile)

	endTime := time.Now()
	duration := endTime.Sub(startTime)

	fmt.Fprintf(logFile, "\n========================================\n")
	fmt.Fprintf(logFile, "结束时间: %s\n", endTime.Format(time.DateTime))
	fmt.Fprintf(logFile, "耗时: %s\n", duration.String())
	if execErr != nil {
		fmt.Fprintf(logFile, "状态: 失败\n")
		fmt.Fprintf(logFile, "错误: %s\n", execErr.Error())
		log.Error("定时任务执行失败",
			zap.Int("job_id", jobID),
			zap.String("job_name", jobName),
			zap.Duration("duration", duration),
			zap.Error(execErr),
		)
	} else {
		fmt.Fprintf(logFile, "状态: 成功\n")
		log.Info("定时任务执行成功",
			zap.Int("job_id", jobID),
			zap.String("job_name", jobName),
			zap.Duration("duration", duration),
		)
	}
	fmt.Fprintf(logFile, "========================================\n")

	// 更新 DB last_run_at
	now := time.Now()
	updateBuilder := m.client.CronJob.UpdateOneID(jobID).
		SetLastRunAt(now)

	// 更新 next_run_at
	if entryIDVal, ok := m.jobs.Load(jobID); ok {
		entry := m.cron.Entry(entryIDVal.(robfigcron.EntryID))
		if !entry.Next.IsZero() {
			updateBuilder.SetNextRunAt(entry.Next)
		}
	}

	if _, dbErr := updateBuilder.Save(context.Background()); dbErr != nil {
		log.Error("更新定时任务执行时间失败",
			zap.Int("job_id", jobID),
			zap.Error(dbErr),
		)
	}
}

// updateNextRunAt 计算并更新下次执行时间
func (m *CronManager) updateNextRunAt(jobID int, entryID robfigcron.EntryID) {
	entry := m.cron.Entry(entryID)
	if !entry.Next.IsZero() {
		if _, err := m.client.CronJob.UpdateOneID(jobID).
			SetNextRunAt(entry.Next).
			Save(context.Background()); err != nil {
			log.Error("更新下次执行时间失败",
				zap.Int("job_id", jobID),
				zap.Error(err),
			)
		}
	}
}
