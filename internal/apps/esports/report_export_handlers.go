package esports

import (
	"os"
	"strings"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsreportexport"
	"go-admin/internal/global"
)

// validateReportExportAccess 校验导出权限与筛选边界：
// finance 报表仅超级管理员可见，且其资金不具备逐订单渠道归属，拒绝渠道/入口筛选。
func validateReportExportAccess(reportType string, f reportFilter, isSuper bool) error {
	if reportType == "finance" && !isSuper {
		return errForbidden
	}
	if reportType == "finance" && (f.ChannelSource != "" || f.EntryPoint != "") {
		return errInvalidInput
	}
	return nil
}

// adminReportExport 导出报表 CSV（§17 导出与审计）。
// 建议接线：GET /admin/reports/:type/export（管理端 Auth），支持 format=csv。
// 权限与数据边界与报表接口一致：非超级管理员收敛本人、finance 仅超级管理员。
func adminReportExport(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	reportType := strings.TrimSpace(c.Params("type"))
	if !validReportType(reportType) {
		return businessError(c, errInvalidInput)
	}
	if format := strings.TrimSpace(c.Query("format")); format != "" && format != "csv" {
		return businessError(c, errInvalidInput)
	}
	filter, err := parseReportFilter(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if err := validateReportExportAccess(reportType, filter, isSuper); err != nil {
		return businessError(c, err)
	}
	filter = scopeReportFilter(filter, operator.ID, isSuper)
	handlerAdmin := 0
	if reportType == "aftersale_quality" && !isSuper {
		handlerAdmin = operator.ID
	}
	fileName := reportExportFileName(reportType, filter.Start, filter.End)
	// 先流式落临时文件：保证审计记录的「行数 / 摘要」与最终返回的字节一致，
	// 生成失败则不落审计，避免幽灵记录。
	tmp, err := os.CreateTemp("", "esports-report-export-*.csv")
	if err != nil {
		return businessError(c, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	rowCount, err := exportReportCSV(c.Context(), global.ORM, reportType, filter, handlerAdmin, tmp)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return businessError(c, err)
	}
	checksum, err := fileSHA256(tmpName)
	if err != nil {
		return businessError(c, err)
	}
	if _, err := recordReportExport(c.Context(), global.ORM, operator.ID, adminDisplayName(operator),
		reportType, filter, handlerAdmin, rowCount, fileName, checksum); err != nil {
		return businessError(c, err)
	}
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+fileName+`"`)
	return c.SendFile(tmpName)
}

// adminReportExports 查询导出历史（统一入口）。
// 建议接线：GET /admin/report-exports（管理端 Auth），非超级管理员仅见本人记录。
func adminReportExports(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listReportExports(c, global.ORM, operator.ID, isSuper, strings.TrimSpace(c.Query("type")))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminReportTypeExports 查询某报表类型的导出历史。
// 建议接线：GET /admin/reports/:type/exports（管理端 Auth）。
func adminReportTypeExports(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	reportType := strings.TrimSpace(c.Params("type"))
	if !validReportType(reportType) {
		return businessError(c, errInvalidInput)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listReportExports(c, global.ORM, operator.ID, isSuper, reportType)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func listReportExports(c fiber.Ctx, db *ent.Client, adminID int, isSuper bool, reportType string) ([]*ent.EsportsReportExport, error) {
	query := db.EsportsReportExport.Query()
	if reportType != "" {
		query.Where(esportsreportexport.ReportTypeEQ(reportType))
	}
	if !isSuper {
		query.Where(esportsreportexport.AdminIDEQ(adminID))
	}
	return query.Order(ent.Desc(esportsreportexport.FieldCreatedAt), ent.Desc(esportsreportexport.FieldID)).
		Limit(queryLimit(c, 100)).All(c.Context())
}
