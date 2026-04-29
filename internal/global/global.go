package global

import (
	"github.com/wuwuseo/cmf/bootstrap"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"

	"go-admin/internal/cron"
	"go-admin/internal/ent"
)

var (
	ORM             *ent.Client
	Bootstrap       *bootstrap.Bootstrap
	EnforcerManager *cmfCasbin.EnforcerManager
	CronManager     *cron.CronManager
)
