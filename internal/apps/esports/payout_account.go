package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayerpayoutaccount"
	"go-admin/internal/global"
)

// 陪玩收款账户签约（云账户代发）：
// 本站不落完整卡号 / 账号，只保存云账户签约号与后四位脱敏串；
// 云账户 HTTP 接口与签名、签约回调在下一阶段实现，这里只维护本地账户状态并拼装签约托管地址。

const (
	// payoutProviderConfigKey 陪玩打款渠道：manual=人工打款（默认），yunzhanghu=云账户代发。
	payoutProviderConfigKey = "app.esports.payout_provider"
	// payoutProviderYunzhanghuConfigKey 云账户代发配置（JSON，密钥单独成键）。
	payoutProviderYunzhanghuConfigKey = "app.esports.payout_provider_yunzhanghu"

	payoutProviderManual     = "manual"
	payoutProviderYunzhanghu = "yunzhanghu"

	// payoutSignReturnPath 签约完成后回跳的陪玩钱包页路径。
	payoutSignReturnPath = "/player/wallet"
)

// yunzhanghuPayoutConfig 云账户代发配置（密钥不在 JSON 内，单独存系统配置键）。
type yunzhanghuPayoutConfig struct {
	Enabled    bool   `json:"enabled"`
	DealerID   string `json:"dealer_id"`
	AppKey     string `json:"app_key"`
	BaseURL    string `json:"base_url"`
	NotifyBase string `json:"notify_base"`
	SignBase   string `json:"sign_base"`
}

// payoutProviderKind 读取当前打款渠道：只有显式配置为 yunzhanghu 才启用云账户，
// 缺失 / 非法一律回落 manual，保持人工打款的既有行为。
// 名称与代发服务商适配器接口 payoutProvider 区分，避免同包内类型与函数重名。
func payoutProviderKind(ctx context.Context, db *ent.Client) string {
	if strings.TrimSpace(systemConfigValue(ctx, db, payoutProviderConfigKey)) == payoutProviderYunzhanghu {
		return payoutProviderYunzhanghu
	}
	return payoutProviderManual
}

// loadYunzhanghuPayoutConfig 读取云账户配置；缺失 / JSON 非法按「未启用」处理。
func loadYunzhanghuPayoutConfig(ctx context.Context, db *ent.Client) yunzhanghuPayoutConfig {
	raw := systemConfigValue(ctx, db, payoutProviderYunzhanghuConfigKey)
	if raw == "" {
		return yunzhanghuPayoutConfig{}
	}
	var cfg yunzhanghuPayoutConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return yunzhanghuPayoutConfig{}
	}
	return cfg
}

// validPayoutAccountType 云账户代发只支持银行卡 / 支付宝两类收款账户。
func validPayoutAccountType(accountType string) bool {
	return accountType == string(esportsplayerpayoutaccount.AccountTypeBankCard) ||
		accountType == string(esportsplayerpayoutaccount.AccountTypeAlipay)
}

// maskPayoutAccountNo 脱敏收款账号：完整卡号（长度 > 8 且含数字）只保留后四位并加 **** 前缀；
// 已脱敏短串或非数字内容原样返回。本站不存明文，云账户回调也应传脱敏串。
// ponytail: 仅按长度 + 是否含数字判断，不识别银行卡 / 支付宝账号的真实格式；
// 与服务商回调联调确认账号口径后再收紧。
func maskPayoutAccountNo(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) <= 8 || !strings.ContainsAny(raw, "0123456789") {
		return raw
	}
	return "****" + raw[len(raw)-4:]
}

// payoutAccountView 陪玩端收款账户视图：只给展示所需字段，不回传签约号。
type payoutAccountView struct {
	AccountType     string     `json:"account_type"`
	AccountName     string     `json:"account_name"`
	AccountNoMasked string     `json:"account_no_masked"`
	Status          string     `json:"status"`
	SignedAt        *time.Time `json:"signed_at"`
}

func payoutAccountViewOf(item *ent.EsportsPlayerPayoutAccount) payoutAccountView {
	return payoutAccountView{
		AccountType:     item.AccountType.String(),
		AccountName:     item.AccountName,
		AccountNoMasked: item.AccountNoMasked,
		Status:          item.Status.String(),
		SignedAt:        item.SignedAt,
	}
}

// listPlayerPayoutAccounts 列出某陪玩在云账户渠道下的签约账户（银行卡 / 支付宝各至多一条）。
func listPlayerPayoutAccounts(ctx context.Context, db *ent.Client, playerID int) ([]*ent.EsportsPlayerPayoutAccount, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsPlayerPayoutAccount.Query().Where(
		esportsplayerpayoutaccount.PlayerIDEQ(playerID),
		esportsplayerpayoutaccount.ProviderEQ(payoutProviderYunzhanghu),
	).Order(esportsplayerpayoutaccount.ByAccountType()).All(ctx)
}

