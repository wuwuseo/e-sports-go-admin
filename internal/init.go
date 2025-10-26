package internal

import (
	"context"

	entsql "entgo.io/ent/dialect/sql"
	fiberlog "github.com/gofiber/fiber/v2/log"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/orm"
	"github.com/wuwuseo/go-admin/internal/ent"
	"github.com/wuwuseo/go-admin/internal/global"
)

func Init(config *config.Config) error {
	err := initDatabase(config)
	if err != nil {
		return err
	}
	return nil
}

func initDatabase(config *config.Config) error {
	driver := orm.GetDatabaseConfig([]string{config.Database.Default}, config).Driver
	fiberlog.Infof("using database driver: %s", driver)
	db, err := orm.GetSqlDb(driver, orm.GetDatabaseSourceDns(config, config.Database.Default))
	if err != nil {
		return err
	}
	drv := entsql.OpenDB(driver, db)
	client := ent.NewClient(ent.Driver(drv))
	if schemaErr := client.Schema.Create(context.Background()); schemaErr != nil {
		fiberlog.Fatalf("failed creating schema resources: %v", schemaErr)
	}
	global.ORM = client
	return nil
}
