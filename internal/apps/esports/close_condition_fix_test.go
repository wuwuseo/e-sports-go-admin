package esports

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsservicesegment"

	"go-admin/internal/apps/esports/schematier"
)

// closeConditionFactsMust 取订单结单事实快照，失败直接终止用例。
func closeConditionFactsMust(t *testing.T, ctx context.Context, db *ent.Client, order *ent.EsportsOrder) closeConditionFacts {
	t.Helper()
	facts, err := closeConditionFactsOf(ctx, db, order)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

// seedExtraEvidenceSegment 给订单追加一条已完成服务段（同订单同一陪玩唯一，故换 player_id），
// 用于验证凭证张数的跨段汇总口径。
func seedExtraEvidenceSegment(t *testing.T, db *ent.Client, orderID, playerID int, evidenceRef string) {
	t.Helper()
	now := time.Now().UTC()
	db.EsportsServiceSegment.Create().SetOrderID(orderID).SetPlayerID(playerID).SetSeatIndex(playerID).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-time.Minute)).SetEndedAt(now).
		SetSystemSeconds(60).SetReportedUnits(1).SetEvidenceRef(evidenceRef).SaveX(t.Context())
}

// 服务凭证张数（evidence_count）按图片计：一个服务段传两张图计 2 张，
// JSON 数组按附件 ID 跨段去重，历史文本引用整段按 1 张计，空引用不计。
func TestCloseConditionEvidenceCountByImageRefs(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_evidence_count")
	defer db.Close()
	ctx := t.Context()

	// 单个服务段上传两张图片 → 2 张（旧口径按「有凭证的服务段数」只算 1）。
	single := seedSimpleCloseableOrder(t, db, "close-ev-images", 1000, 11)
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(single.ID)).
		SetEvidenceRef("[11,12]").SaveX(ctx)
	if facts := closeConditionFactsMust(t, ctx, db, single); facts.EvidenceCount != 2 {
		t.Fatalf("单段两张图片应计 2 张，got=%d", facts.EvidenceCount)
	}

	// 两段：JSON 数组 1 张 + 历史文本引用按 1 张计 → 2 张。
	mixed := seedSimpleCloseableOrder(t, db, "close-ev-mixed", 1000, 11)
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(mixed.ID)).
		SetEvidenceRef("[13]").SaveX(ctx)
	seedExtraEvidenceSegment(t, db, mixed.ID, 2, "legacy.png")
	if facts := closeConditionFactsMust(t, ctx, db, mixed); facts.EvidenceCount != 2 {
		t.Fatalf("JSON 1 张 + 文本 1 张应计 2 张，got=%d", facts.EvidenceCount)
	}

	// 跨段重复的附件 ID 只算一张：11,12 + 12,13 → 3 张。
	dup := seedSimpleCloseableOrder(t, db, "close-ev-dup", 1000, 11)
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(dup.ID)).
		SetEvidenceRef("[11,12]").SaveX(ctx)
	seedExtraEvidenceSegment(t, db, dup.ID, 3, "[12,13]")
	if facts := closeConditionFactsMust(t, ctx, db, dup); facts.EvidenceCount != 3 {
		t.Fatalf("跨段重复附件 ID 去重后应计 3 张，got=%d", facts.EvidenceCount)
	}

	// 空引用不计。
	empty := seedSimpleCloseableOrder(t, db, "close-ev-empty", 1000, 11)
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(empty.ID)).
		SetEvidenceRef("").SaveX(ctx)
	if facts := closeConditionFactsMust(t, ctx, db, empty); facts.EvidenceCount != 0 {
		t.Fatalf("空凭证引用应计 0 张，got=%d", facts.EvidenceCount)
	}
}

// 自定义条件树不得替换默认底线：规则只写「服务量 ≥1」，把订单凭证清空后
// 默认的「凭证 ≥MinEvidence」底线仍要兜住结单，条件状态逐项明细同时给出两侧叶子。
func TestCustomConditionTreeCannotDropDefaultEvidenceFloor(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_floor_evidence")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	tree := schematier.CloseConditionNode{Field: "service_units", Operator: ">=", Value: num(1)}
	rule, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "TREE-UNITS-ONLY", Name: "仅服务量", Scope: "global", MinEvidence: 1,
		ConditionTree: &tree,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedSimpleCloseableOrder(t, db, "close-floor-evidence", 1000, 11)
	// 清空服务段凭证：自定义树只看服务量（1 ≥ 1 成立），默认底线必须兜底。
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(order.ID)).
		SetEvidenceRef("").SaveX(ctx)

	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "服务凭证张数") {
		t.Fatalf("默认凭证底线应拦截结单：%v", err)
	}
	status, err := orderCloseConditionStatus(ctx, db, order.ID)
	if err != nil || status.Satisfied || !status.TreeConfigured || status.RuleCode != "TREE-UNITS-ONLY" {
		t.Fatalf("条件状态不正确：%+v err=%v", status, err)
	}
	// 合并后逐项列出默认底线叶子 + 自定义树叶子，默认底线排在前面。
	if len(status.Items) != 2 || status.Items[0].Field != "evidence_count" || status.Items[0].Satisfied ||
		status.Items[1].Field != "service_units" || !status.Items[1].Satisfied {
		t.Fatalf("合并后应逐项列出默认底线与自定义树叶子：%+v", status.Items)
	}

	// 同一条规则下补齐 1 张凭证（服务量已达标）→ 默认底线与自定义树同时满足，可结单。
	db.EsportsServiceSegment.Update().Where(esportsservicesegment.OrderIDEQ(order.ID)).
		SetEvidenceRef("proof.png").SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("补齐凭证后应可结单：%v", err)
	}
	status, err = orderCloseConditionStatus(ctx, db, order.ID)
	if err != nil || !status.Satisfied || len(status.Items) != 2 {
		t.Fatalf("补齐凭证后条件状态应满足：%+v err=%v", status, err)
	}
}

// 自定义条件树比默认更严格（时长 ≥9999 秒）时仍拦截：
// 合并求值下任一侧不满足都不可结单，逐项明细能区分默认底线叶子（满足）与自定义叶子（不满足）。
func TestCustomConditionTreeStricterThanDefaultStillBlocks(t *testing.T) {
	db := openCloseRuleDB(t, "esports_close_floor_strict")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	tree := schematier.CloseConditionNode{Field: "service_seconds", Operator: ">=", Value: num(9999)}
	rule, err := createCloseRule(ctx, db, closeRuleInput{
		Code: "TREE-STRICT-TIME", Name: "时长加严", Scope: "global", MinEvidence: 1,
		ConditionTree: &tree,
	}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCloseRule(ctx, db, rule.ID, now); err != nil {
		t.Fatal(err)
	}
	// seedSimpleCloseableOrder：600 秒 + 文本凭证，默认底线全部满足。
	order := seedSimpleCloseableOrder(t, db, "close-floor-strict", 1000, 11)

	if _, err = closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); !errors.Is(err, errInvalidInput) ||
		!strings.Contains(err.Error(), "系统服务时长") {
		t.Fatalf("加严的自定义条件应拦截结单：%v", err)
	}
	status, err := orderCloseConditionStatus(ctx, db, order.ID)
	if err != nil || status.Satisfied {
		t.Fatalf("加严条件下条件状态应不满足：%+v err=%v", status, err)
	}
	if len(status.Items) != 2 || !status.Items[0].Satisfied || status.Items[1].Satisfied {
		t.Fatalf("逐项明细应区分默认底线与自定义叶子：%+v", status.Items)
	}
}
