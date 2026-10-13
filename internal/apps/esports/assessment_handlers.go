package esports

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// ── 管理端：权重配置（§7.2） ──

func adminAssessmentRules(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listAssessmentRules(c.Context(), global.ORM, c.Query("assessment_type"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func setAssessmentRulesHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in struct {
		AssessmentType string                  `json:"assessment_type"`
		Weights        []assessmentWeightInput `json:"weights"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	items, err := setAssessmentRules(c.Context(), global.ORM, in.AssessmentType, in.Weights)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// ── 管理端：考核任务（§7.1/§7.2/§7.4） ──

func adminAssessments(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listAssessments(c.Context(), global.ORM, queryInt(c, "player_id"),
		c.Query("status"), c.Query("assessment_type"), queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createAssessmentHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in assessmentInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createAssessment(c.Context(), global.ORM, operator.ID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func adminAssessmentDetail(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "考核 ID 无效")
	}
	detail, err := assessmentDetail(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, detail)
}

func assignAssessmentHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "考核 ID 无效")
	}
	var in struct {
		ExaminerAdminID int        `json:"examiner_admin_id"`
		ScheduledAt     *time.Time `json:"scheduled_at"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := assignAssessment(c.Context(), global.ORM, id, in.ExaminerAdminID, in.ScheduledAt, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func scoreAssessmentHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "考核 ID 无效")
	}
	var in struct {
		Scores []assessmentScoreInput `json:"scores"`
		Note   string                 `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := scoreAssessment(c.Context(), global.ORM, id, operator.ID, in.Scores, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func reviewAppealHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "考核 ID 无效")
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	decision := strings.TrimSpace(in.Decision)
	if decision != "approve" && decision != "reject" {
		return fail(c, fiber.StatusBadRequest, "复核结论无效")
	}
	item, err := reviewAppeal(c.Context(), global.ORM, id, operator.ID, decision == "approve", in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// ── 管理端：客户评价与综合评分（§7.5） ──

func adminPlayerReviews(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listReviews(c.Context(), global.ORM, queryInt(c, "player_id"),
		queryInt(c, "order_id"), c.Query("status"), queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func recordReviewHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in reviewInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := recordReviewByAdmin(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func setReviewStatusHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "评价 ID 无效")
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := setReviewStatus(c.Context(), global.ORM, id, in.Status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func adminPlayerRating(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "陪玩 ID 无效")
	}
	view, err := playerRatingSummary(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// ── 陪玩端自助：我的考核与申诉（§7.4） ──

func playerAssessmentsHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	player, err := currentPlayer(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listAssessments(c.Context(), global.ORM, player.ID, "", "", 100)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func playerAppealHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	player, err := currentPlayer(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "考核 ID 无效")
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	appeal, err := createAppeal(c.Context(), global.ORM, player.ID, id, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, appeal)
}

// ── 客户自助：已完成订单的服务评价（§7.5） ──

func userSubmitReviewHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := strconv.Atoi(c.Params("id"))
	if err != nil || orderID < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	var in struct {
		PlayerID int    `json:"player_id"`
		Rating   int    `json:"rating"`
		Comment  string `json:"comment"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	review, err := submitCustomerReview(c.Context(), global.ORM, customer.ID, orderID, in.PlayerID, in.Rating, in.Comment)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, review)
}
