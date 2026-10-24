package esports

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/esportsledgerallocation"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/role"
)

// 账本核验新增维度：「钱包流水求和 == 批次账本求和」。
//
// 口径（逐账户比对；登记账户 = customer_balance / player_catfood）：
//
//	钱包净额   = Σ wallet_tx(principal_delta + bonus_delta)   （该钱包账户）
//	批次净额   = Σ ledger_entry.delta                          （对应批次账本账户）
//	跨账户消费 = Σ ledger_entry.delta（account_type=customer_order_prepayment，account_id=同一归属人）
//	             余额核销订单 / 加钟时，钱从余额转到「订单预付」账户，批次账户本身不再记一条；
//	             该项把这笔跨账户记账还原回比对口径（仅客户余额适用）。
//	未登记净额 = Σ wallet_tx(principal_delta + bonus_delta)，仅取「在全部账本里都找不到对应条目」的流水
//	             （按 source_id = 流水号 或 流水主键匹配）；这些是尚未落批次账本的资金流动，
//	             对预付账户的出账另行作为 wallet_outflow_unbooked 报出，避免把口径差异伪装成账不平。
//
// 报错条件：钱包净额 != 批次净额 − 跨账户消费 + 未登记净额。
// 正常数据下恒等；一旦出现「有流水无账本」「有账本无流水」「两侧金额不等」都会报出。
//
// 必须记住的映射差异：客户余额核销订单的批次记账落在 `customer_order_prepayment`，
// 故需要跨账户还原项。曾用于储值卡钱包账户（stored_value_card）与预付款批次账户
// （stored_value_prepay）对齐的映射已随该功能下线（2026-10-20）移除。

// ledgerAnomalyWalletBatchMismatch 钱包流水求和与批次账本求和不一致。
const ledgerAnomalyWalletBatchMismatch = "wallet_batch_sum_mismatch"

// ledgerAnomalyWalletOutflowUnbooked 预付账户的出账没有登记批次账本 / 分摊明细。
const ledgerAnomalyWalletOutflowUnbooked = "wallet_outflow_unbooked"

// ledgerPrepayAccountType 余额核销订单时承接资金的账本账户类型。
const ledgerPrepayAccountType = "customer_order_prepayment"

// ledgerWalletSumScan 是单个账户的一次比对结果。
type ledgerWalletSumScan struct {
	accountType   string
	accountID     string
	walletCents   int64
	batchCents    int64
	crossCents    int64
	unbookedCents int64
	unbooked      []string
}

// ledgerBatchAccountType 把钱包账户类型映射到其批次账本账户类型：余额与猫粮两侧同名，
// 储值卡（唯一不一致的一处）已随功能下线（2026-10-20），不再参与核验扫描。
func ledgerBatchAccountType(walletAccountType string) string {
	return walletAccountType
}

// scanWalletLedgerSum 对单个账户执行「钱包流水求和 == 批次账本求和」核验。
// 由余额核验的同一轮账户扫描顺带调用：两者账户集合完全一致（登记钱包账户），
// 复用同一游标与单轮上限，不新增配置键、不重复扫表。
//
// 本函数只负责「判定 + 记录异常」，不直接发通知：通知出口由调用方（ledger_reconcile.go 的
// 账户 / 卡扫描）把该账户本轮新增的全部异常（余额类 + 求和类）合并成一条事件后统一写出，
// 确保所有异常种类走同一个出口、同一小时窗口只提醒一次。
func scanWalletLedgerSum(ctx context.Context, db *ent.Client,
	result *ledgerReconcileResult, accountType, accountID string) error {
	result.ScannedWalletSums++
	return checkWalletLedgerSum(ctx, db, result, ledgerWalletSumScan{
		accountType: accountType, accountID: accountID,
	})
}

