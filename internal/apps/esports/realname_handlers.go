package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsrealnameattempt"
	"go-admin/internal/ent/esportsrealnameverification"
	"go-admin/internal/global"
)

// 统一实名 HTTP 接口：
//   - 用户端（客户 / 陪玩）查询与提交，主体身份由登录态与路由决定，不接受请求参数指定；
//   - 渠道异步回调为公开路由，先验签再落定；
//   - 管理端列表 / 详情 / 人工审核 / 代提交。

// userRealNameStatusHandler 客户查询本人实名状态与可用渠道。
func userRealNameStatusHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	view, err := realNameStatusFor(c.Context(), global.ORM, realnameSubjectTypeCustomer, customer.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// userRealNameSubmitHandler 客户提交实名认证。
func userRealNameSubmitHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in realNameSubmitInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := submitRealName(c.Context(), global.ORM, realnameSubjectTypeCustomer, customer.ID, 0, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// playerRealNameStatusHandler 陪玩查询本人实名状态与可用渠道。
func playerRealNameStatusHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	view, err := realNameStatusFor(c.Context(), global.ORM, realnameSubjectTypePlayer, player.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// playerRealNameSubmitHandler 陪玩提交实名认证。
func playerRealNameSubmitHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in realNameSubmitInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := submitRealName(c.Context(), global.ORM, realnameSubjectTypePlayer, player.ID, 0, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// realNameCallbackHandler 渠道异步核验回调（公开路由）。
// 签名口径：X-Signature = hex(HMAC-SHA256(app_secret, app_id\ntimestamp\nnonce\nbody))。
// 先验签再落定；重复回调按幂等成功返回。
func realNameCallbackHandler(c fiber.Ctx) error {
	channel := strings.TrimSpace(c.Params("channel"))
	body := c.Body()
	var in struct {
		AttemptRef string `json:"attempt_ref"`
		Passed     bool   `json:"passed"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	now := time.Now()
	if err := verifyRealNameCallbackSignature(c.Context(), global.ORM, channel,
		c.Get("X-App-Id"), c.Get("X-Timestamp"), c.Get("X-Nonce"), c.Get("X-Signature"), body, now); err != nil {
		return businessError(c, err)
	}
	if err := settleRealNameAttempt(c.Context(), global.ORM, channel, in.AttemptRef, in.Passed, strings.TrimSpace(in.Message), now); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"channel": channel, "attempt_ref": in.AttemptRef})
}

// adminRealNames 管理端实名档案列表：按主体类型 / 状态 / 渠道筛选，按更新时间倒序。
func adminRealNames(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	q := global.ORM.EsportsRealNameVerification.Query()
	if subjectType := strings.TrimSpace(c.Query("subject_type")); subjectType != "" {
		q = q.Where(esportsrealnameverification.SubjectTypeEQ(esportsrealnameverification.SubjectType(subjectType)))
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		q = q.Where(esportsrealnameverification.StatusEQ(esportsrealnameverification.Status(status)))
	}
	if channel := strings.TrimSpace(c.Query("channel")); channel != "" {
		q = q.Where(esportsrealnameverification.ChannelEQ(esportsrealnameverification.Channel(channel)))
	}
	records, err := q.Order(ent.Desc(esportsrealnameverification.FieldUpdatedAt)).
		Limit(queryLimit(c, 100)).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	names, err := realNameSubjectNames(c.Context(), global.ORM, records)
	if err != nil {
		return businessError(c, err)
	}
	views := make([]fiber.Map, 0, len(records))
	for _, row := range records {
		views = append(views, realNameRecordView(row, realNameSubjectName(names, row)))
	}
	return reply(c, views)
}

// adminRealNameDetail 管理端实名档案详情：含该主体最近 20 条核验流水。
func adminRealNameDetail(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "实名记录")
	if err != nil {
		return businessError(c, err)
	}
	record, err := global.ORM.EsportsRealNameVerification.Get(c.Context(), id)
	if err != nil {
		return businessError(c, err)
	}
	attempts, err := global.ORM.EsportsRealNameAttempt.Query().Where(
		esportsrealnameattempt.SubjectTypeEQ(esportsrealnameattempt.SubjectType(record.SubjectType)),
		esportsrealnameattempt.SubjectIDEQ(record.SubjectID),
	).Order(ent.Desc(esportsrealnameattempt.FieldCreatedAt)).Limit(20).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	names, err := realNameSubjectNames(c.Context(), global.ORM, []*ent.EsportsRealNameVerification{record})
	if err != nil {
		return businessError(c, err)
	}
	view := realNameRecordView(record, realNameSubjectName(names, record))
	view["attempts"] = attempts
	return reply(c, view)
}

// adminReviewRealNameHandler 管理端人工审核：通过置 verified，驳回须填原因。
func adminReviewRealNameHandler(c fiber.Ctx) error {
	reviewer, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "实名记录")
	if err != nil {
		return businessError(c, err)
	}
	var in realNameReviewInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	record, err := reviewRealName(c.Context(), global.ORM, reviewer.ID, id, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, record)
}

// adminSubmitRealNameHandler 管理端代提交：body 携带主体类型与主体 ID，
// 渠道 / 方式缺省按人工审核（线下收证件照的场景）。
func adminSubmitRealNameHandler(c fiber.Ctx) error {
	reviewer, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		SubjectType string `json:"subject_type"`
		SubjectID   int    `json:"subject_id"`
		RealName    string `json:"real_name"`
		IDNumber    string `json:"id_number"`
		Channel     string `json:"channel"`
		Method      string `json:"method"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	in.SubjectType = strings.TrimSpace(in.SubjectType)
	if !validRealNameSubjectType(in.SubjectType) || in.SubjectID < 1 {
		return fail(c, fiber.StatusBadRequest, "主体类型或主体 ID 无效")
	}
	if err := ensureRealNameSubjectExists(c.Context(), global.ORM, in.SubjectType, in.SubjectID); err != nil {
		return businessError(c, err)
	}
	if strings.TrimSpace(in.Channel) == "" {
		in.Channel = realNameChannelManual
	}
	if strings.TrimSpace(in.Method) == "" {
		in.Method = realNameMethodManual
	}
	view, err := submitRealName(c.Context(), global.ORM, in.SubjectType, in.SubjectID, reviewer.ID, realNameSubmitInput{
		RealName: in.RealName, IDNumber: in.IDNumber, Channel: in.Channel, Method: in.Method,
	}, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// ensureRealNameSubjectExists 代提交前校验主体存在，避免写出悬空实名档案。
func ensureRealNameSubjectExists(ctx context.Context, db *ent.Client, subjectType string, subjectID int) error {
	switch subjectType {
	case realnameSubjectTypeCustomer:
		if _, err := db.EsportsCustomer.Get(ctx, subjectID); err != nil {
			if ent.IsNotFound(err) {
				return fmt.Errorf("%w：客户不存在", errInvalidInput)
			}
			return err
		}
	case realnameSubjectTypePlayer:
		if _, err := db.EsportsPlayer.Get(ctx, subjectID); err != nil {
			if ent.IsNotFound(err) {
				return fmt.Errorf("%w：陪玩不存在", errInvalidInput)
			}
			return err
		}
	default:
		return errInvalidInput
	}
	return nil
}

// realNameSubjectNames 批量取主体显示名（键为 主体类型#ID），供列表与详情展示。
func realNameSubjectNames(ctx context.Context, db *ent.Client, records []*ent.EsportsRealNameVerification) (map[string]string, error) {
	customerIDs := make([]int, 0, len(records))
	playerIDs := make([]int, 0, len(records))
	for _, row := range records {
		switch row.SubjectType {
		case esportsrealnameverification.SubjectTypeCustomer:
			customerIDs = append(customerIDs, row.SubjectID)
		case esportsrealnameverification.SubjectTypePlayer:
			playerIDs = append(playerIDs, row.SubjectID)
		}
	}
	names := make(map[string]string, len(records))
	if len(customerIDs) > 0 {
		rows, err := db.EsportsCustomer.Query().Where(esportscustomer.IDIn(customerIDs...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			names[realNameSubjectKey(realnameSubjectTypeCustomer, row.ID)] = row.DisplayName
		}
	}
	if len(playerIDs) > 0 {
		rows, err := db.EsportsPlayer.Query().Where(esportsplayer.IDIn(playerIDs...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			names[realNameSubjectKey(realnameSubjectTypePlayer, row.ID)] = row.DisplayName
		}
	}
	return names, nil
}

func realNameSubjectKey(subjectType string, subjectID int) string {
	return subjectType + "#" + strconv.Itoa(subjectID)
}

// realNameSubjectName 取主体显示名；未建档或昵称为空回落 #ID，避免列表出现空白行。
func realNameSubjectName(names map[string]string, row *ent.EsportsRealNameVerification) string {
	name := strings.TrimSpace(names[realNameSubjectKey(string(row.SubjectType), row.SubjectID)])
	if name == "" {
		return "#" + strconv.Itoa(row.SubjectID)
	}
	return name
}

// realNameRecordView 档案出参：证件号只给脱敏值，原号从不回显。
func realNameRecordView(row *ent.EsportsRealNameVerification, subjectName string) fiber.Map {
	return fiber.Map{
		"id":                   row.ID,
		"subject_type":         row.SubjectType,
		"subject_id":           row.SubjectID,
		"subject_name":         subjectName,
		"real_name":            row.RealName,
		"id_number_masked":     row.IDNumberMasked,
		"channel":              row.Channel,
		"method":               row.Method,
		"status":               row.Status,
		"fail_reason":          row.FailReason,
		"provider_ref":         row.ProviderRef,
		"requested_by":         row.RequestedBy,
		"reviewed_by_admin_id": row.ReviewedByAdminID,
		"verified_at":          row.VerifiedAt,
		"created_at":           row.CreatedAt,
		"updated_at":           row.UpdatedAt,
	}
}
