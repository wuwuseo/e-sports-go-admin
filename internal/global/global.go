package global

import (
	"github.com/wuwuseo/cmf/bootstrap"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/queue"
	"sync/atomic"

	"go-admin/internal/cron"
	"go-admin/internal/ent"
)

var (
	ORM             *ent.Client
	Bootstrap       *bootstrap.Bootstrap
	EnforcerManager *cmfCasbin.EnforcerManager
	CronManager     *cron.CronManager

	// QueueClient 任务队列投递端（业务代码经此入队）
	QueueClient atomic.Pointer[queue.Client]
	// QueueServer 任务队列执行端（常驻 worker，进程内注册处理器）
	QueueServer atomic.Pointer[queue.Server]
	// QueueInspector 任务队列管理端（驱动管理后台 API）
	QueueInspector atomic.Pointer[queue.Inspector]
)
