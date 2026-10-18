package esports

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayeridentity"
	"go-admin/internal/global"
)

// 陪玩实名认证（§6.1）。
//
// 状态机：未提交 →（提交）→ pending_verification →（管理员审核）→ verified / rejected。
// 实名通过前陪玩 qualification 不得为 qualified（updatePlayer 把关）。
// 身份证号只存脱敏值（保留前 6 后 4），原始证件号不落库。
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

// submitPlayerIdentity 陪玩提交实名认证（§6.1）：写 pending_verification 状态。
// 已存在待审核 / 已通过记录时不允许重复提交（返回 errConflict）；被驳回后可重新提交。
func submitPlayerIdentity(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer, in playerIdentitySubmitInput) (*ent.EsportsPlayerIdentity, error) {
	in.RealName = strings.TrimSpace(in.RealName)
	in.IDNumber = strings.TrimSpace(in.IDNumber)
	if player == nil || player.ID < 1 || in.RealName == "" || len([]rune(in.RealName)) > 120 ||
		!validIDNumber(in.IDNumber) {
		return nil, errInvalidInput
	}
	existing, err := db.EsportsPlayerIdentity.Query().Where(
		esportsplayeridentity.PlayerIDEQ(player.ID),
		esportsplayeridentity.StatusIn(esportsplayeridentity.StatusPendingVerification, esportsplayeridentity.StatusVerified),
	).First(ctx)
	if err == nil && existing != nil {
		return nil, errConflict
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	// 被驳回后重新提交：删除旧 rejected 记录后再写新 pending，避免
	// (player_id,status) 唯一索引在第二次驳回时冲突。
	if _, err = db.EsportsPlayerIdentity.Delete().Where(
		esportsplayeridentity.PlayerIDEQ(player.ID),
		esportsplayeridentity.StatusEQ(esportsplayeridentity.StatusRejected),
	).Exec(ctx); err != nil {
		return nil, err
	}
	return db.EsportsPlayerIdentity.Create().SetPlayerID(player.ID).
		SetRealName(in.RealName).
		SetIDNumberMasked(maskIDNumber(in.IDNumber)).
		SetStatus(esportsplayeridentity.StatusPendingVerification).
		SetRequestedBy(0).Save(ctx)
}

// reviewPlayerIdentity 管理员审核陪玩实名认证（§6.1）：通过置 verified，驳回置 rejected。
func reviewPlayerIdentity(ctx context.Context, db *ent.Client, reviewer *ent.Admin, playerID int, in playerIdentityReviewInput, now time.Time) (*ent.EsportsPlayerIdentity, error) {
	in.RejectReason = strings.TrimSpace(in.RejectReason)
	if reviewer == nil || playerID < 1 || (!in.Approved && in.RejectReason == "") {
		return nil, errInvalidInput
	}
	if len([]rune(in.RejectReason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := tx.EsportsPlayerIdentity.Query().Where(
		esportsplayeridentity.PlayerIDEQ(playerID),
		esportsplayeridentity.StatusEQ(esportsplayeridentity.StatusPendingVerification),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	upd := tx.EsportsPlayerIdentity.Update().Where(
		esportsplayeridentity.IDEQ(record.ID),
		esportsplayeridentity.StatusEQ(esportsplayeridentity.StatusPendingVerification),
	).SetReviewedByAdminID(reviewer.ID).SetReviewedAt(now.UTC())
	if in.Approved {
		upd = upd.SetStatus(esportsplayeridentity.StatusVerified)
	} else {
		// 兼容历史遗留的多条 rejected：先删除其它 rejected 行，再落本次驳回，
		// 避免 (player_id,status) 唯一索引冲突。
		if _, err = tx.EsportsPlayerIdentity.Delete().Where(
			esportsplayeridentity.PlayerIDEQ(playerID),
			esportsplayeridentity.StatusEQ(esportsplayeridentity.StatusRejected),
			esportsplayeridentity.IDNEQ(record.ID),
		).Exec(ctx); err != nil {
			return nil, err
		}
		upd = upd.SetStatus(esportsplayeridentity.StatusRejected).SetRejectReason(in.RejectReason)
	}
	changed, err := upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayerIdentity.Get(ctx, record.ID)
}

// isPlayerIdentityVerified 判断陪玩实名认证是否已通过（§6.1）。updatePlayer 据此把关 qualification。
func isPlayerIdentityVerified(ctx context.Context, db *ent.Client, playerID int) (bool, error) {
	exists, err := db.EsportsPlayerIdentity.Query().Where(
		esportsplayeridentity.PlayerIDEQ(playerID),
		esportsplayeridentity.StatusEQ(esportsplayeridentity.StatusVerified),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// currentPlayerIdentityStatus 取陪玩当前实名认证状态；无记录返回空串（未提交）。
func currentPlayerIdentityStatus(ctx context.Context, db *ent.Client, playerID int) (string, error) {
	record, err := db.EsportsPlayerIdentity.Query().Where(
		esportsplayeridentity.PlayerIDEQ(playerID),
	).Order(ent.Desc(esportsplayeridentity.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(record.Status), nil
}

// playerSubmitIdentityHandler 陪玩端提交实名认证（§6.1）。
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

// playerIdentityStatusHandler 陪玩端查询本人实名认证状态（§6.1）。
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

// adminReviewPlayerIdentityHandler 管理员审核陪玩实名认证（§6.1）。
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

// adminPlayerIdentityHandler 管理员查看陪玩最近一次实名认证记录（§6.1）。
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

// latestPlayerIdentity 取陪玩最近一次实名认证记录；无记录返回 nil。
func latestPlayerIdentity(ctx context.Context, db *ent.Client, playerID int) (*ent.EsportsPlayerIdentity, error) {
	record, err := db.EsportsPlayerIdentity.Query().Where(
		esportsplayeridentity.PlayerIDEQ(playerID),
	).Order(ent.Desc(esportsplayeridentity.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

// ensurePlayerQualificationAllowed 实名未通过时禁止将陪玩资格置为 qualified（§6.1）。
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