// checkWalletLedgerSum 汇总单个账户的两侧口径、比对并输出异常。
func checkWalletLedgerSum(ctx context.Context, db *ent.Client,
	result *ledgerReconcileResult, scan ledgerWalletSumScan) error {
	rows, err := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountType(scan.accountType)),
		esportswallettx.OwnerIDEQ(atoiOrZero(scan.accountID)),
	).All(ctx)
	if err != nil {
		return err
	}
	booked, err := walletRowsBookedInLedger(ctx, db, rows)
	if err != nil {
		return err
	}
	prepaid := ledgerPrepayTracker(scan.accountType)
	for i, row := range rows {
		delta := row.PrincipalDeltaCents + row.BonusDeltaCents
		scan.walletCents += delta
		if booked[i] {
			continue
		}
		scan.unbookedCents += delta
		if delta >= 0 || !prepaid {
			continue
		}
		// 预付性质账户的出账必须能在批次层追溯到（负向账本条目或分摊明细至少有一处）。
		allocated, err := walletRowHasAllocation(ctx, db, scan, row)
		if err != nil {
			return err
		}
		if allocated {
			continue
		}
		scan.unbooked = append(scan.unbooked, fmt.Sprintf(
			"流水 %s（%s#%s）出账 %d 分未登记批次账本或分摊明细",
			row.TxNo, row.SourceType, row.SourceID, -delta))
	}
	batchAccount := ledgerBatchAccountType(scan.accountType)
	entries, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.AccountTypeEQ(batchAccount),
		esportsledgerentry.AccountIDEQ(scan.accountID),
	).Select(esportsledgerentry.FieldDeltaCents).All(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		scan.batchCents += entry.DeltaCents
	}
	if scan.accountType == walletCustomerBalance {
		cross, err := db.EsportsLedgerEntry.Query().Where(
			esportsledgerentry.AccountTypeEQ(ledgerPrepayAccountType),
			esportsledgerentry.AccountIDEQ(scan.accountID),
		).Select(esportsledgerentry.FieldDeltaCents).All(ctx)
		if err != nil {
			return err
		}
		for _, entry := range cross {
			scan.crossCents += entry.DeltaCents
		}
	}
	expected := scan.batchCents - scan.crossCents + scan.unbookedCents
	if scan.walletCents != expected {
		result.add(ledgerAnomalyWalletBatchMismatch, scan.accountType, scan.accountID,
			fmt.Sprintf("钱包流水求和 %d 分，批次账本求和 %d 分（跨账户消费 %d 分，未登记 %d 分），差额 %d 分",
				scan.walletCents, scan.batchCents, scan.crossCents, scan.unbookedCents,
				scan.walletCents-expected))
	}
	for _, detail := range scan.unbooked {
		result.add(ledgerAnomalyWalletOutflowUnbooked, scan.accountType, scan.accountID, detail)
	}
	return nil
}

// ledgerPrepayTracker 只有预付性质账户参与「出账必须批次可追溯」的判定：
// 客户余额是预付款，陪玩猫粮是结算发放（不走预付款批次）。
func ledgerPrepayTracker(accountType string) bool {
	return accountType == walletCustomerBalance
}

// 账本对账异常的通知出口（P1-21 / ）。
//
// 口径：**全部**账本核验异常种类都经本处唯一的出口函数写通知事件，而不是只写一条聚合事件
// ledger.reconcile_anomaly。覆盖的异常种类（见 ledger_reconcile.go / 本文件的种类常量）：
//   - account_negative_balance     账户本金 / 赠送余额为负；
//   - allocation_batch_missing     分摊引用的批次账本不存在或非正向；
//   - batch_over_allocated         批次被分摊额超过批次金额；
//   - wallet_outflow_unbooked      预付账户出账未登记批次账本 / 分摊明细；
//   - wallet_batch_sum_mismatch    钱包流水求和与批次账本求和不等。
//
// 出口约定：
//   - event_type 固定以 ledger. 前缀开头（ledger.reconcile_anomaly_notice），供通知分拣按前缀识别；
//   - 收件人字段沿用既有管理员定向通知的约定：payload.admin_id（逐收件人一条事件），
//     另带 recipient_roles 标注来源角色（finance 财务 / super_admin 超管）便于审计；
//   - aggregate_ref 固定 "ledger"（账本无业务单号），正文由通知层按事件类型生成；
//   - 幂等键 = 小时窗口 + 收件人 + 账户 + 异常种类集合（event_key 唯一索引兜底）：同一异常在同一小时内
//     只发一次，异常持续存在时最多每小时提醒一次，避免重复轰炸；同一账户一轮内的多条异常合并进同一条事件。
//
// 需要另一处配合（详见 w4/misc-report.md）：notification_outbox.go 的 notifyChannelTargets 需增加
// {"ledger.", []string{notifyChannelWecomApp}}，notification_recipients.go 需增加
// `case strings.HasPrefix(eventType, "ledger."): return resolveOrderAdminNotificationRecipients(...)`，
// 该函数已按 payload.admin_id 解析收件人（账本事件没有关联订单，回落到载荷里的 admin_id）。
const (
	// ledgerReconcileNoticeEventType 账本对账异常的通知事件类型（ledger. 前缀供通知分拣识别）。
	ledgerReconcileNoticeEventType = "ledger.reconcile_anomaly_notice"
	// ledgerReconcileNoticeRolesField 载荷中的收件人角色标记字段。
	ledgerReconcileNoticeRolesField = "recipient_roles"
	// ledgerReconcileNoticeBucket 同一账户同一收件人的告警去重窗口（按小时）。
	ledgerReconcileNoticeBucket = time.Hour
	// ledgerReconcileNoticeQueryPath 异常查询接口（GET /admin/ledger-reconcile-anomalies），
	// 写进通知载荷便于财务 / 超管直接定位复核入口。
	ledgerReconcileNoticeQueryPath = "/api/v1/esports/admin/ledger-reconcile-anomalies"
)

