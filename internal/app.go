package internal

import (
	"github.com/wuwuseo/cmf/cache"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"
	"github.com/wuwuseo/cmf/log"
	"github.com/wuwuseo/cmf/orm"
	"github.com/wuwuseo/cmf/sse"
	"github.com/wuwuseo/cmf/validate"

	"go-admin/internal/ent"
)

// App 应用依赖容器
type App struct {
	Config          *config.Config
	Logger          log.Logger
	DB              *orm.DBManager
	Validator       *validate.Validator
	Cache           *cache.Cache[[]byte]
	EntClient       *ent.Client
	EnforcerManager *cmfCasbin.EnforcerManager
	Filesystem      *filesystem.Filesystem
	Hub             *sse.Hub
	TicketStore     *sse.TicketStore
}
