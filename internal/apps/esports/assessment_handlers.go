package esports

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// ── 管理端：权重配置 ──

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

// ── 管理端：考核任务 ──

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

// ── 管理端：客户评价与综合评分 ──

func adminPlayerReviews(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := orderIDByNo(c.Context(), global.ORM, c.Query("order_no"))
	if err != nil {
		return businessError(c, err)
	}
	items, err := listReviews(c.Context(), global.ORM, queryInt(c, "player_id"),
		orderID, c.Query("status"), queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	views, err := playerReviewAdminViewsOf(c.Context(), global.ORM, items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
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

// ── 陪玩端自助：我的考核与申诉 ──

// playerAssessmentsHandler 陪玩端「我的考核」：返回主表 + 逐维度得分与权重（评级依据），
// 否则陪玩只能看到等级与结论、看不到评级依据。
func playerAssessmentsHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	player, err := currentPlayer(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerAssessmentDetails(c.Context(), global.ORM, player.ID, 100)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// playerApplyUpgradeAssessmentHandler 陪玩端自助申请升级考核。
// 需在 app.go 接线：{Method: "POST", Path: "/user/player/assessments/upgrade-apply", Auth: cmfapp.AuthUser, Handler: playerApplyUpgradeAssessmentHandler}
// 陪玩身份由 AuthUser + requestPlayer 保证；按仓库约定 AuthUser 自助路由不需要 PermAPI（PermAPI 只声明管理端 /admin 路径）。
// 目标档位由服务端按指定分区的当前等级推导，客户端不能指定等级。
func playerApplyUpgradeAssessmentHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Game string `json:"game"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := applyPlayerUpgradeAssessment(c.Context(), global.ORM, player.ID, in.Game, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
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

// ── 客户自助：我的评价列表 ──

// userReviewsHandler 客户自助「我的评价」列表：只返回**当前登录客户本人**的评价，
// 客户身份由 customerForVerifiedUser（AuthUser + 已绑定身份）解析，不接受客户端传 customer_id。
// 分页沿用同包既有列表风格（?limit=，默认 100、上限 200）；可选 ?order_id= 只看某单。
// 需在 app.go 接线：{Method: "GET", Path: "/user/reviews", Auth: cmfapp.AuthUser, Handler: userReviewsHandler}
// AuthUser 自助路由按仓库惯例不声明 PermAPI。
func userReviewsHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listCustomerReviews(c.Context(), global.ORM, customer.ID,
		queryInt(c, "order_id"), queryLimit(c, 100), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// ── 客户自助：已完成订单的服务评价 ──

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

// ── 客户 / 陪玩自助：评价申诉与管理员复核 ──

// userReviewAppealHandler 客户或陪玩对评价发起申诉。
// 需在 app.go 接线：{Method: "POST", Path: "/user/reviews/:id/appeal", Auth: cmfapp.AuthUser, Handler: userReviewAppealHandler}
// AuthUser 自助路由按仓库约定不需要 PermAPI。
func userReviewAppealHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	appellantType, appellantID, err := reviewAppealAppellant(c.Context(), uid)
	if err != nil {
		return businessError(c, err)
	}
	reviewID, err := strconv.Atoi(c.Params("id"))
	if err != nil || reviewID < 1 {
		return fail(c, fiber.StatusBadRequest, "评价 ID 无效")
	}
	var in struct {
		Reason      string `json:"reason"`
		EvidenceRef string `json:"evidence_ref"`
	}
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	appeal, err := createReviewAppeal(c.Context(), global.ORM, reviewID, appellantType, appellantID,
		in.Reason, in.EvidenceRef, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, appeal)
}

// adminReviewAppeals 管理端评价申诉复核队列。
// 需在 app.go 接线 + PermAPI（码前缀 esports:player_reviews:*）：
//
//	GET  /admin/player-review-appeals            → adminReviewAppeals
//	POST /admin/player-review-appeals/:id/review → reviewReviewAppealHandler
func adminReviewAppeals(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listReviewAppeals(c.Context(), global.ORM, c.Query("status"), queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	views, err := reviewAppealViewsOf(c.Context(), global.ORM, items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func reviewReviewAppealHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	appealID, err := strconv.Atoi(c.Params("id"))
	if err != nil || appealID < 1 {
		return fail(c, fiber.StatusBadRequest, "申诉 ID 无效")
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	decision := strings.TrimSpace(in.Decision)
	if decision != "approve" && decision != "reject" {
		return fail(c, fiber.StatusBadRequest, "复核结论无效")
	}
	review, err := reviewReviewAppeal(c.Context(), global.ORM, appealID, operator.ID,
		decision == "approve", in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, review)
}
