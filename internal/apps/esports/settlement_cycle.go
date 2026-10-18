package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportssettlementcycle"
	"go-admin/internal/ent/systemconfig"
)

// 结算周期（§12.6）：
// 1. 周期支持日结 / 周结 / 月结 / 自定义间隔（间隔天数按 UTC 自然日从 epoch 对齐切分）；
// 2. 配置维度「全局默认 → 按游戏 → 按等级 → 单个陪玩」逐级覆盖，同一维度同一 key 只有一条配置；
// 3. 全部缺失时回落系统配置项 app.esports.settlement_period（兼容旧配置），再回落 weekly。

const settlementCycleScopePlayer = "player"

// effectiveSettlementCycle 解析后的生效结算周期；Scope/ScopeKey 记录命中层级，便于展示与排查。
type effectiveSettlementCycle struct {
	Period       string
	IntervalDays int
	Scope        string
	ScopeKey     string
}

type settlementCycleInput struct {
	Scope        string `json:"scope"`
	ScopeKey     string `json:"scope_key"`
	Period       string `json:"period"`
	IntervalDays int    `json:"interval_days"`
	Enabled      *bool  `json:"enabled"`
	Note         string `json:"note"`
}

func validSettlementCyclePeriod(period string) bool {
	switch period {
	case "daily", "weekly", "monthly", "custom":
		return true
	}
	return false
}

// validateSettlementCycle 校验维度、键值与周期组合；非法返回 errInvalidInput。
func validateSettlementCycle(scope, scopeKey, period string, intervalDays int) error {
	switch scope {
	case "global":
		if scopeKey != "" {
			return fmt.Errorf("%w：全局默认维度不允许 scope_key", errInvalidInput)
		}
	case "game":
		if scopeKey == "" || len([]rune(scopeKey)) > 64 {
			return fmt.Errorf("%w：按游戏维度须填游戏分区名", errInvalidInput)
		}
	case "level":
		switch scopeKey {
		case "novice", "skilled", "senior", "mentor":
		default:
			return fmt.Errorf("%w：按等级维度的等级值非法", errInvalidInput)
		}
	case "player":
		id, err := strconv.Atoi(scopeKey)
		if err != nil || id < 1 {
			return fmt.Errorf("%w：按陪玩维度须填陪玩档案 ID", errInvalidInput)
		}
	default:
		return errInvalidInput
	}
	switch period {
	case "daily", "weekly", "monthly":
		if intervalDays != 0 {
			return fmt.Errorf("%w：非自定义周期不允许设置间隔天数", errInvalidInput)
		}
	case "custom":
		if intervalDays < 1 || intervalDays > 365 {
			return fmt.Errorf("%w：自定义间隔天数须在 1–365 之间", errInvalidInput)
		}
	default:
		return fmt.Errorf("%w：结算周期取值非法", errInvalidInput)
	}
	return nil
}

