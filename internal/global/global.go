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

	// QueueRuntime 原子发布同一后端的投递、执行和管理能力。
	QueueRuntime atomic.Pointer[queue.Runtime]
)
