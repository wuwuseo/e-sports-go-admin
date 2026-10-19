package esports

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
)

// 陪玩端自助资料维护（补「入驻流程未贯通」缺陷）。
//
// 陪玩档案（schema/esports_identity.go 的 EsportsPlayer）只有昵称 display_name、擅长游戏 games
// 属于本人可维护的非资格类字段；资格 qualification、等级 level、在线状态 presence、
// 接单上限 daily_limit / concurrent_limit、归属 user_id 均由考核 / 派单 / 管理端控制，
// 自助接口一律不接受（输入结构里根本没有这些字段，多余的 JSON 键在绑定时被丢弃）。
// 手机号是登录账号 user.phone（陪玩档案没有手机号列），因此改手机号落库到 ent.User。
const (
	// 昵称上限与 savePlayer / updatePlayer 保持一致（schema MaxLen 120）。
	playerSelfNameMaxRunes = 120
	// 手机号：大陆 11 位手机号（user.phone 仅允许数字，MaxLen 20）。
	playerSelfPhoneMaxLen = 11
	// 擅长游戏数量与单个游戏名长度上限：仅防滥用，不改变派单口径。
	playerSelfGamesMax     = 20
	playerSelfGameMaxRunes = 40
	playerSelfPhonePattern = `^1[3-9]\d{9}$`
)

var playerSelfPhoneRegex = regexp.MustCompile(playerSelfPhonePattern)

// playerSelfProfileInput 陪玩自助修改资料的输入。
// Phone / Games 用指针区分「不修改」（nil）与「清空」（空串 / 空数组）。
// Version 为乐观锁：>0 时按 version 条件更新，冲突返回 errConflict。
//
// §6.4-06 的五项接单配置同样用指针表达「不修改」：
// 服务时段（service_slot_start_minute / end_minute，UTC+8 当日分钟数，0/0=不限）、
// 自动上线（auto_online）、可接单价范围（accept_price_min_cents / max_cents，0=该侧不限）、
// 固排开关（fixed_team_enabled）与固排时段（fixed_team_slot_start_minute / end_minute）。
type playerSelfProfileInput struct {
	DisplayName string    `json:"display_name"`
	Phone       *string   `json:"phone"`
	Games       *[]string `json:"games"`
	Version     int       `json:"version"`
	// §6.4-06 接单配置。
	ServiceSlotStartMinute   *int   `json:"service_slot_start_minute"`
	ServiceSlotEndMinute     *int   `json:"service_slot_end_minute"`
	AutoOnline               *bool  `json:"auto_online"`
	AcceptPriceMinCents      *int64 `json:"accept_price_min_cents"`
	AcceptPriceMaxCents      *int64 `json:"accept_price_max_cents"`
	FixedTeamEnabled         *bool  `json:"fixed_team_enabled"`
	FixedTeamSlotStartMinute *int   `json:"fixed_team_slot_start_minute"`
	FixedTeamSlotEndMinute   *int   `json:"fixed_team_slot_end_minute"`
}