// buildYunzhanghuSignURL 拼装签约托管地址：在 sign_base 上追加
// dealer_id / player_id / account_type / return_url 四个 query 参数。
// ponytail: 服务商签约协议尚未联调，这里按网页跳转口径实现；接入官方签约接口后
// 只需替换本函数（回跳地址口径随之调整），调用方与账户状态机不受影响。
func buildYunzhanghuSignURL(cfg yunzhanghuPayoutConfig, playerID int, accountType string) (string, error) {
	signBase := strings.TrimSpace(cfg.SignBase)
	base, err := url.Parse(signBase)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("%w：云账户签约地址配置不合法，请联系管理员检查系统配置", errInvalidInput)
	}
	returnBase := strings.TrimSuffix(strings.TrimSpace(cfg.NotifyBase), "/")
	if returnBase == "" {
		// 未配置回调域名前缀时用签约地址的 origin 兜底，至少保证签约页能回到本站。
		returnBase = base.Scheme + "://" + base.Host
	}
	query := base.Query()
	query.Set("dealer_id", strings.TrimSpace(cfg.DealerID))
	query.Set("player_id", strconv.Itoa(playerID))
	query.Set("account_type", accountType)
	query.Set("return_url", returnBase+payoutSignReturnPath)
	base.RawQuery = query.Encode()
	return base.String(), nil
}