// ledgerReconcileNoticeRecipient 是一条对账异常通知的收件人。
type ledgerReconcileNoticeRecipient struct {
	AdminID int
	Role    string
}

// publishLedgerReconcileNotices 是账本核验异常的**唯一通知出口**（P1-21 / ）：
// 传入某个账户本轮新增的异常（可能同时含多种 kind），为每个收件人写一条通知事件。
// 收件人优先取财务角色成员（app.esports.finance_report_role_names 配置的启用角色下的启用管理员），
// 未配置或没有成员时回落启用中的超级管理员；两者都取不到时不做通知（异常仍可经
// GET /admin/ledger-reconcile-anomalies 查到，不静默丢失）。
// 同一小时窗口内同一收件人 + 同一账户 + 同一异常种类集合只写一条，靠 outbox event_key 唯一索引兜底。
// 键里带种类集合是必需的：余额 / 求和的扫描与分摊 / 批次的扫描是两个独立阶段，
// 同一账户可能先报求和异常、后报超额分摊，若只按账户去重，后一类异常会被幂等键吃掉。
func publishLedgerReconcileNotices(ctx context.Context, db *ent.Client, accountType, accountID string,
	anomalies []ledgerAnomaly, now time.Time) error {
	if len(anomalies) == 0 {
		return nil
	}
	recipients, err := ledgerReconcileNoticeRecipients(ctx, db)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return nil
	}
	bucket := now.UTC().Truncate(ledgerReconcileNoticeBucket).Format("2006010215")
	kindSet := make([]string, 0, len(anomalies))
	kindSeen := make(map[string]bool, len(anomalies))
	for _, item := range anomalies {
		if kindSeen[item.Kind] {
			continue
		}
		kindSeen[item.Kind] = true
		kindSet = append(kindSet, item.Kind)
	}
	sort.Strings(kindSet)
	kindsKey := strings.Join(kindSet, "+")
	details := make([]map[string]interface{}, 0, len(anomalies))
	for _, item := range anomalies {
		details = append(details, map[string]interface{}{
			"kind": item.Kind, "account_type": item.AccountType,
			"account_id": item.AccountID, "detail": item.Detail,
		})
	}
	for _, recipient := range recipients {
		payload := map[string]interface{}{
			// 收件人字段：与既有管理员定向通知一致，通知分拣直接读 admin_id。
			"admin_id":                      recipient.AdminID,
			ledgerReconcileNoticeRolesField: []string{recipient.Role},
			"account_type":                  accountType,
			"account_id":                    accountID,
			"anomaly_kinds":                 kindSet,
			"anomaly_count":                 len(anomalies),
			"anomalies":                     details,
			"occurred_at":                   now.UTC().Format(time.RFC3339Nano),
			"query_endpoint":                ledgerReconcileNoticeQueryPath,
		}
		_, err = db.EsportsOutbox.Create().
			SetEventKey(ledgerReconcileNoticeEventType + ":" + bucket + ":" +
				strconv.Itoa(recipient.AdminID) + ":" + accountType + ":" + accountID + ":" + kindsKey).
			SetEventType(ledgerReconcileNoticeEventType).SetAggregateRef("ledger").
			SetPayload(payload).Save(ctx)
		if ent.IsConstraintError(err) {
			// 本小时窗口内已提醒过该收件人的同类异常，跳过即可（异常仍在核验事件与查询接口里）。
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ledgerReconcileNoticeRecipients 解析账本对账异常的通知收件人（P1-21）：
// 财务角色成员优先，未配置财务角色或没有启用成员时回落启用中的超管。
func ledgerReconcileNoticeRecipients(ctx context.Context, db *ent.Client) ([]ledgerReconcileNoticeRecipient, error) {
	if finance := ledgerFinanceAdminIDs(ctx, db); len(finance) > 0 {
		recipients := make([]ledgerReconcileNoticeRecipient, 0, len(finance))
		for _, id := range finance {
			recipients = append(recipients, ledgerReconcileNoticeRecipient{AdminID: id, Role: "finance"})
		}
		return recipients, nil
	}
	supers, err := db.Admin.Query().Where(
		admin.IsSuperAdminEQ(true), admin.StatusEQ(admin.StatusActive),
	).Select(admin.FieldID).Order(ent.Asc(admin.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	recipients := make([]ledgerReconcileNoticeRecipient, 0, len(supers))
	for _, item := range supers {
		recipients = append(recipients, ledgerReconcileNoticeRecipient{AdminID: item.ID, Role: "super_admin"})
	}
	return recipients, nil
}

// ledgerFinanceAdminIDs 取财务角色下的启用管理员 ID（升序、去重）：
// 角色名取 app.esports.finance_report_role_names（与 canAccessFinanceReport 同一份配置），
// 只认启用角色 + 启用管理员的组合，缺失或读取失败时返回空表示「未配置财务角色」。
func ledgerFinanceAdminIDs(ctx context.Context, db *ent.Client) []int {
	names := financeReportRoleNames(ctx, db)
	if len(names) == 0 {
		return nil
	}
	roles, err := db.Role.Query().Where(
		role.NameIn(names...), role.StatusEQ(role.StatusActive),
	).Select(role.FieldID).Order(ent.Asc(role.FieldID)).All(ctx)
	if err != nil || len(roles) == 0 {
		return nil
	}
	roleIDs := make([]int, 0, len(roles))
	for _, item := range roles {
		roleIDs = append(roleIDs, item.ID)
	}
	bindings, err := db.AdminRole.Query().Where(
		adminrole.RoleIDIn(roleIDs...),
	).Select(adminrole.FieldAdminID).All(ctx)
	if err != nil || len(bindings) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(bindings))
	ids := make([]int, 0, len(bindings))
	for _, item := range bindings {
		if _, ok := seen[item.AdminID]; ok {
			continue
		}
		seen[item.AdminID] = struct{}{}
		ids = append(ids, item.AdminID)
	}
	sort.Ints(ids)
	admins, err := db.Admin.Query().Where(
		admin.IDIn(ids...), admin.StatusEQ(admin.StatusActive),
	).Select(admin.FieldID).All(ctx)
	if err != nil {
		return nil
	}
	active := make(map[int]struct{}, len(admins))
	for _, item := range admins {
		active[item.ID] = struct{}{}
	}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := active[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// walletRowHasAllocation 判断某笔出账是否有对应的批次分摊明细。
func walletRowHasAllocation(ctx context.Context, db *ent.Client, scan ledgerWalletSumScan,
	row *ent.EsportsWalletTx) (bool, error) {
	return db.EsportsLedgerAllocation.Query().Where(
		esportsledgerallocation.AccountTypeEQ(ledgerBatchAccountType(scan.accountType)),
		esportsledgerallocation.AccountIDEQ(scan.accountID),
		esportsledgerallocation.ConsumeSourceTypeEQ(row.SourceType),
		esportsledgerallocation.ConsumeSourceIDEQ(row.SourceID),
	).Exist(ctx)
}

// walletRowsBookedInLedger 标记哪些钱包流水在账本里有对应条目（按 source_id = 流水号 或 流水主键匹配，
// 不限定账户类型：余额核销订单的条目记在 customer_order_prepayment 账户下）。
func walletRowsBookedInLedger(ctx context.Context, db *ent.Client, rows []*ent.EsportsWalletTx) ([]bool, error) {
	booked := make([]bool, len(rows))
	if len(rows) == 0 {
		return booked, nil
	}
	indexBySource := map[string][]int{}
	keys := make([]string, 0, len(rows)*2)
	for i, row := range rows {
		for _, key := range []string{row.TxNo, strconv.Itoa(row.ID)} {
			if key == "" {
				continue
			}
			if _, ok := indexBySource[key]; !ok {
				keys = append(keys, key)
			}
			indexBySource[key] = append(indexBySource[key], i)
		}
	}
	const chunkSize = 200
	for start := 0; start < len(keys); start += chunkSize {
		end := min(start+chunkSize, len(keys))
		entries, err := db.EsportsLedgerEntry.Query().
			Where(esportsledgerentry.SourceIDIn(keys[start:end]...)).
			Select(esportsledgerentry.FieldSourceID).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			for _, i := range indexBySource[entry.SourceID] {
				booked[i] = true
			}
		}
	}
	return booked, nil
}

// atoiOrZero 解析账户 ID（账户 ID 一律为非负整数，解析失败按 0 处理，不会命中任何流水）。
func atoiOrZero(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// ledgerReconcileAnomalyView 是管理端可查的核验异常事件视图。
type ledgerReconcileAnomalyView struct {
	EventKey           string          `json:"event_key"`
	OccurredAt         string          `json:"occurred_at"`
	ScannedAccounts    int             `json:"scanned_accounts"`
	ScannedAllocations int             `json:"scanned_allocations"`
	ScannedBatches     int             `json:"scanned_batches"`
	ScannedWalletSums  int             `json:"scanned_wallet_sums"`
	ScanTruncated      bool            `json:"scan_truncated"`
	AnomalyOverflow    bool            `json:"anomaly_overflow"`
	AnomalyCount       int             `json:"anomaly_count"`
	Anomalies          []ledgerAnomaly `json:"anomalies"`
}

// listLedgerReconcileAnomalies 列出最近的账本核验异常事件（管理端可查）。
// 数据来源是核验任务写下的 ledger.reconcile_anomaly 外盒事件，按时间倒序。
func listLedgerReconcileAnomalies(ctx context.Context, db *ent.Client, limit int) ([]ledgerReconcileAnomalyView, error) {
	if limit < 1 || limit > 200 {
		limit = 20
	}
	events, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("ledger.reconcile_anomaly"),
	).Order(ent.Desc(esportsoutbox.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]ledgerReconcileAnomalyView, 0, len(events))
	for _, event := range events {
		payload := event.Payload
		view := ledgerReconcileAnomalyView{
			EventKey:           event.EventKey,
			OccurredAt:         stringField(payload, "occurred_at"),
			ScannedAccounts:    intField(payload, "scanned_accounts"),
			ScannedAllocations: intField(payload, "scanned_allocations"),
			ScannedBatches:     intField(payload, "scanned_batches"),
			ScannedWalletSums:  intField(payload, "scanned_wallet_sums"),
			ScanTruncated:      boolField(payload, "scan_truncated"),
			AnomalyOverflow:    boolField(payload, "anomaly_overflow"),
			AnomalyCount:       intField(payload, "anomaly_count"),
		}
		if raw, ok := payload["anomalies"].([]interface{}); ok {
			for _, item := range raw {
				entry, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				view.Anomalies = append(view.Anomalies, ledgerAnomaly{
					Kind: stringField(entry, "kind"), AccountType: stringField(entry, "account_type"),
					AccountID: stringField(entry, "account_id"), Detail: stringField(entry, "detail"),
				})
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func stringField(payload map[string]interface{}, key string) string {
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

func intField(payload map[string]interface{}, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return 0
}

func boolField(payload map[string]interface{}, key string) bool {
	value, _ := payload[key].(bool)
	return value
}
