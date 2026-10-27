package esports

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayeridentity"
	"go-admin/internal/ent/esportsrealnameverification"
	"go-admin/internal/global"
)

// 陪玩实名认证兼容门面。
//
// 2026-10 起陪玩实名并入统一实名（realname.go）：
// 存储读写真源改为 EsportsRealNameVerification（subject_type=player，channel/method=manual），
// 本文件只保留旧的入参 / 出参形状与四个旧路由 handler，内部全部转发统一实现。
// 旧表 esports_player_identities 保留只读，仅迁移时被 realname_migrate.go 读取。
const (
	playerIdentityPending  = "pending_verification"
	playerIdentityVerified = "verified"
	playerIdentityRejected = "rejected"
)

type playerIdentitySubmitInput struct {
	RealName string `json:"real_name"`
	IDNumber string `json:"id_number"`
}

type playerIdentityReviewInput struct {
	Approved     bool   `json:"approved"`
	RejectReason string `json:"reject_reason"`
}

var idNumberPattern = regexp.MustCompile(`^(\d{15}|\d{17}[\dXx])$`)

// validIDNumber 身份证号基础格式校验：15 位数字或 18 位（前 17 位数字 + 数字/X）。
func validIDNumber(raw string) bool {
	return idNumberPattern.MatchString(raw)
}

// maskIDNumber 脱敏身份证号：保留前 6 后 4，中间以 * 填充。
func maskIDNumber(raw string) string {
	if len(raw) <= 10 {
		return "****"
	}
	return raw[:6] + "********" + raw[len(raw)-4:]
}

// playerIdentityStatusFromRealName 统一档案状态 → 旧状态口径。
// expired 在旧口径里没有对应值，按 rejected 处理（用户可重新提交）。
func playerIdentityStatusFromRealName(status esportsrealnameverification.Status) string {
	switch status {
	case esportsrealnameverification.StatusPending:
		return playerIdentityPending
	case esportsrealnameverification.StatusVerified:
		return playerIdentityVerified
	default:
		return playerIdentityRejected
	}
}

// playerIdentityView 把统一档案投影为旧实体形状（内存对象，不落库）。
func playerIdentityView(record *ent.EsportsRealNameVerification) *ent.EsportsPlayerIdentity {
	view := &ent.EsportsPlayerIdentity{
		ID:             record.ID,
		PlayerID:       record.SubjectID,
		RealName:       record.RealName,
		IDNumberMasked: record.IDNumberMasked,
		Status:         esportsplayeridentity.Status(playerIdentityStatusFromRealName(record.Status)),
		RejectReason:   record.FailReason,
		RequestedBy:    record.RequestedBy,
		CreatedAt:      record.CreatedAt,
	}
	if record.ReviewedByAdminID != nil {
		view.ReviewedByAdminID = record.ReviewedByAdminID
	}
	if record.Status == esportsrealnameverification.StatusVerified && record.VerifiedAt != nil {
		view.ReviewedAt = record.VerifiedAt
	} else {
		updatedAt := record.UpdatedAt
		view.ReviewedAt = &updatedAt
	}
	return view
}

// submitPlayerIdentity 陪玩提交实名认证（兼容旧接口）：内部走统一实名的 manual 渠道。
// 已待审 / 已通过时返回 errConflict；被驳回后可重新提交。
func submitPlayerIdentity(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer, in playerIdentitySubmitInput) (*ent.EsportsPlayerIdentity, error) {
	if player == nil || player.ID < 1 {
		return nil, errInvalidInput
	}
	if _, err := submitRealName(ctx, db, realnameSubjectTypePlayer, player.ID, 0,
		realNameSubmitInput{RealName: in.RealName, IDNumber: in.IDNumber, Channel: realNameChannelManual, Method: realNameMethodManual},
		time.Now()); err != nil {
		return nil, err
	}
	record, err := realNameVerificationFor(ctx, db, realnameSubjectTypePlayer, player.ID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("%w：实名档案写入失败", errConflict)
	}
	return playerIdentityView(record), nil
}

// reviewPlayerIdentity 管理员审核陪玩实名认证（兼容旧接口）：按 playerID 找待审档案再审核。
func reviewPlayerIdentity(ctx context.Context, db *ent.Client, reviewer *ent.Admin, playerID int, in playerIdentityReviewInput, now time.Time) (*ent.EsportsPlayerIdentity, error) {
	if reviewer == nil || playerID < 1 {
		return nil, errInvalidInput
	}
	record, err := realNameVerificationFor(ctx, db, realnameSubjectTypePlayer, playerID)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Status != esportsrealnameverification.StatusPending {
		return nil, errConflict
	}
	reviewed, err := reviewRealName(ctx, db, reviewer.ID, record.ID, realNameReviewInput{
		Approved: in.Approved, RejectReason: in.RejectReason,
	}, now)
	if err != nil {
		return nil, err
	}
	return playerIdentityView(reviewed), nil
}