// playerPayoutSignURL 生成签约地址：校验云账户开关与签约地址，本地无记录时先落一条 pending。
func playerPayoutSignURL(ctx context.Context, db *ent.Client, playerID int, accountType string) (*ent.EsportsPlayerPayoutAccount, string, error) {
	accountType = strings.TrimSpace(accountType)
	if playerID < 1 || !validPayoutAccountType(accountType) {
		return nil, "", errInvalidInput
	}
	if payoutProviderKind(ctx, db) != payoutProviderYunzhanghu {
		return nil, "", fmt.Errorf("%w：当前未开启云账户代发（陪玩打款渠道不是云账户），请先在系统配置开启云账户代发", errInvalidInput)
	}
	cfg := loadYunzhanghuPayoutConfig(ctx, db)
	if !cfg.Enabled {
		return nil, "", fmt.Errorf("%w：云账户代发未开启，请先在系统配置开启云账户代发", errInvalidInput)
	}
	if strings.TrimSpace(cfg.SignBase) == "" {
		return nil, "", fmt.Errorf("%w：云账户签约地址未配置，请先在系统配置填写签约地址前缀", errInvalidInput)
	}
	if _, err := db.EsportsPlayer.Get(ctx, playerID); err != nil {
		return nil, "", err
	}
	account, err := db.EsportsPlayerPayoutAccount.Query().Where(
		esportsplayerpayoutaccount.PlayerIDEQ(playerID),
		esportsplayerpayoutaccount.ProviderEQ(payoutProviderYunzhanghu),
		esportsplayerpayoutaccount.AccountTypeEQ(esportsplayerpayoutaccount.AccountType(accountType)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		account, err = db.EsportsPlayerPayoutAccount.Create().
			SetPlayerID(playerID).SetProvider(payoutProviderYunzhanghu).
			SetAccountType(esportsplayerpayoutaccount.AccountType(accountType)).
			SetStatus(esportsplayerpayoutaccount.StatusPending).Save(ctx)
	}
	if err != nil {
		return nil, "", err
	}
	link, err := buildYunzhanghuSignURL(cfg, playerID, accountType)
	if err != nil {
		return nil, "", err
	}
	return account, link, nil
}

// unbindPlayerPayoutAccount 解约：状态置 disabled 并清空签约号与脱敏账号；
// 保留户名与签约时间用于审计（updated_at 自动更新）。accountType 为空表示解约该陪玩全部账户。
func unbindPlayerPayoutAccount(ctx context.Context, db *ent.Client, playerID int, accountType string) error {
	if playerID < 1 {
		return errInvalidInput
	}
	accountType = strings.TrimSpace(accountType)
	if accountType != "" && !validPayoutAccountType(accountType) {
		return errInvalidInput
	}
	q := db.EsportsPlayerPayoutAccount.Update().Where(
		esportsplayerpayoutaccount.PlayerIDEQ(playerID),
		esportsplayerpayoutaccount.ProviderEQ(payoutProviderYunzhanghu),
	)
	if accountType != "" {
		q.Where(esportsplayerpayoutaccount.AccountTypeEQ(esportsplayerpayoutaccount.AccountType(accountType)))
	}
	_, err := q.SetStatus(esportsplayerpayoutaccount.StatusDisabled).
		SetProviderSignNo("").SetAccountNoMasked("").AddVersion(1).Save(ctx)
	return err
}

// activatePlayerPayoutAccount 签约成功回填（下一阶段云账户签约回调调用）：
// 按 (player_id, provider=yunzhanghu, account_type) upsert，写入签约号、户名与脱敏账号，
// 状态置 active 并记录签约时间。完整卡号只会以脱敏形式落库。
func activatePlayerPayoutAccount(ctx context.Context, db *ent.Client, playerID int, accountType, providerSignNo, accountName, accountNoMasked string) error {
	accountType = strings.TrimSpace(accountType)
	providerSignNo = strings.TrimSpace(providerSignNo)
	accountName = strings.TrimSpace(accountName)
	if playerID < 1 || !validPayoutAccountType(accountType) || providerSignNo == "" {
		return fmt.Errorf("%w：签约参数不完整", errInvalidInput)
	}
	if len(providerSignNo) > 128 || len([]rune(accountName)) > 64 {
		return fmt.Errorf("%w：签约参数长度超限", errInvalidInput)
	}
	if _, err := db.EsportsPlayer.Get(ctx, playerID); err != nil {
		return err
	}
	masked := maskPayoutAccountNo(accountNoMasked)
	if len(masked) > 64 {
		return fmt.Errorf("%w：脱敏账号长度超限", errInvalidInput)
	}
	account, err := db.EsportsPlayerPayoutAccount.Query().Where(
		esportsplayerpayoutaccount.PlayerIDEQ(playerID),
		esportsplayerpayoutaccount.ProviderEQ(payoutProviderYunzhanghu),
		esportsplayerpayoutaccount.AccountTypeEQ(esportsplayerpayoutaccount.AccountType(accountType)),
	).Only(ctx)
	if ent.IsNotFound(err) {
		account, err = db.EsportsPlayerPayoutAccount.Create().
			SetPlayerID(playerID).SetProvider(payoutProviderYunzhanghu).
			SetAccountType(esportsplayerpayoutaccount.AccountType(accountType)).
			SetStatus(esportsplayerpayoutaccount.StatusPending).Save(ctx)
	}
	if err != nil {
		return err
	}
	// 重复签约回调幂等：已 active 且签约号一致时不重复写，保留原签约时间与版本号。
	if account.Status == esportsplayerpayoutaccount.StatusActive && account.ProviderSignNo == providerSignNo {
		return nil
	}
	_, err = db.EsportsPlayerPayoutAccount.UpdateOne(account).
		SetProviderSignNo(providerSignNo).SetAccountName(accountName).
		SetAccountNoMasked(masked).SetStatus(esportsplayerpayoutaccount.StatusActive).
		SetSignedAt(time.Now().UTC()).AddVersion(1).Save(ctx)
	return err
}

// payoutAccountIDForWithdrawal 云账户代发下返回可用于打款的签约账户 ID：
// 人工打款返回 nil（不改变原有提现行为）；云账户渠道缺少 active 签约账户时明确拒绝。
// 微信零钱无法代发，channel=wechat 按 bank_card 签约账户校验。
func payoutAccountIDForWithdrawal(ctx context.Context, db *ent.Client, playerID int, channel string) (*int, error) {
	if payoutProviderKind(ctx, db) != payoutProviderYunzhanghu {
		return nil, nil
	}
	accountType := channel
	if accountType == "wechat" {
		accountType = string(esportsplayerpayoutaccount.AccountTypeBankCard)
	}
	account, err := db.EsportsPlayerPayoutAccount.Query().Where(
		esportsplayerpayoutaccount.PlayerIDEQ(playerID),
		esportsplayerpayoutaccount.ProviderEQ(payoutProviderYunzhanghu),
		esportsplayerpayoutaccount.AccountTypeEQ(esportsplayerpayoutaccount.AccountType(accountType)),
		esportsplayerpayoutaccount.StatusEQ(esportsplayerpayoutaccount.StatusActive),
	).First(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：请先完成收款账户签约", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	return &account.ID, nil
}

// playerPayoutAccountsHandler 陪玩端查看本人签约账户（无记录返回空数组）。
func playerPayoutAccountsHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerPayoutAccounts(c.Context(), global.ORM, player.ID)
	if err != nil {
		return businessError(c, err)
	}
	views := make([]payoutAccountView, 0, len(items))
	for _, item := range items {
		views = append(views, payoutAccountViewOf(item))
	}
	return reply(c, views)
}

// playerPayoutSignURLHandler 陪玩端获取签约地址：未开启云账户代发时返回中文错误。
func playerPayoutSignURLHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		AccountType string `json:"account_type"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	account, link, err := playerPayoutSignURL(c.Context(), global.ORM, player.ID, in.AccountType)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"url":          link,
		"account_type": account.AccountType.String(),
		"status":       account.Status.String(),
	})
}

// playerPayoutUnbindHandler 陪玩端解约：account_type 可省略，省略时解约全部渠道账户。
func playerPayoutUnbindHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		AccountType string `json:"account_type"`
	}
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&in); err != nil {
			return fail(c, fiber.StatusBadRequest, "请求正文无效")
		}
	}
	if err := unbindPlayerPayoutAccount(c.Context(), global.ORM, player.ID, in.AccountType); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"ok": true})
}
