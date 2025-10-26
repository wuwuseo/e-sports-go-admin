package internal

import (
	"github.com/wuwuseo/go-admin/internal/global"
)

func Cleanup() error {
	// 清理资源
	if global.ORM != nil {
		// 关闭数据库连接
		if err := global.ORM.Close(); err != nil {
			return err
		}
		// 将客户端设置为 nil，防止重复关闭
		global.ORM = nil
	}

	return nil
}
