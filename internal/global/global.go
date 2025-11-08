package global

import (
	"go-admin/internal/ent"

	"github.com/wuwuseo/cmf/bootstrap"
)

var (
	ORM       *ent.Client
	Bootstrap *bootstrap.Bootstrap
)