// createSettlementCycle 新建（或覆盖）一条维度配置：同一维度同一 key 只保留一条，重复创建返回 errConflict。
func createSettlementCycle(ctx context.Context, db *ent.Client, in settlementCycleInput, adminID int, now time.Time) (*ent.EsportsSettlementCycle, error) {
	in.Scope = strings.TrimSpace(in.Scope)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.Period = strings.TrimSpace(in.Period)
	in.Note = strings.TrimSpace(in.Note)
	if len([]rune(in.Note)) > 255 {
		return nil, errInvalidInput
	}
	if in.Scope == "" {
		in.Scope = "global"
	}
	if in.Period == "" {
		in.Period = "weekly"
	}
	if err := validateSettlementCycle(in.Scope, in.ScopeKey, in.Period, in.IntervalDays); err != nil {
		return nil, err
	}
	create := db.EsportsSettlementCycle.Create().
		SetScope(esportssettlementcycle.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetPeriod(esportssettlementcycle.Period(in.Period)).SetIntervalDays(in.IntervalDays).
		SetEnabled(true).SetNote(in.Note).SetCreatedByAdminID(adminID)
	if in.Enabled != nil {
		create.SetEnabled(*in.Enabled)
	}
	cycle, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return cycle, nil
}

// updateSettlementCycle 更新配置（维度与键值不可改，避免与逐级覆盖语义冲突）。
func updateSettlementCycle(ctx context.Context, db *ent.Client, id int, in settlementCycleInput, now time.Time) (*ent.EsportsSettlementCycle, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	in.Period = strings.TrimSpace(in.Period)
	in.Note = strings.TrimSpace(in.Note)
	if len([]rune(in.Note)) > 255 || !validSettlementCyclePeriod(in.Period) {
		return nil, errInvalidInput
	}
	cycle, err := db.EsportsSettlementCycle.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = validateSettlementCycle(cycle.Scope.String(), cycle.ScopeKey, in.Period, in.IntervalDays); err != nil {
		return nil, err
	}
	update := db.EsportsSettlementCycle.UpdateOneID(id).
		SetPeriod(esportssettlementcycle.Period(in.Period)).
		SetIntervalDays(in.IntervalDays).SetNote(in.Note).
		AddVersion(1).SetUpdatedAt(now.UTC())
	if in.Enabled != nil {
		update.SetEnabled(*in.Enabled)
	}
	if err = update.Exec(ctx); err != nil {
		return nil, err
	}
	return db.EsportsSettlementCycle.Get(ctx, id)
}

// setSettlementCycleEnabled 启用 / 停用配置；停用后该维度不再参与解析。
func setSettlementCycleEnabled(ctx context.Context, db *ent.Client, id int, enabled bool, now time.Time) (*ent.EsportsSettlementCycle, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	if err := db.EsportsSettlementCycle.UpdateOneID(id).SetEnabled(enabled).
		AddVersion(1).SetUpdatedAt(now.UTC()).Exec(ctx); err != nil {
		return nil, err
	}
	return db.EsportsSettlementCycle.Get(ctx, id)
}

// listSettlementCycles 列出配置，按维度优先级排序（陪玩 → 等级 → 游戏 → 全局）。
func listSettlementCycles(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsSettlementCycle, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	return db.EsportsSettlementCycle.Query().
		Order(ent.Desc(esportssettlementcycle.FieldID)).Limit(limit).All(ctx)
}

// resolveSettlementCycle 按 陪玩 → 等级 → 游戏 → 全局 解析陪玩的结算周期。
// 未命中任何配置时回落系统配置项 app.esports.settlement_period（daily/weekly/monthly），再回落 weekly。
func resolveSettlementCycle(ctx context.Context, db *ent.Client, player *ent.EsportsPlayer) (effectiveSettlementCycle, error) {
	rows, err := db.EsportsSettlementCycle.Query().
		Where(esportssettlementcycle.EnabledEQ(true)).All(ctx)
	if err != nil {
		return effectiveSettlementCycle{}, err
	}
	byScope := map[string]*ent.EsportsSettlementCycle{}
	for _, row := range rows {
		byScope[row.Scope.String()+"\x00"+row.ScopeKey] = row
	}
	pick := func(scope, key string) *ent.EsportsSettlementCycle {
		return byScope[scope+"\x00"+key]
	}
	if player != nil {
		if row := pick("player", strconv.Itoa(player.ID)); row != nil {
			return cycleFromRow(row, "player", row.ScopeKey), nil
		}
		if row := pick("level", player.Level.String()); row != nil {
			return cycleFromRow(row, "level", row.ScopeKey), nil
		}
		for _, game := range player.Games {
			game = strings.TrimSpace(game)
			if game == "" {
				continue
			}
			if row := pick("game", game); row != nil {
				return cycleFromRow(row, "game", row.ScopeKey), nil
			}
		}
	}
	if row := pick("global", ""); row != nil {
		return cycleFromRow(row, "global", ""), nil
	}
	return effectiveSettlementCycle{
		Period: settlementPeriod(ctx, db), Scope: "system_config",
	}, nil
}

func cycleFromRow(row *ent.EsportsSettlementCycle, scope, scopeKey string) effectiveSettlementCycle {
	return effectiveSettlementCycle{
		Period:       row.Period.String(),
		IntervalDays: row.IntervalDays,
		Scope:        scope,
		ScopeKey:     scopeKey,
	}
}

// settlementPeriodWindow 返回 now 所在周期窗口 [start, end)。
// daily/weekly/monthly 沿用 UTC 自然周期（与 nextSettlementAt 同口径）；
// custom 以 1970-01-01 UTC 为锚按 intervalDays 对齐切分。
func settlementPeriodWindow(period string, intervalDays int, now time.Time) (time.Time, time.Time) {
	utc := now.UTC()
	day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "daily":
		return day, day.AddDate(0, 0, 1)
	case "monthly":
		start := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	case "custom":
		if intervalDays < 1 {
			intervalDays = 1
		}
		epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
		days := int(utc.Sub(epoch).Hours() / 24)
		cycles := days / intervalDays
		if days < 0 && days%intervalDays != 0 {
			cycles--
		}
		start := epoch.AddDate(0, 0, cycles*intervalDays)
		return start, start.AddDate(0, 0, intervalDays)
	default:
		offset := (int(day.Weekday()) + 6) % 7 // 距本周一的天数
		start := day.AddDate(0, 0, -offset)
		return start, start.AddDate(0, 0, 7)
	}
}

