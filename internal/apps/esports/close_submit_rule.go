package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsclosesubmitrule"
	"go-admin/internal/ent/esportsservicesegment"
)

// 结单提交规则：规定结单前「陪玩个人服务段上报」（服务量 + 凭证）需要多少成员完成、
// 是否允许客服代提交、未达标如何处理；按 全局 / 游戏分区 / SKU 绑定，与结单规则同款版本管理。
// 口径：服务段 status=finished 且（上报服务量 > 0 或已填服务凭证）视为该成员「已提交」。

type closeSubmitRuleInput struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	Scope            string `json:"scope"`
	ScopeKey         string `json:"scope_key"`
	Mode             string `json:"mode"`
	MinSubmitRatioBp int    `json:"min_submit_ratio_bp"`
	AllowSubstitute  bool   `json:"allow_substitute"`
	OnMissing        string `json:"on_missing"`
	Note             string `json:"note"`
}

func validCloseSubmitMode(mode string) bool {
	return mode == "single" || mode == "multi" || mode == "custom"
}

func validCloseSubmitOnMissing(value string) bool {
	return value == "block" || value == "remind" || value == "auto_accept"
}

func validateCloseSubmitRuleInput(in closeSubmitRuleInput) error {
	if strings.TrimSpace(in.Code) == "" || len(in.Code) > 48 ||
		strings.TrimSpace(in.Name) == "" || len([]rune(in.Name)) > 64 ||
		!validCloseFormScope(in.Scope) || len([]rune(strings.TrimSpace(in.ScopeKey))) > 64 ||
		!validCloseSubmitMode(in.Mode) || in.MinSubmitRatioBp < 0 || in.MinSubmitRatioBp > 10000 ||
		!validCloseSubmitOnMissing(in.OnMissing) || len([]rune(strings.TrimSpace(in.Note))) > 255 {
		return errInvalidInput
	}
	if in.Scope != "global" && strings.TrimSpace(in.ScopeKey) == "" {
		return errInvalidInput
	}
	if in.Mode == "custom" && in.MinSubmitRatioBp == 0 {
		return fmt.Errorf("%w：自定义模式须填写大于 0 的提交比例", errInvalidInput)
	}
	return nil
}

func normalizeCloseSubmitRuleInput(in closeSubmitRuleInput) closeSubmitRuleInput {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.ScopeKey = strings.TrimSpace(in.ScopeKey)
	in.Note = strings.TrimSpace(in.Note)
	return in
}

