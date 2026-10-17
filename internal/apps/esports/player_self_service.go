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
type playerSelfProfileInput struct {
	DisplayName string    `json:"display_name"`
	Phone       *string   `json:"phone"`
	Games       *[]string `json:"games"`
	Version     int       `json:"version"`
}

// playerSelfProfileView 陪玩自助资料视图：与 GET /user/player/profile 的档案核心字段一致，
// 额外补上 player 表没有的预留手机号（来自登录账号）与乐观锁版本号。
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
	Version          int        `json:"version"`
	UpdatedAt        time.Time  `json:"updated_at"`
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
		Games: games, Version: player.Version, UpdatedAt: player.UpdatedAt,
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
