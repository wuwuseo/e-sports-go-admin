package global

import (
	"github.com/wuwuseo/cmf/bootstrap"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/queue"
	"github.com/wuwuseo/cmf/sse"
	"sync/atomic"

	"go-admin/internal/cron"
	"go-admin/internal/ent"
)

var (
	ORM             *ent.Client
	Bootstrap       *bootstrap.Bootstrap
	EnforcerManager *cmfCasbin.EnforcerManager
	CronManager     *cron.CronManager

	// Hub 全局 SSE 连接管理器，供 HTTP 中间件之外的路径（定时任务）广播变更。
	Hub *sse.Hub

	// QueueRuntime 原子发布同一后端的投递、执行和管理能力。
	QueueRuntime atomic.Pointer[queue.Runtime]
)
