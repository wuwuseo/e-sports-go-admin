package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsagecheck"
	"go-admin/internal/global"
)

func createAgeCheck(c fiber.Ctx) error {
	staff, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID, err := strconv.Atoi(c.Params("id"))
	if err != nil || customerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	var input ageCheckInput
	if err := c.Bind().Body(&input); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	check, err := submitAgeCheck(c.Context(), global.ORM, staff, customerID, input)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"id": check.ID, "status": check.Status})
}

func pendingAgeChecks(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	checks, err := global.ORM.EsportsAgeCheck.Query().Where(
		esportsagecheck.StatusEQ(esportsagecheck.StatusPending),
	).Order(ent.Asc(esportsagecheck.FieldCreatedAt)).Limit(100).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	result := make([]fiber.Map, 0, len(checks))
	for _, check := range checks {
		customer, err := global.ORM.EsportsCustomer.Get(c.Context(), check.CustomerID)
		if err != nil {
			return businessError(c, err)
		}
		result = append(result, fiber.Map{
			"id": check.ID, "customer_id": check.CustomerID,
			"customer_name": customer.DisplayName, "customer_version": customer.Version,
			"requested_by_admin_id":  check.RequestedByAdminID,
			"claimed_age_status":     check.ClaimedAgeStatus,
			"evidence_attachment_id": check.EvidenceAttachmentID,
			"created_at":             check.CreatedAt,
		})
	}
	return reply(c, result)
}

func reviewAgeCheckRequest(c fiber.Ctx) error {
	reviewer, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	checkID, err := strconv.Atoi(c.Params("id"))
	if err != nil || checkID < 1 {
		return fail(c, fiber.StatusBadRequest, "核验 ID 无效")
	}
	var input ageReviewInput
	if err := c.Bind().Body(&input); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	check, err := reviewAgeCheck(c.Context(), global.ORM, reviewer, checkID, input, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"id": check.ID, "status": check.Status, "reviewed_at": check.ReviewedAt})
}