// playerSelfProfileView 陪玩自助资料视图：与 GET /user/player/profile 的档案核心字段一致，
// 额外补上 player 表没有的预留手机号（来自登录账号）与乐观锁版本号，以及 §6.4-06 接单配置
// 与 §6.1-12 期望报价（含审核状态，供资料页展示「待审核 / 已通过 / 已驳回」）。
type playerSelfProfileView struct {
	ID               int        `json:"id"`
	DisplayName      string     `json:"display_name"`
	Phone            string     `json:"phone"`
	Qualification    string     `json:"qualification"`
	Presence         string     `json:"presence"`
	Level            string     `json:"level"`
	LevelAppraisedAt *time.Time `json:"level_appraised_at"`
	DailyLimit       int        `json:"daily_limit"`
	ConcurrentLimit  int        `json:"concurrent_limit"`
	Games            []string   `json:"games"`
	// §6.4-06 接单配置（0/0 或 0 表示不限，auto_online / fixed_team_enabled 默认关闭）。
	ServiceSlotStartMinute   int   `json:"service_slot_start_minute"`
	ServiceSlotEndMinute     int   `json:"service_slot_end_minute"`
	AutoOnline               bool  `json:"auto_online"`
	AcceptPriceMinCents      int64 `json:"accept_price_min_cents"`
	AcceptPriceMaxCents      int64 `json:"accept_price_max_cents"`
	FixedTeamEnabled         bool  `json:"fixed_team_enabled"`
	FixedTeamSlotStartMinute int   `json:"fixed_team_slot_start_minute"`
	FixedTeamSlotEndMinute   int   `json:"fixed_team_slot_end_minute"`
	// §6.1-12 期望报价与审核留痕。
	ExpectedPriceCents       int64      `json:"expected_price_cents"`
	ExpectedPriceStatus      string     `json:"expected_price_status"`
	ExpectedPriceSubmittedAt *time.Time `json:"expected_price_submitted_at"`
	ExpectedPriceReviewedAt  *time.Time `json:"expected_price_reviewed_at"`
	ExpectedPriceReviewNote  string     `json:"expected_price_review_note"`
	Version                  int        `json:"version"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

// validMainlandPhone 大陆手机号基础校验（11 位、1 开头、第二位 3-9）。
func validMainlandPhone(raw string) bool {
	return len(raw) == playerSelfPhoneMaxLen && playerSelfPhoneRegex.MatchString(raw)
}

// normalizePlayerSelfGames 归一化擅长游戏：去空白、丢空项、去重、限制数量与单项长度。
func normalizePlayerSelfGames(raw []string) ([]string, error) {
	games := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if len([]rune(name)) > playerSelfGameMaxRunes {
			return nil, fmt.Errorf("%w：单个游戏名称不得超过 %d 个字符", errInvalidInput, playerSelfGameMaxRunes)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		games = append(games, name)
	}
	if len(games) > playerSelfGamesMax {
		return nil, fmt.Errorf("%w：擅长游戏最多 %d 个", errInvalidInput, playerSelfGamesMax)
	}
	return games, nil
}

// playerSelfProfile 组装自助资料视图（手机号取登录账号，查不到按空串处理）。
func playerSelfProfile(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer) (*playerSelfProfileView, error) {
	phone := ""
	account, err := db.User.Query().Where(user.IDEQ(player.UserID)).Only(ctx)
	if err == nil {
		phone = account.Phone
	} else if !ent.IsNotFound(err) {
		return nil, err
	}
	games := player.Games
	if games == nil {
		games = []string{}
	}
	return &playerSelfProfileView{
		ID: player.ID, DisplayName: player.DisplayName, Phone: phone,
		Qualification: player.Qualification.String(), Presence: player.Presence.String(),
		Level: player.Level.String(), LevelAppraisedAt: player.LevelAppraisedAt,
		DailyLimit: player.DailyLimit, ConcurrentLimit: player.ConcurrentLimit,
		Games:                    games,
		ServiceSlotStartMinute:   player.ServiceSlotStartMinute,
		ServiceSlotEndMinute:     player.ServiceSlotEndMinute,
		AutoOnline:               player.AutoOnline,
		AcceptPriceMinCents:      player.AcceptPriceMinCents,
		AcceptPriceMaxCents:      player.AcceptPriceMaxCents,
		FixedTeamEnabled:         player.FixedTeamEnabled,
		FixedTeamSlotStartMinute: player.FixedTeamSlotStartMinute,
		FixedTeamSlotEndMinute:   player.FixedTeamSlotEndMinute,
		ExpectedPriceCents:       player.ExpectedPriceCents,
		ExpectedPriceStatus:      player.ExpectedPriceStatus.String(),
		ExpectedPriceSubmittedAt: player.ExpectedPriceSubmittedAt,
		ExpectedPriceReviewedAt:  player.ExpectedPriceReviewedAt,
		ExpectedPriceReviewNote:  player.ExpectedPriceReviewNote,
		Version:                  player.Version, UpdatedAt: player.UpdatedAt,
	}, nil
}

// updatePlayerSelfProfile 陪玩自助修改非资格类资料（昵称 / 预留手机号 / 擅长游戏）。
//
// 只允许改这三类字段；资格、等级、在线状态、接单上限与归属账号不在输入结构内，传入也会被忽略。
// 昵称必填（与 updatePlayer 同口径）；手机号可留空表示清空预留号，非空必须为大陆 11 位手机号。
// 陪玩档案与账号手机号同事务写入，任一失败整体回滚；version>0 时按乐观锁条件更新。
func updatePlayerSelfProfile(ctx context.Context, db *ent.Client, playerID int, in playerSelfProfileInput) (*ent.EsportsPlayer, error) {
	if playerID < 1 {
		return nil, errForbidden
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		return nil, fmt.Errorf("%w：昵称不能为空", errInvalidInput)
	}
	if len([]rune(displayName)) > playerSelfNameMaxRunes {
		return nil, fmt.Errorf("%w：昵称不得超过 %d 个字符", errInvalidInput, playerSelfNameMaxRunes)
	}
	changePhone := in.Phone != nil
	phone := ""
	if changePhone {
		phone = strings.TrimSpace(*in.Phone)
		if phone != "" && !validMainlandPhone(phone) {
			return nil, fmt.Errorf("%w：手机号格式不正确，请填写 11 位大陆手机号", errInvalidInput)
		}
	}
	var games []string
	if in.Games != nil {
		normalized, err := normalizePlayerSelfGames(*in.Games)
		if err != nil {
			return nil, err
		}
		games = normalized
	}

	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	player, err := tx.EsportsPlayer.Get(ctx, playerID)
	if ent.IsNotFound(err) {
		// 与 requestPlayer 同口径：账号未绑定陪玩档案视为无权操作。
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	update := tx.EsportsPlayer.Update().Where(esportsplayer.IDEQ(playerID)).SetDisplayName(displayName)
	if in.Games != nil {
		update = update.SetGames(games)
	}
	// §6.4-06：五项接单配置（未传的字段保持原值）。
	configUpdate, err := applyPlayerAcceptConfig(update, playerAcceptConfig{
		ServiceSlotStartMinute:   in.ServiceSlotStartMinute,
		ServiceSlotEndMinute:     in.ServiceSlotEndMinute,
		AutoOnline:               in.AutoOnline,
		AcceptPriceMinCents:      in.AcceptPriceMinCents,
		AcceptPriceMaxCents:      in.AcceptPriceMaxCents,
		FixedTeamEnabled:         in.FixedTeamEnabled,
		FixedTeamSlotStartMinute: in.FixedTeamSlotStartMinute,
		FixedTeamSlotEndMinute:   in.FixedTeamSlotEndMinute,
	})
	if err != nil {
		return nil, err
	}
	update = configUpdate
	// 乐观锁：仅在客户端带上版本号时启用，未带则按最后写入生效（保持与旧前端兼容）。
	if in.Version > 0 {
		update = update.Where(esportsplayer.VersionEQ(in.Version))
	}
	changed, err := update.AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if changePhone {
		// 手机号属于登录账号：陪玩档案没有手机号列，写 ent.User 才能被手机号登录 / 找回复用。
		accountChanged, err := tx.User.Update().Where(user.IDEQ(player.UserID)).SetPhone(phone).Save(ctx)
		if err != nil {
			return nil, err
		}
		if accountChanged != 1 {
			return nil, errForbidden
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPlayer.Get(ctx, playerID)
}

// ── §6.4-06 接单配置（服务时段 / 自动上线 / 可接单价 / 固排开关与时段） ──

// playerAcceptConfig 是五项接单配置的「可选补丁」：nil 表示不修改。
type playerAcceptConfig struct {
	ServiceSlotStartMinute   *int
	ServiceSlotEndMinute     *int
	AutoOnline               *bool
	AcceptPriceMinCents      *int64
	AcceptPriceMaxCents      *int64
	FixedTeamEnabled         *bool
	FixedTeamSlotStartMinute *int
	FixedTeamSlotEndMinute   *int
}

// playerAcceptConfigValidationError 校验接单配置取值：分钟数必须在 [0,1439]（UTC+8 当日分钟数），
// 价区间必须非负且上下限不能颠倒。用于把「非法配置」挡在写入口，避免生效逻辑读到脏值。
func playerAcceptConfigValidationError(minute *int, label string) error {
	if minute == nil {
		return nil
	}
	if *minute < 0 || *minute > 1439 {
		return fmt.Errorf("%w：%s必须为 0~1439 的当日分钟数", errInvalidInput, label)
	}
	return nil
}

// applyPlayerAcceptConfig 把接单配置补丁写进 update（只写传入的字段），非法取值返回中文参数错误。
func applyPlayerAcceptConfig(update *ent.EsportsPlayerUpdate,
	cfg playerAcceptConfig) (*ent.EsportsPlayerUpdate, error) {
	for _, item := range []struct {
		value *int
		label string
	}{
		{cfg.ServiceSlotStartMinute, "服务时段开始分钟"},
		{cfg.ServiceSlotEndMinute, "服务时段结束分钟"},
		{cfg.FixedTeamSlotStartMinute, "固排时段开始分钟"},
		{cfg.FixedTeamSlotEndMinute, "固排时段结束分钟"},
	} {
		if err := playerAcceptConfigValidationError(item.value, item.label); err != nil {
			return nil, err
		}
	}
	if cfg.AcceptPriceMinCents != nil && *cfg.AcceptPriceMinCents < 0 {
		return nil, fmt.Errorf("%w：可接价下限不得为负", errInvalidInput)
	}
	if cfg.AcceptPriceMaxCents != nil && *cfg.AcceptPriceMaxCents < 0 {
		return nil, fmt.Errorf("%w：可接价上限不得为负", errInvalidInput)
	}
	if cfg.AcceptPriceMinCents != nil && cfg.AcceptPriceMaxCents != nil &&
		*cfg.AcceptPriceMaxCents > 0 && *cfg.AcceptPriceMinCents > *cfg.AcceptPriceMaxCents {
		return nil, fmt.Errorf("%w：可接价下限不得高于上限", errInvalidInput)
	}
	if cfg.ServiceSlotStartMinute != nil {
		update = update.SetServiceSlotStartMinute(*cfg.ServiceSlotStartMinute)
	}
	if cfg.ServiceSlotEndMinute != nil {
		update = update.SetServiceSlotEndMinute(*cfg.ServiceSlotEndMinute)
	}
	if cfg.AutoOnline != nil {
		update = update.SetAutoOnline(*cfg.AutoOnline)
	}
	if cfg.AcceptPriceMinCents != nil {
		update = update.SetAcceptPriceMinCents(*cfg.AcceptPriceMinCents)
	}
	if cfg.AcceptPriceMaxCents != nil {
		update = update.SetAcceptPriceMaxCents(*cfg.AcceptPriceMaxCents)
	}
	if cfg.FixedTeamEnabled != nil {
		update = update.SetFixedTeamEnabled(*cfg.FixedTeamEnabled)
	}
	if cfg.FixedTeamSlotStartMinute != nil {
		update = update.SetFixedTeamSlotStartMinute(*cfg.FixedTeamSlotStartMinute)
	}
	if cfg.FixedTeamSlotEndMinute != nil {
		update = update.SetFixedTeamSlotEndMinute(*cfg.FixedTeamSlotEndMinute)
	}
	return update, nil
}

// ── §6.1-12 期望报价（陪玩提交 + 运营复核） ──

// playerExpectedPriceLimitsKey 期望报价允许区间（分，裸键 player_expected_price_limits_cents）。
// 格式 "min,max"，0 表示该侧不限；缺失/非法一律回落 "0,0"（两侧不限）。
const playerExpectedPriceLimitsKey = "app.esports.player_expected_price_limits_cents"

// playerExpectedPriceLimits 读取期望报价允许区间；任一异常都回落 (0,0)，不因脏配置把所有人拒掉。
func playerExpectedPriceLimits(ctx context.Context, db *ent.Client) (int64, int64) {
	raw := strings.TrimSpace(assessmentConfigString(ctx, db, playerExpectedPriceLimitsKey, ""))
	if raw == "" {
		return 0, 0
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 2 {
		return 0, 0
	}
	minimum, minErr := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	maximum, maxErr := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if minErr != nil || maxErr != nil || minimum < 0 || maximum < 0 {
		return 0, 0
	}
	if minimum > 0 && maximum > 0 && minimum > maximum {
		return 0, 0
	}
	return minimum, maximum
}

// submitPlayerExpectedPrice 陪玩提交期望报价（§6.1-12）：落 pending 待运营审核。
//
// 口径（可验收断言）：
//   - 报价必须是正整数分，越出配置区间（min,max，0=该侧不限）直接拒绝；
//   - 当前已是 pending 时不得重复提交（避免审核队列被刷）；
//   - 已驳回 / 已通过后可重新提交，重新提交会清空上一轮审核留痕并回到 pending。
func submitPlayerExpectedPrice(ctx context.Context, db *ent.Client, playerID int, cents int64,
	now time.Time) (*ent.EsportsPlayer, error) {
	if playerID < 1 || cents <= 0 {
		return nil, fmt.Errorf("%w：期望报价必须为正整数（分）", errInvalidInput)
	}
	minimum, maximum := playerExpectedPriceLimits(ctx, db)
	if minimum > 0 && cents < minimum {
		return nil, fmt.Errorf("%w：期望报价不得低于 %d 分", errInvalidInput, minimum)
	}
	if maximum > 0 && cents > maximum {
		return nil, fmt.Errorf("%w：期望报价不得高于 %d 分", errInvalidInput, maximum)
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	if player.ExpectedPriceStatus == esportsplayer.ExpectedPriceStatusPending {
		return nil, fmt.Errorf("%w：期望报价正在审核中，请勿重复提交", errConflict)
	}
	changed, err := db.EsportsPlayer.Update().Where(
		esportsplayer.IDEQ(playerID), esportsplayer.VersionEQ(player.Version),
	).SetExpectedPriceCents(cents).
		SetExpectedPriceStatus(esportsplayer.ExpectedPriceStatusPending).
		SetExpectedPriceSubmittedAt(now.UTC()).
		ClearExpectedPriceReviewedByAdminID().ClearExpectedPriceReviewedAt().
		SetExpectedPriceReviewNote("").
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPlayer.Get(ctx, playerID)
}

// reviewPlayerExpectedPrice 运营复核期望报价（§6.1-12）：通过或驳回，落审核人 / 时间 / 原因。
// 驳回必须填写原因（审核结论要可追溯）；仅 pending 状态可复核。
func reviewPlayerExpectedPrice(ctx context.Context, db *ent.Client, adminID, playerID int,
	approve bool, note string, now time.Time) (*ent.EsportsPlayer, error) {
	note = strings.TrimSpace(note)
	if adminID < 1 || playerID < 1 || len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	if !approve && note == "" {
		return nil, fmt.Errorf("%w：驳回期望报价须填写原因", errInvalidInput)
	}
	player, err := db.EsportsPlayer.Get(ctx, playerID)
	if ent.IsNotFound(err) {
		return nil, errInvalidInput
	}
	if err != nil {
		return nil, err
	}
	if player.ExpectedPriceStatus != esportsplayer.ExpectedPriceStatusPending {
		return nil, fmt.Errorf("%w：该期望报价不在待审核状态", errConflict)
	}
	target := esportsplayer.ExpectedPriceStatusApproved
	if !approve {
		target = esportsplayer.ExpectedPriceStatusRejected
	}
	changed, err := db.EsportsPlayer.Update().Where(
		esportsplayer.IDEQ(playerID), esportsplayer.VersionEQ(player.Version),
		esportsplayer.ExpectedPriceStatusEQ(esportsplayer.ExpectedPriceStatusPending),
	).SetExpectedPriceStatus(target).
		SetExpectedPriceReviewedByAdminID(adminID).SetExpectedPriceReviewedAt(now.UTC()).
		SetExpectedPriceReviewNote(note).
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPlayer.Get(ctx, playerID)
}

// playerSubmitExpectedPriceHandler 陪玩端提交期望报价（§6.1-12）。需在 app.go 接线：
// {Method: "POST", Path: "/user/player/expected-price", Auth: cmfapp.AuthUser, Handler: playerSubmitExpectedPriceHandler}
func playerSubmitExpectedPriceHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedPriceCents int64 `json:"expected_price_cents"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	updated, err := submitPlayerExpectedPrice(c.Context(), global.ORM, player.ID,
		in.ExpectedPriceCents, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerSelfProfile(c.Context(), global.ORM, updated)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// adminReviewExpectedPriceHandler 运营复核期望报价（§6.1-12）。需在 app.go 接线：
// {Method: "POST", Path: "/admin/players/:id/expected-price/review", Auth: cmfapp.AuthAdmin,
// Handler: adminReviewExpectedPriceHandler}，并补同路径 PermAPI（见交付报告）。
func adminReviewExpectedPriceHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePathID(c, "陪玩")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	updated, err := reviewPlayerExpectedPrice(c.Context(), global.ORM, adminID, playerID,
		in.Approve, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerSelfProfile(c.Context(), global.ORM, updated)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// playerUpdateProfileHandler 陪玩端自助修改本人资料（昵称 / 预留手机号 / 擅长游戏）。
// 需在 app.go 接线：{Method: "PUT", Path: "/user/player/profile", Auth: cmfapp.AuthUser, Handler: playerUpdateProfileHandler}
// 陪玩身份由 AuthUser + requestPlayer 保证，无需 PermAPI。
func playerUpdateProfileHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in playerSelfProfileInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	updated, err := updatePlayerSelfProfile(c.Context(), global.ORM, player.ID, in)
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerSelfProfile(c.Context(), global.ORM, updated)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