// settlementCycleWindow 返回该周期配置下 now 所在窗口与下一周期起点。
func settlementCycleWindow(cycle effectiveSettlementCycle, now time.Time) (time.Time, time.Time, time.Time) {
	start, end := settlementPeriodWindow(cycle.Period, cycle.IntervalDays, now)
	return start, end, end
}

// previousSettlementCycleWindow 返回上一个完整周期窗口 [start, end)。
// 取当前周期起点再往前 1 秒必然落在上一周期内（与客服提成口径一致）。
func previousSettlementCycleWindow(cycle effectiveSettlementCycle, now time.Time) (time.Time, time.Time) {
	start, _ := settlementPeriodWindow(cycle.Period, cycle.IntervalDays, now)
	prevStart, _ := settlementPeriodWindow(cycle.Period, cycle.IntervalDays, start.Add(-time.Second))
	return prevStart, start
}

// generateDuePlayerSettlements 为各陪玩按其生效周期自动生成「上一完整周期」的结算单（§12.6）。
// 只处理「该周期内有可结算（未冻结）待结算明细」的陪玩；同陪玩同周期已存在结算单时跳过（幂等）；
// 单个陪玩失败不中断整体任务。生成的是草稿结算单，锁定与发放仍由财务操作。
func generateDuePlayerSettlements(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	pending, err := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.StatusEQ(esportscommissionentry.StatusPendingSettlement),
		esportscommissionentry.SettlementIDIsNil(),
		esportscommissionentry.FrozenAtIsNil(),
	).Select(esportscommissionentry.FieldPlayerID, esportscommissionentry.FieldCreatedAt).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	accruedAt := map[int][]time.Time{}
	for _, entry := range pending {
		accruedAt[entry.PlayerID] = append(accruedAt[entry.PlayerID], entry.CreatedAt)
	}
	created := 0
	for playerID, times := range accruedAt {
		if created >= limit {
			break
		}
		player, err := db.EsportsPlayer.Get(ctx, playerID)
		if err != nil {
			continue
		}
		cycle, err := resolveSettlementCycle(ctx, db, player)
		if err != nil {
			return created, err
		}
		start, end := previousSettlementCycleWindow(cycle, now)
		found := false
		for _, at := range times {
			if !at.Before(start) && at.Before(end) {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		exists, err := db.EsportsSettlement.Query().Where(
			esportssettlement.OwnerTypeEQ(esportssettlement.OwnerTypePlayer),
			esportssettlement.OwnerIDEQ(playerID),
			esportssettlement.PeriodStartEQ(start.UTC()),
			esportssettlement.PeriodEndEQ(end.UTC()),
		).Exist(ctx)
		if err != nil {
			return created, err
		}
		if exists {
			continue
		}
		if _, err = settlePlayerCommission(ctx, db, playerID, start, end, 0, now); err != nil {
			continue
		}
		created++
	}
	return created, nil
}

// settlementCycleConfigValue 读取系统配置项 app.esports.settlement_period（供解析回落与展示）。
func settlementCycleConfigValue(ctx context.Context, db *ent.Client) (string, error) {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(settlementPeriodKey)).Only(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(item.Value), nil
}