// createCloseSubmitRule 新建提交规则（draft）。同 code 已存在则版本 = 现有最大 version + 1。
func createCloseSubmitRule(ctx context.Context, db *ent.Client, in closeSubmitRuleInput, adminID int) (*ent.EsportsCloseSubmitRule, error) {
	in = normalizeCloseSubmitRuleInput(in)
	if err := validateCloseSubmitRuleInput(in); err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	version := 1
	latest, err := tx.EsportsCloseSubmitRule.Query().Where(
		esportsclosesubmitrule.CodeEQ(in.Code),
	).Order(ent.Desc(esportsclosesubmitrule.FieldVersion)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if latest != nil {
		version = latest.Version + 1
	}
	item, err := tx.EsportsCloseSubmitRule.Create().SetCode(in.Code).SetName(in.Name).
		SetScope(esportsclosesubmitrule.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMode(esportsclosesubmitrule.Mode(in.Mode)).SetMinSubmitRatioBp(in.MinSubmitRatioBp).
		SetAllowSubstitute(in.AllowSubstitute).SetOnMissing(esportsclosesubmitrule.OnMissing(in.OnMissing)).
		SetVersion(version).SetCreatedByAdminID(adminID).SetNote(in.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseSubmitRule.Get(ctx, item.ID)
}

// updateCloseSubmitRule 编辑草稿（乐观锁，须为 draft）；code 不可改。
func updateCloseSubmitRule(ctx context.Context, db *ent.Client, id, expectedVersion int, in closeSubmitRuleInput) (*ent.EsportsCloseSubmitRule, error) {
	in = normalizeCloseSubmitRuleInput(in)
	if err := validateCloseSubmitRuleInput(in); err != nil {
		return nil, err
	}
	changed, err := db.EsportsCloseSubmitRule.Update().Where(
		esportsclosesubmitrule.IDEQ(id),
		esportsclosesubmitrule.VersionEQ(expectedVersion),
		esportsclosesubmitrule.StatusEQ(esportsclosesubmitrule.StatusDraft),
	).SetName(in.Name).SetScope(esportsclosesubmitrule.Scope(in.Scope)).SetScopeKey(in.ScopeKey).
		SetMode(esportsclosesubmitrule.Mode(in.Mode)).SetMinSubmitRatioBp(in.MinSubmitRatioBp).
		SetAllowSubstitute(in.AllowSubstitute).SetOnMissing(esportsclosesubmitrule.OnMissing(in.OnMissing)).
		SetNote(in.Note).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsCloseSubmitRule.Get(ctx, id)
}

// activateCloseSubmitRule 生效：同 code 现有 active 转 history，本行（须 draft）转 active。
func activateCloseSubmitRule(ctx context.Context, db *ent.Client, id int) (*ent.EsportsCloseSubmitRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	target, err := tx.EsportsCloseSubmitRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if target.Status != esportsclosesubmitrule.StatusDraft {
		return nil, errConflict
	}
	if _, err = tx.EsportsCloseSubmitRule.Update().Where(
		esportsclosesubmitrule.CodeEQ(target.Code),
		esportsclosesubmitrule.StatusEQ(esportsclosesubmitrule.StatusActive),
	).SetStatus(esportsclosesubmitrule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsCloseSubmitRule.Update().Where(
		esportsclosesubmitrule.IDEQ(id),
		esportsclosesubmitrule.StatusEQ(esportsclosesubmitrule.StatusDraft),
	).SetStatus(esportsclosesubmitrule.StatusActive).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseSubmitRule.Get(ctx, id)
}

// rollbackCloseSubmitRule 回滚到指定版本：以该行配置为模板新建 max version+1 的直接 active 行，
// 并把原 active 转 history（原历史行不可变，）。
func rollbackCloseSubmitRule(ctx context.Context, db *ent.Client, id, adminID int) (*ent.EsportsCloseSubmitRule, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	source, err := tx.EsportsCloseSubmitRule.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	latest, err := tx.EsportsCloseSubmitRule.Query().Where(
		esportsclosesubmitrule.CodeEQ(source.Code),
	).Order(ent.Desc(esportsclosesubmitrule.FieldVersion)).First(ctx)
	if err != nil {
		return nil, err
	}
	created, err := tx.EsportsCloseSubmitRule.Create().SetCode(source.Code).SetName(source.Name).
		SetScope(source.Scope).SetScopeKey(source.ScopeKey).SetMode(source.Mode).
		SetMinSubmitRatioBp(source.MinSubmitRatioBp).SetAllowSubstitute(source.AllowSubstitute).
		SetOnMissing(source.OnMissing).SetVersion(latest.Version + 1).
		SetStatus(esportsclosesubmitrule.StatusActive).
		SetCreatedByAdminID(adminID).SetNote(source.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsCloseSubmitRule.Update().Where(
		esportsclosesubmitrule.CodeEQ(source.Code),
		esportsclosesubmitrule.StatusEQ(esportsclosesubmitrule.StatusActive),
		esportsclosesubmitrule.IDNEQ(created.ID),
	).SetStatus(esportsclosesubmitrule.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsCloseSubmitRule.Get(ctx, created.ID)
}

// listCloseSubmitRules 列出提交规则（按 ID 倒序）。
func listCloseSubmitRules(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsCloseSubmitRule, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsCloseSubmitRule.Query().
		Order(ent.Desc(esportsclosesubmitrule.FieldID)).Limit(limit).All(ctx)
}

// resolveCloseSubmitRule 解析订单适用的生效提交规则：sku > game > global，同层取 version 最大；
// 无命中返回 nil, nil（不校验提交达标情况）。
func resolveCloseSubmitRule(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (*ent.EsportsCloseSubmitRule, error) {
	active, err := db.EsportsCloseSubmitRule.Query().Where(
		esportsclosesubmitrule.StatusEQ(esportsclosesubmitrule.StatusActive),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	pick := func(scope esportsclosesubmitrule.Scope, key string) *ent.EsportsCloseSubmitRule {
		var best *ent.EsportsCloseSubmitRule
		for _, item := range active {
			if item.Scope != scope || item.ScopeKey != key {
				continue
			}
			if best == nil || item.Version > best.Version {
				best = item
			}
		}
		return best
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err == nil {
		if item := pick(esportsclosesubmitrule.ScopeSku, sku.Code); item != nil {
			return item, nil
		}
		if sku.CategoryID != nil {
			if category, err := db.EsportsProductCategory.Get(ctx, *sku.CategoryID); err == nil {
				if game := strings.TrimSpace(category.Game); game != "" {
					if item := pick(esportsclosesubmitrule.ScopeGame, game); item != nil {
						return item, nil
					}
				}
			}
		}
	}
	return pick(esportsclosesubmitrule.ScopeGlobal, ""), nil
}

// submitRequiredCount 按模式算「最少提交成员数」：multi 全部 / custom 按比例向上取整 / single 单人单。
// 依据（功能列表 ）：单人单本人提交，临时多人单所有实际服务成员逐人提交。
// 因此 mode=single 只表达「单人单由本人提交」，**不能**把多人单放宽成「任一人提交即可」——
// 只要订单有多个服务段仍返回 total。原实现 single 一律返回 1，等于允许一次提交覆盖多人，
// 与上述规则冲突，此处收严；代价是 multi 与 single 在多席订单上行为一致（single 退化为
// 「单席订单的多人提交口径」），但不会让任何一席的上报凭空被免除。
// 返回 0 表示不要求（custom 比例为 0，或订单没有服务段）。
func submitRequiredCount(rule *ent.EsportsCloseSubmitRule, total int) int {
	if total <= 0 {
		return 0
	}
	switch rule.Mode {
	case esportsclosesubmitrule.ModeSingle:
		return total
	case esportsclosesubmitrule.ModeCustom:
		if rule.MinSubmitRatioBp <= 0 {
			return 0
		}
		required := (total*rule.MinSubmitRatioBp + 9999) / 10000
		if required < 1 {
			required = 1
		}
		if required > total {
			required = total
		}
		return required
	default:
		return total
	}
}

// segmentSubmitted 判定陪玩服务段是否构成一次有效「个人上报」。
func segmentSubmitted(segment *ent.EsportsServiceSegment) bool {
	return segment.Status == esportsservicesegment.StatusFinished &&
		(segment.ReportedUnits > 0 || strings.TrimSpace(segment.EvidenceRef) != "")
}

// requireAllSegmentsSubmitted 未配置提交规则时的默认口径（③）：
// 多人单（服务段 > 1）要求每一名陪玩都形成有效上报；单人单沿用既有口径（单段结单条件已覆盖）。
func requireAllSegmentsSubmitted(segments []*ent.EsportsServiceSegment) error {
	if len(segments) <= 1 {
		return nil
	}
	missing := make([]string, 0, len(segments))
	for _, segment := range segments {
		if !segmentSubmitted(segment) {
			missing = append(missing, strconv.Itoa(segment.SeatIndex))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w：多人单未配置提交规则时须逐人上报，第 %s 席尚未上报，无法结单",
		errInvalidInput, strings.Join(missing, "、"))
}

// closeSubstituteReport 是客服代提交的一条上报（席位号 + 服务量 + 凭证）。
type closeSubstituteReport struct {
	SeatIndex     int    `json:"seat_index"`
	ReportedUnits int64  `json:"reported_units"`
	EvidenceRef   string `json:"evidence_ref"`
}

// unsubmittedMember 是订单中尚未形成有效上报的陪玩服务段。
type unsubmittedMember struct {
	SeatIndex int `json:"seat_index"`
	PlayerID  int `json:"player_id"`
}

// unsubmittedMembers 列出订单里未上报的服务段（席位号 + 陪玩 ID，按席位升序）。
func unsubmittedMembers(ctx context.Context, db *ent.Client, orderID int) ([]unsubmittedMember, error) {
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).Order(ent.Asc(esportsservicesegment.FieldSeatIndex)).All(ctx)
	if err != nil {
		return nil, err
	}
	missing := make([]unsubmittedMember, 0, len(segments))
	for _, segment := range segments {
		if !segmentSubmitted(segment) {
			missing = append(missing, unsubmittedMember{SeatIndex: segment.SeatIndex, PlayerID: segment.PlayerID})
		}
	}
	return missing, nil
}

// evidenceImageAttachmentIDs 解析单条 evidence_ref 里的图片附件 ID（JSON 数组形态，如 [11,12]），
// 按 ID 去重、忽略非正数；非数组 / 非法 JSON / 历史 oss:// 与纯文本一律返回空。
// 用途：判定代提交凭证是否真的带图片——自由文本不算图片凭证（审核报告 ）。
func evidenceImageAttachmentIDs(ref string) []int {
	ref = strings.TrimSpace(ref)
	if ref == "" || !strings.HasPrefix(ref, "[") {
		return nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(ref), &ids); err != nil {
		return nil
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// closeSubmitMinEvidenceImages 返回订单生效结单规则要求的最少服务凭证图片张数（默认项
// 「服务截图不少于 N 张」）。未配置生效结单规则时返回 0：此时凭证张数仍由结单条件统一判定
// （历史文本凭证按 1 张计），不对代提交 / 视为已提交额外加码，避免存量单被新口径追溯拦截。
func closeSubmitMinEvidenceImages(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) (int, error) {
	rule, err := resolveCloseRule(ctx, db, order)
	if err != nil || rule == nil {
		return 0, err
	}
	return effectiveCloseConditions(rule).MinEvidence, nil
}

// applySubstituteReports 客服代未上报成员补填服务段上报（代提交）：逐条写回并留痕。
// minImages 为生效结单规则要求的最少图片张数：>0 时每条代提交的凭证必须是不少于该张数的
// 图片附件 ID（JSON 数组），纯文本凭证不能替代图片，否则「代提交」就成了绕过
// 「服务截图不少于 N 张」的通道（审核报告 ）。
func applySubstituteReports(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, adminID int,
	segments []*ent.EsportsServiceSegment, substitutes []closeSubstituteReport, minImages int) error {
	bySeat := make(map[int]*ent.EsportsServiceSegment, len(segments))
	for _, segment := range segments {
		bySeat[segment.SeatIndex] = segment
	}
	seen := map[int]bool{}
	for _, item := range substitutes {
		evidence := strings.TrimSpace(item.EvidenceRef)
		if item.SeatIndex < 1 || item.ReportedUnits < 0 || len([]rune(evidence)) > 255 || seen[item.SeatIndex] {
			return fmt.Errorf("%w：代提交内容不合法或席位重复", errInvalidInput)
		}
		seen[item.SeatIndex] = true
		segment := bySeat[item.SeatIndex]
		if segment == nil {
			return fmt.Errorf("%w：订单不存在第 %d 个席位的服务段", errInvalidInput, item.SeatIndex)
		}
		if segmentSubmitted(segment) {
			return fmt.Errorf("%w：第 %d 个席位已由陪玩上报，无需代提交", errInvalidInput, item.SeatIndex)
		}
		if item.ReportedUnits <= 0 && evidence == "" {
			return fmt.Errorf("%w：第 %d 个席位的代提交须填服务量或服务凭证", errInvalidInput, item.SeatIndex)
		}
		if minImages > 0 {
			if got := len(evidenceImageAttachmentIDs(evidence)); got < minImages {
				return fmt.Errorf("%w：结单规则要求至少 %d 张服务凭证图片，第 %d 个席位的代提交当前只有 %d 张图片（文本凭证不能替代图片），还差 %d 张",
					errInvalidInput, minImages, item.SeatIndex, got, minImages-got)
			}
		}
		if _, err := tx.EsportsServiceSegment.UpdateOneID(segment.ID).
			SetReportedUnits(item.ReportedUnits).SetEvidenceRef(evidence).AddVersion(1).Save(ctx); err != nil {
			return err
		}
		// 订单事件按 (order_id, order_version) 唯一：代提交留痕必须写在 bump 之后的新版本上。
		// 待结单订单的当前 version 必然已被「进入待结单」那条事件占用，用读取时的旧版本号写事件
		// 必然撞唯一索引 → 结单返回 409「业务状态已变化」，代提交 / 视为已提交两条路径全部结不了。
		newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
		if err != nil {
			return err
		}
		// 版本号前移后，调用方（closeOrder / finalizeCloseOrderIn）必须看到新版本才能继续
		// 做条件更新与后续事件，故就地回写实体。
		order.Version = newVersion
		if _, err := tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
			SetOrderVersion(newVersion).SetEventType("order.service_submitted_by_admin").
			SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
			SetDetails(map[string]interface{}{
				"seat_index": item.SeatIndex, "reported_units": item.ReportedUnits, "evidence_ref": evidence,
			}).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// applyCloseSubmitRule 结单时按生效提交规则校验陪玩上报达标情况。
// 规则允许代提交时先用 substitutes 补填未上报成员再复核；仍未达标按 on_missing 阻塞 / 提醒 / 视为已提交。
// ③：未配置任何生效提交规则时，多人单（服务段 > 1）仍要求逐人上报，
// 避免「没配规则 = 谁都不用上报」把多人单的结单口径放空。
func applyCloseSubmitRule(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder, adminID int,
	substitutes []closeSubstituteReport) error {
	rule, err := resolveCloseSubmitRule(ctx, tx.Client(), order)
	if err != nil {
		return err
	}
	segments, err := tx.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(order.ID),
	).Order(ent.Asc(esportsservicesegment.FieldSeatIndex)).All(ctx)
	if err != nil {
		return err
	}
	if rule == nil {
		return requireAllSegmentsSubmitted(segments)
	}
	required := submitRequiredCount(rule, len(segments))
	if required == 0 {
		return nil
	}
	// 审核报告 ：代提交 / 视为已提交不能让自由文本凭证绕过「服务截图不少于 N 张」。
	// 仅在订单确实配了生效结单规则时加码（未配置时沿用结单条件既有口径）。
	minImages, err := closeSubmitMinEvidenceImages(ctx, tx.Client(), order)
	if err != nil {
		return err
	}
	countSubmitted := func(items []*ent.EsportsServiceSegment) int {
		submitted := 0
		for _, item := range items {
			if segmentSubmitted(item) {
				submitted++
			}
		}
		return submitted
	}
	submitted := countSubmitted(segments)
	if submitted < required && rule.AllowSubstitute && len(substitutes) > 0 {
		if err = applySubstituteReports(ctx, tx, order, adminID, segments, substitutes, minImages); err != nil {
			return err
		}
		if segments, err = tx.EsportsServiceSegment.Query().Where(
			esportsservicesegment.OrderIDEQ(order.ID),
		).All(ctx); err != nil {
			return err
		}
		submitted = countSubmitted(segments)
	}
	if submitted >= required {
		return nil
	}
	details := map[string]interface{}{
		"mode": string(rule.Mode), "required": required,
		"submitted": submitted, "total": len(segments),
	}
	switch rule.OnMissing {
	case esportsclosesubmitrule.OnMissingAutoAccept:
		// 视为已提交：不阻塞结单，只留痕。但同样不得让「未传图片」借「视为已提交」绕开图片底线：
		// 配了结单规则时，本单图片凭证张数必须已达规则要求（文本凭证不计入图片张数）。
		if minImages > 0 {
			images, err := serviceEvidenceAttachmentIDs(ctx, tx.Client(), order.ID)
			if err != nil {
				return err
			}
			if len(images) < minImages {
				return fmt.Errorf("%w：结单规则「%s」要求至少 %d 张服务凭证图片，本单当前只有 %d 张图片凭证（文本凭证不能替代图片），还差 %d 张，不能按「视为已提交」放行",
					errInvalidInput, rule.Code, minImages, len(images), minImages-len(images))
			}
		}
		// 同上：先 bump 占一个新版本，再用新版本号写事件，否则撞 (order_id, order_version) 唯一索引。
		newVersion, err := bumpOrder(ctx, tx, order.ID, order.Version, "")
		if err != nil {
			return err
		}
		order.Version = newVersion
		_, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
			SetOrderVersion(newVersion).SetEventType("order.close_submit_auto_accepted").
			SetActorType("system").SetActorID("system").SetDetails(details).Save(ctx)
		return err
	case esportsclosesubmitrule.OnMissingRemind:
		// 放行并写一条事务外盒催办提醒。
		_, err = tx.EsportsOutbox.Create().
			SetEventKey("order.close_submit_remind:" + order.OrderNo + ":" + strconv.Itoa(submitted)).
			SetEventType("order.close_submit_remind").SetAggregateRef(order.OrderNo).
			SetPayload(details).Save(ctx)
		return err
	default:
		return fmt.Errorf("%w：陪玩服务段上报未达提交规则要求（需 %d / 共 %d，当前 %d），无法结单",
			errInvalidInput, required, len(segments), submitted)
	}
}

// closeSubmitRuleSummary 是提交规则在结单视图中的摘要，供管理端提示。
func closeSubmitRuleSummary(rule *ent.EsportsCloseSubmitRule, total int) map[string]interface{} {
	if rule == nil {
		return nil
	}
	return map[string]interface{}{
		"id": rule.ID, "code": rule.Code, "name": rule.Name, "version": rule.Version,
		"mode": string(rule.Mode), "min_submit_ratio_bp": rule.MinSubmitRatioBp,
		"allow_substitute": rule.AllowSubstitute, "on_missing": string(rule.OnMissing),
		"required": submitRequiredCount(rule, total),
	}
}