// isPlayerIdentityVerified 判断陪玩实名认证是否已通过。updatePlayer 据此把关 qualification。
func isPlayerIdentityVerified(ctx context.Context, db *ent.Client, playerID int) (bool, error) {
	return isRealNameVerified(ctx, db, realnameSubjectTypePlayer, playerID)
}

// currentPlayerIdentityStatus 取陪玩当前实名认证状态（旧口径）；无记录返回空串（未提交）。
func currentPlayerIdentityStatus(ctx context.Context, db *ent.Client, playerID int) (string, error) {
	record, err := realNameVerificationFor(ctx, db, realnameSubjectTypePlayer, playerID)
	if err != nil {
		return "", err
	}
	if record == nil {
		return "", nil
	}
	return playerIdentityStatusFromRealName(record.Status), nil
}

// playerSubmitIdentityHandler 陪玩端提交实名认证。
// 需在 app.go 接线：{Method: "POST", Path: "/user/player/identity", Auth: cmfapp.AuthUser, Handler: playerSubmitIdentityHandler}
func playerSubmitIdentityHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in playerIdentitySubmitInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	record, err := submitPlayerIdentity(c.Context(), global.ORM, player, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"id": record.ID, "status": playerIdentityPending})
}

// playerIdentityStatusHandler 陪玩端查询本人实名认证状态。
// 需在 app.go 接线：{Method: "GET", Path: "/user/player/identity", Auth: cmfapp.AuthUser, Handler: playerIdentityStatusHandler}
func playerIdentityStatusHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	status, err := currentPlayerIdentityStatus(c.Context(), global.ORM, player.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"status": status})
}

// adminReviewPlayerIdentityHandler 管理员审核陪玩实名认证。
// 需在 app.go 接线：{Method: "POST", Path: "/admin/players/:id/identity/review", Auth: cmfapp.AuthAdmin, Handler: adminReviewPlayerIdentityHandler}
func adminReviewPlayerIdentityHandler(c fiber.Ctx) error {
	reviewer, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePathID(c, "陪玩")
	if err != nil {
		return businessError(c, err)
	}
	var in playerIdentityReviewInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	record, err := reviewPlayerIdentity(c.Context(), global.ORM, reviewer, playerID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	status := playerIdentityRejected
	if in.Approved {
		status = playerIdentityVerified
	}
	return reply(c, fiber.Map{"id": record.ID, "status": status})
}

// adminPlayerIdentityHandler 管理员查看陪玩最近一次实名认证记录。
// 身份证号只返回脱敏值；无记录时 status 为空串。
func adminPlayerIdentityHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePathID(c, "陪玩")
	if err != nil {
		return businessError(c, err)
	}
	record, err := latestPlayerIdentity(c.Context(), global.ORM, playerID)
	if err != nil {
		return businessError(c, err)
	}
	if record == nil {
		return reply(c, fiber.Map{"player_id": playerID, "status": ""})
	}
	return reply(c, fiber.Map{
		"id":                   record.ID,
		"player_id":            record.PlayerID,
		"real_name":            record.RealName,
		"id_number_masked":     record.IDNumberMasked,
		"status":               record.Status,
		"reject_reason":        record.RejectReason,
		"reviewed_by_admin_id": record.ReviewedByAdminID,
		"reviewed_at":          record.ReviewedAt,
		"created_at":           record.CreatedAt,
	})
}

// latestPlayerIdentity 取陪玩最近一次实名认证记录（统一档案投影）；无记录返回 nil。
func latestPlayerIdentity(ctx context.Context, db *ent.Client, playerID int) (*ent.EsportsPlayerIdentity, error) {
	record, err := realNameVerificationFor(ctx, db, realnameSubjectTypePlayer, playerID)
	if err != nil || record == nil {
		return nil, err
	}
	return playerIdentityView(record), nil
}

// ensurePlayerQualificationAllowed 实名未通过时禁止将陪玩资格置为 qualified。
// 供 updatePlayer 及后续考核定级路径复用。
func ensurePlayerQualificationAllowed(ctx context.Context, db *ent.Client, playerID int, qualification esportsplayer.Qualification) error {
	if qualification != esportsplayer.QualificationQualified {
		return nil
	}
	verified, err := isPlayerIdentityVerified(ctx, db, playerID)
	if err != nil {
		return err
	}
	if !verified {
		return fmt.Errorf("%w：陪玩实名认证未通过，不得设为合格", errInvalidInput)
	}
	return nil
}
