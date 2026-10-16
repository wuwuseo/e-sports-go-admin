package esports

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsrechargecampaign"
	"go-admin/internal/ent/esportswalletaccount"
	"go-admin/internal/ent/esportswallettx"
)

// campaignSourceType 是充值活动来源的钱包流水来源类型（与手动的 manual_recharge 区分）。
const campaignSourceType = "recharge_campaign"

var campaignCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,48}$`)

// campaignInput 是充值活动可编辑字段。code 创建后不可变。
type campaignInput struct {
	Code                string                `json:"code"`
	Name                string                `json:"name"`
	ActivityType        string                `json:"activity_type"`
	Tiers               []schema.CampaignTier `json:"tiers"`
	Audience            string                `json:"audience"`
	AudienceCustomerIDs []int                 `json:"audience_customer_ids"`
	StackRule           string                `json:"stack_rule"`
	BonusValidDays      int                   `json:"bonus_valid_days"`
	StartsAt            time.Time             `json:"starts_at"`
	EndsAt              time.Time             `json:"ends_at"`
}

// validateCampaignInput 校验活动配置；任一不满足返回 errInvalidInput，调用方不得写库。
func validateCampaignInput(in campaignInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" || len([]rune(name)) > 120 || !campaignCodePattern.MatchString(strings.TrimSpace(in.Code)) {
		return errInvalidInput
	}
	switch in.ActivityType {
	case "full_gift", "ladder", "limited":
	default:
		return errInvalidInput
	}
	if len(in.Tiers) == 0 {
		return errInvalidInput
	}
	prev := int64(0)
	for i, tier := range in.Tiers {
		if tier.ThresholdCents <= 0 || tier.BonusCents <= 0 {
			return errInvalidInput
		}
		// 按 threshold 严格递增。
		if i > 0 && tier.ThresholdCents <= prev {
			return errInvalidInput
		}
		prev = tier.ThresholdCents
	}
	if in.BonusValidDays < 0 || !in.StartsAt.Before(in.EndsAt) {
		return errInvalidInput
	}
	switch in.Audience {
	case "all", "new_customer":
	case "specified":
		if len(in.AudienceCustomerIDs) == 0 {
			return errInvalidInput
		}
		seen := map[int]bool{}
		for _, id := range in.AudienceCustomerIDs {
			if id < 1 || seen[id] {
				return errInvalidInput
			}
			seen[id] = true
		}
	default:
		return errInvalidInput
	}
	switch in.StackRule {
	case "exclusive", "stack":
	default:
		return errInvalidInput
	}
	return nil
}

// createCampaign 新建充值活动，状态固定为 draft；code 冲突返回 errConflict。
func createCampaign(ctx context.Context, db *ent.Client, in campaignInput) (*ent.EsportsRechargeCampaign, error) {
	if err := validateCampaignInput(in); err != nil {
		return nil, err
	}
	item, err := db.EsportsRechargeCampaign.Create().
		SetCode(strings.TrimSpace(in.Code)).SetName(strings.TrimSpace(in.Name)).
		SetActivityType(esportsrechargecampaign.ActivityType(in.ActivityType)).
		SetTiers(in.Tiers).SetAudience(esportsrechargecampaign.Audience(in.Audience)).
		SetAudienceCustomerIds(in.AudienceCustomerIDs).
		SetStackRule(esportsrechargecampaign.StackRule(in.StackRule)).
		SetBonusValidDays(in.BonusValidDays).
		SetStartsAt(in.StartsAt.UTC()).SetEndsAt(in.EndsAt.UTC()).
		SetStatus(esportsrechargecampaign.StatusDraft).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return item, nil
}

// updateCampaign 条件更新活动（version 乐观锁）；code 不可改，版本不一致返回 errConflict。
func updateCampaign(ctx context.Context, db *ent.Client, id, expectedVersion int, in campaignInput) (*ent.EsportsRechargeCampaign, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	if err := validateCampaignInput(in); err != nil {
		return nil, err
	}
	changed, err := db.EsportsRechargeCampaign.Update().Where(
		esportsrechargecampaign.IDEQ(id),
		esportsrechargecampaign.VersionEQ(expectedVersion),
	).SetName(strings.TrimSpace(in.Name)).
		SetActivityType(esportsrechargecampaign.ActivityType(in.ActivityType)).
		SetTiers(in.Tiers).SetAudience(esportsrechargecampaign.Audience(in.Audience)).
		SetAudienceCustomerIds(in.AudienceCustomerIDs).
		SetStackRule(esportsrechargecampaign.StackRule(in.StackRule)).
		SetBonusValidDays(in.BonusValidDays).
		SetStartsAt(in.StartsAt.UTC()).SetEndsAt(in.EndsAt.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsRechargeCampaign.Get(ctx, id)
}

// setCampaignStatus 状态流转：draft↔active↔paused 之间任意切换，任意非 ended → ended；
// ended 不可回退。非法流转返回 errConflict。
func setCampaignStatus(ctx context.Context, db *ent.Client, id, expectedVersion int, status string) (*ent.EsportsRechargeCampaign, error) {
	if id < 1 || expectedVersion < 1 {
		return nil, errInvalidInput
	}
	switch status {
	case "draft", "active", "paused", "ended":
	default:
		return nil, errInvalidInput
	}
	current, err := db.EsportsRechargeCampaign.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Status == esportsrechargecampaign.StatusEnded {
		return nil, errConflict
	}
	changed, err := db.EsportsRechargeCampaign.Update().Where(
		esportsrechargecampaign.IDEQ(id),
		esportsrechargecampaign.VersionEQ(expectedVersion),
		esportsrechargecampaign.StatusNEQ(esportsrechargecampaign.StatusEnded),
	).SetStatus(esportsrechargecampaign.Status(status)).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsRechargeCampaign.Get(ctx, id)
}

// listCampaigns 列出活动，status 为空表示全部，按 ID 倒序。
func listCampaigns(ctx context.Context, db *ent.Client, status string, limit int) ([]*ent.EsportsRechargeCampaign, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsRechargeCampaign.Query()
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "draft", "active", "paused", "ended":
			q.Where(esportsrechargecampaign.StatusEQ(esportsrechargecampaign.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(ent.Desc(esportsrechargecampaign.FieldID)).Limit(limit).All(ctx)
}

// campaignTierBonus 按实充金额匹配阶梯：取满足 threshold <= principal 的最高档。
// 无命中返回 (0, 0)。纯函数，便于测试。
func campaignTierBonus(tiers []schema.CampaignTier, principalCents int64) (matchedThreshold, bonusCents int64) {
	for _, tier := range tiers {
		if tier.ThresholdCents <= principalCents && tier.ThresholdCents > matchedThreshold {
			matchedThreshold = tier.ThresholdCents
			bonusCents = tier.BonusCents
		}
	}
	return matchedThreshold, bonusCents
}

// resolveRechargeBonus 按实充金额与客户资格解析充值赠送：阶梯取最高档，多活动按叠加规则处理，
// 并产出写入钱包流水的规则快照。无命中返回 (0, nil, nil, nil)。
func resolveRechargeBonus(ctx context.Context, db *ent.Client, customerID int, principalCents int64, now time.Time) (int64, map[string]any, *time.Time, error) {
	utc := now.UTC()
	active, err := db.EsportsRechargeCampaign.Query().Where(
		esportsrechargecampaign.StatusEQ(esportsrechargecampaign.StatusActive),
		esportsrechargecampaign.StartsAtLTE(utc),
		esportsrechargecampaign.EndsAtGTE(utc),
	).Order(ent.Asc(esportsrechargecampaign.FieldID)).All(ctx)
	if err != nil {
		return 0, nil, nil, err
	}
	// 新客判定：无任何 customer_balance recharge 流水即为新客。仅当存在新客活动时才查询。
	hasPriorRecharge := false
	for _, campaign := range active {
		if campaign.Audience == esportsrechargecampaign.AudienceNewCustomer {
			exists, err := db.EsportsWalletTx.Query().Where(
				esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeCustomerBalance),
				esportswallettx.OwnerIDEQ(customerID),
				esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRecharge),
			).Exist(ctx)
			if err != nil {
				return 0, nil, nil, err
			}
			hasPriorRecharge = exists
			break
		}
	}
	type campaignHit struct {
		campaign  *ent.EsportsRechargeCampaign
		threshold int64
		bonus     int64
	}
	hits := make([]campaignHit, 0, len(active))
	for _, campaign := range active {
		switch campaign.Audience {
		case esportsrechargecampaign.AudienceAll:
		case esportsrechargecampaign.AudienceNewCustomer:
			if hasPriorRecharge {
				continue
			}
		case esportsrechargecampaign.AudienceSpecified:
			matched := false
			for _, id := range campaign.AudienceCustomerIds {
				if id == customerID {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		default:
			continue
		}
		threshold, bonus := campaignTierBonus(campaign.Tiers, principalCents)
		if bonus <= 0 {
			continue
		}
		hits = append(hits, campaignHit{campaign: campaign, threshold: threshold, bonus: bonus})
	}
	if len(hits) == 0 {
		return 0, nil, nil, nil
	}
	// 叠加规则：命中的活动里只要有一个 exclusive，就只取互斥活动中赠送额最大的一个；
	// 否则把所有命中活动的赠送额求和。
	selected := hits
	var exclusive *campaignHit
	for i := range hits {
		if hits[i].campaign.StackRule != esportsrechargecampaign.StackRuleExclusive {
			continue
		}
		if exclusive == nil || hits[i].bonus > exclusive.bonus {
			exclusive = &hits[i]
		}
	}
	if exclusive != nil {
		selected = []campaignHit{*exclusive}
	}
	var totalBonus int64
	// 兼容旧流水的顶层到期日使用最晚/永久口径；各活动自身的到期日保存在 campaigns 快照中，
	// 实际消费及作废由 rechargeBonusBatches 拆为独立虚拟批次处理。
	var expiresAt *time.Time
	permanent := false
	campaigns := make([]map[string]any, 0, len(selected))
	for _, hit := range selected {
		totalBonus += hit.bonus
		if hit.campaign.BonusValidDays > 0 {
			expiry := utc.AddDate(0, 0, hit.campaign.BonusValidDays)
			if expiresAt == nil || expiry.After(*expiresAt) {
				expiresAt = &expiry
			}
		} else {
			permanent = true
		}
		campaigns = append(campaigns, map[string]any{
			"id":                      hit.campaign.ID,
			"code":                    hit.campaign.Code,
			"name":                    hit.campaign.Name,
			"activity_type":           hit.campaign.ActivityType.String(),
			"stack_rule":              hit.campaign.StackRule.String(),
			"version":                 hit.campaign.Version,
			"matched_threshold_cents": hit.threshold,
			"bonus_cents":             hit.bonus,
			"audience":                hit.campaign.Audience.String(),
			"bonus_valid_days":        hit.campaign.BonusValidDays,
			"bonus_expires_at": func() string {
				if hit.campaign.BonusValidDays <= 0 {
					return ""
				}
				return utc.AddDate(0, 0, hit.campaign.BonusValidDays).Format(time.RFC3339Nano)
			}(),
		})
	}
	// 顶层到期日为空时，作废任务仍会读取各活动快照里的独立到期日。
	if permanent {
		expiresAt = nil
	}
	snapshot := map[string]any{
		"principal_cents":   principalCents,
		"total_bonus_cents": totalBonus,
		"bonus_expires_at":  expiresAt,
		"campaigns":         campaigns,
	}
	return totalBonus, snapshot, expiresAt, nil
}

// rechargeWithCampaigns 按活动充值：只接受 customer_balance；事务内解析活动赠送并写一条
// 充值流水（本金 / 赠送分列，附规则快照与赠送有效期）。重复幂等键返回 errConflict。
// 余额拆分记账＝本金与赠送同一条流水分列（principal_delta_cents / bonus_delta_cents），
// 符合「进入不同来源批次」的记账要求。
func rechargeWithCampaigns(ctx context.Context, db *ent.Client, operatorID int, accountType string,
	ownerID int, principalCents int64, note, key string, now time.Time) (*walletAccountView, error) {
	normalized, err := normalizeWalletType(accountType)
	if err != nil || normalized != walletCustomerBalance {
		// 陪玩猫粮属结算发放，不接受充值活动。
		return nil, errInvalidInput
	}
	note = strings.TrimSpace(note)
	key = strings.TrimSpace(key)
	if ownerID < 1 || principalCents <= 0 || len([]rune(note)) > 255 || key == "" || len(key) > 80 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	displayName, err := walletOwnerName(ctx, tx, normalized, ownerID)
	if err != nil {
		return nil, err
	}
	account, err := ensureWalletAccount(ctx, tx, normalized, ownerID, displayName)
	if err != nil {
		return nil, err
	}
	if account.Status != esportswalletaccount.StatusActive {
		return nil, errConflict
	}
	bonus, snapshot, expiresAt, err := resolveRechargeBonus(ctx, tx.Client(), ownerID, principalCents, now)
	if err != nil {
		return nil, err
	}
	if err = writeCampaignRechargeTx(ctx, tx, operatorID, normalized, ownerID,
		principalCents, bonus, snapshot, expiresAt, note, key); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return walletView(ctx, db, account)
}

// writeCampaignRechargeTx 在事务内把一笔「本金 + 已确定的赠送条款」写入钱包流水（本金/赠送分列、
// 规则快照与赠送有效期），source_type 固定为 recharge_campaign。赠送额与快照由调用方给定：
// rechargeWithCampaigns 在事务内即时匹配活动，充值独立支付意图则按发起时锁定的活动快照传入，
// 两处落库口径完全一致，避免赠送随活动变更漂移。
func writeCampaignRechargeTx(ctx context.Context, tx *ent.Tx, operatorID int, normalized string,
	ownerID int, principalCents, bonusCents int64, snapshot map[string]any, expiresAt *time.Time,
	note, key string) error {
	create := tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
		SetAccountType(esportswallettx.AccountType(normalized)).SetOwnerID(ownerID).
		SetEntryType(esportswallettx.EntryTypeRecharge).
		SetPrincipalDeltaCents(principalCents).SetBonusDeltaCents(bonusCents).
		SetSourceType(campaignSourceType).SetSourceID(key).
		SetOperatorID(operatorID).SetNote(note)
	if snapshot != nil {
		create.SetRuleSnapshot(snapshot)
	}
	if expiresAt != nil {
		create.SetBonusExpiresAt(expiresAt.UTC())
	}
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return errConflict
		}
		return err
	}
	return nil
}

// bonusBatch 是赠送到期重放中的一档批次（一次充值赠送）。
type bonusBatch struct {
	sourceID  string
	expiresAt *time.Time
	remaining int64
}

// rechargeBonusBatches reconstructs each campaign's promised expiry from the
// immutable recharge snapshot. Older single-campaign rows retain their original
// source ID so already-written expiry entries remain idempotent after upgrade.
func rechargeBonusBatches(row *ent.EsportsWalletTx) []*bonusBatch {
	fallback := []*bonusBatch{{sourceID: row.TxNo, expiresAt: row.BonusExpiresAt, remaining: row.BonusDeltaCents}}
	if row.BonusDeltaCents <= 0 {
		return nil
	}
	raw, ok := row.RuleSnapshot["campaigns"]
	if !ok {
		return fallback
	}
	var grants []struct {
		BonusCents     int64  `json:"bonus_cents"`
		BonusValidDays int    `json:"bonus_valid_days"`
		BonusExpiresAt string `json:"bonus_expires_at"`
	}
	encoded, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(encoded, &grants) != nil || len(grants) == 0 {
		return fallback
	}
	var sum int64
	batches := make([]*bonusBatch, 0, len(grants))
	for i, grant := range grants {
		if grant.BonusCents <= 0 || sum > row.BonusDeltaCents-grant.BonusCents {
			return fallback
		}
		sum += grant.BonusCents
		var expiry *time.Time
		if grant.BonusValidDays > 0 {
			at := row.CreatedAt.UTC().AddDate(0, 0, grant.BonusValidDays)
			if grant.BonusExpiresAt != "" {
				parsed, parseErr := time.Parse(time.RFC3339Nano, grant.BonusExpiresAt)
				if parseErr != nil {
					return fallback
				}
				at = parsed.UTC()
			}
			expiry = &at
		}
		sourceID := row.TxNo
		if len(grants) > 1 {
			sourceID += ":" + strconv.Itoa(i)
		}
		batches = append(batches, &bonusBatch{sourceID: sourceID, expiresAt: expiry, remaining: grant.BonusCents})
	}
	if sum != row.BonusDeltaCents {
		return fallback
	}
	// When one recharge stacks several grants, consume the earliest-expiring
	// entitlement first; permanent grants remain last.
	sort.SliceStable(batches, func(i, j int) bool {
		a, b := batches[i].expiresAt, batches[j].expiresAt
		if a == nil {
			return false
		}
		if b == nil {
			return true
		}
		return a.Before(*b)
	})
	return batches
}

// expireWalletBonus 把已到期的赠送权益写作废流水（只增不改）。对每个客户余额账户做
// FIFO 批次重放：充值赠送入队，其他流水的负赠送额从前到后扣减批次剩余，重放结束后仅对
// 「已到期且仍有剩余」的批次写一条 entry_type=expire 的负数流水；唯一索引
// (source_type, source_id, entry_type) 保证重复运行不重复作废。返回实际作废的批次数。
// ponytail: 重放是 O(账户流水数) 且每次全量扫描；账户流水到十万级时改为物化批次剩余表 + 增量扣减。
func expireWalletBonus(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	utc := now.UTC()
	// 混合活动的聚合流水可能没有 bonus_expires_at；到期时间在规则快照的各活动批次中。
	candidates, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeCustomerBalance),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRecharge),
		esportswallettx.BonusDeltaCentsGT(0),
	).Order(ent.Asc(esportswallettx.FieldID)).All(ctx)
	if err != nil {
		return 0, err
	}
	owners := make([]int, 0, len(candidates))
	seen := map[int]bool{}
	for _, row := range candidates {
		due := false
		for _, batch := range rechargeBonusBatches(row) {
			if batch.expiresAt != nil && !batch.expiresAt.After(utc) {
				due = true
				break
			}
		}
		if due && !seen[row.OwnerID] {
			seen[row.OwnerID] = true
			owners = append(owners, row.OwnerID)
		}
	}
	total := 0
	for _, ownerID := range owners {
		count, err := expireOwnerBonus(ctx, db, ownerID, utc)
		if err != nil {
			return total, err
		}
		total += count
		if total >= limit {
			break
		}
	}
	return total, nil
}

// expireOwnerBonus 对单个账户做 FIFO 重放并作废到期批次。全程在一个事务内完成。
func expireOwnerBonus(ctx context.Context, db *ent.Client, ownerID int, now time.Time) (int, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeCustomerBalance),
		esportswallettx.OwnerIDEQ(ownerID),
	).Order(ent.Asc(esportswallettx.FieldID)).All(ctx)
	if err != nil {
		return 0, err
	}
	queue := make([]*bonusBatch, 0)
	for _, row := range rows {
		switch {
		case row.EntryType == esportswallettx.EntryTypeRecharge && row.BonusDeltaCents > 0:
			queue = append(queue, rechargeBonusBatches(row)...)
		case row.BonusDeltaCents < 0:
			// 先到期的先被消耗：按队列从前到后扣减批次剩余。
			consume := -row.BonusDeltaCents
			for _, batch := range queue {
				if consume <= 0 {
					break
				}
				if batch.remaining <= 0 {
					continue
				}
				if batch.remaining >= consume {
					batch.remaining -= consume
					consume = 0
				} else {
					consume -= batch.remaining
					batch.remaining = 0
				}
			}
		}
	}
	count := 0
	for _, batch := range queue {
		if batch.expiresAt == nil || batch.expiresAt.After(now) || batch.remaining <= 0 {
			continue
		}
		if _, err = tx.EsportsWalletTx.Create().SetTxNo(uuid.NewString()).
			SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(ownerID).
			SetEntryType(esportswallettx.EntryTypeExpire).
			SetPrincipalDeltaCents(0).SetBonusDeltaCents(-batch.remaining).
			SetSourceType(campaignSourceType).SetSourceID(batch.sourceID).
			SetNote("赠送权益到期作废").Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				// 该批次此前已作废，跳过即可。
				continue
			}
			return count, err
		}
		count++
	}
	if err = tx.Commit(); err != nil {
		return count, err
	}
	return count, nil
}
