package esports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	cmfcrypto "github.com/wuwuseo/cmf/crypto"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidentitybinding"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportswalletaccount"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
)

// 演示数据种子：一次铺好「三角洲游戏分区 + 1 个测试商品 + 1 个测试客户 + 5 个陪玩」。
//
// 与 SeedBaseline 的分工：SeedBaseline 只播种「新库不加就跑不起来」的基准数据
// （游戏分区、会员等级等），演示数据绝不能混进去——所以本文件由环境变量
// ESPORTS_DEMO_SEED 显式开关控制，默认关闭，避免生产库冒出测试商品和测试账号。
//
// 幂等口径：商品按 code、账号按 username、客户/陪玩按 user_id、钱包按
// (account_type, owner_id) 判存，命中即跳过；重复执行不产生重复数据，
// 也不会覆盖运营对已存在记录的修改（改名、改价、停用等）。
const (
	// DemoSeedEnv 是演示数据播种开关，取值 1/true/yes/on 时随 SeedBaseline 生效。
	DemoSeedEnv = "ESPORTS_DEMO_SEED"
	// DemoSeedPasswordEnv 可指定演示账号的统一初始密码；未设置时每次播种随机生成。
	DemoSeedPasswordEnv = "ESPORTS_DEMO_PASSWORD"

	// DemoSeedSkuCode 是演示商品编码，同时是幂等判存用的唯一键。
	DemoSeedSkuCode = "DEMO_DELTA_PLAY_01"
	// DemoSeedGameZoneCode 是演示商品挂载的游戏分区（商品分类 code）。
	DemoSeedGameZoneCode = "GAME_DELTA_FORCE"

	// DemoSeedOwnerAdminEnv 可指定演示客户归属的管理员用户名；不设时自动挑 cs_admin 角色的管理员。
	DemoSeedOwnerAdminEnv = "ESPORTS_DEMO_OWNER_ADMIN"

	// demoCustomerVerificationMethod 与业务侧校验客户身份绑定时的口径保持一致。
	demoCustomerVerificationMethod = "verified_account_link"

	demoCustomerUser  = "demo_customer01"
	demoCustomerName  = "测试客户-小三角"
	demoCustomerPhone = "13800000001"
	demoCustomerUID   = "940000001"
)

var (
	demoSeedPasswordOnce sync.Once
	demoSeedPassword     string
)

// DemoSeedPassword 返回本次播种使用的统一初始密码：优先取 ESPORTS_DEMO_PASSWORD，
// 未设置时随机生成（Demo + 16 位十六进制）。源码不保留任何可用凭证；
// 调用方（命令行工具或启动日志）负责打印一次，供联调取用。
func DemoSeedPassword() string {
	demoSeedPasswordOnce.Do(func() {
		if value := strings.TrimSpace(os.Getenv(DemoSeedPasswordEnv)); value != "" {
			demoSeedPassword = value
			return
		}
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			// crypto/rand 失败极罕见：退化为时间戳派生值，至少不留下固定口令。
			demoSeedPassword = fmt.Sprintf("Demo%d", time.Now().UnixNano())
			return
		}
		demoSeedPassword = "Demo" + hex.EncodeToString(buf)
	})
	return demoSeedPassword
}

// demoPlayerSpec 描述一个演示陪玩：账号 + 档案一起播，等级取 defaultSkillLevels 的编码。
type demoPlayerSpec struct {
	Username    string
	DisplayName string
	Phone       string
	UID         string
	Level       string
	DailyLimit  int
	Concurrent  int
}

// demoPlayerSpecs 是 5 个演示陪玩，覆盖四个技能等级，方便联调派单候选的等级门槛过滤。
var demoPlayerSpecs = []demoPlayerSpec{
	{Username: "demo_player01", DisplayName: "三角洲-阿星", Phone: "13800000011", UID: "940000011", Level: "senior", DailyLimit: 6, Concurrent: 3},
	{Username: "demo_player02", DisplayName: "三角洲-小北", Phone: "13800000012", UID: "940000012", Level: "skilled", DailyLimit: 5, Concurrent: 2},
	{Username: "demo_player03", DisplayName: "三角洲-南风", Phone: "13800000013", UID: "940000013", Level: "mentor", DailyLimit: 8, Concurrent: 4},
	{Username: "demo_player04", DisplayName: "三角洲-十七", Phone: "13800000014", UID: "940000014", Level: "novice", DailyLimit: 3, Concurrent: 1},
	{Username: "demo_player05", DisplayName: "三角洲-老K", Phone: "13800000015", UID: "940000015", Level: "skilled", DailyLimit: 5, Concurrent: 2},
}

// DemoSeedResult 汇总一次播种的新建数量，全部为 0 表示库里已有完整演示数据。
type DemoSeedResult struct {
	ProductCreated  bool
	CustomerCreated bool
	PlayersCreated  int
	AccountsCreated int
	WalletsCreated  int
	// BindingCreated 表示本次补建了「客户 ↔ 前台用户」身份绑定（缺了客户端全部接口会 403）。
	BindingCreated bool
	// OwnerAssigned 表示本次给客户补了归属管理员（缺了客服报单页选不到该客户）。
	OwnerAssigned bool
}

// Empty 判断本次是否没有任何新建（用于日志噪音控制）。
func (r DemoSeedResult) Empty() bool {
	return !r.ProductCreated && !r.CustomerCreated && r.PlayersCreated == 0 &&
		r.AccountsCreated == 0 && r.WalletsCreated == 0 &&
		!r.BindingCreated && !r.OwnerAssigned
}

// DemoSeedEnabled 读取演示数据播种开关。
func DemoSeedEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DemoSeedEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// SeedDemoData 幂等播种演示数据（三角洲分区下的测试商品 + 测试客户 + 5 个陪玩）。
// 分区本身由 SeedGameZones 负责，这里只读取；分区缺失时直接报错而不是静默跳过，
// 否则会播出一个没有分区归属的商品。
func SeedDemoData(ctx context.Context, db *ent.Client) (DemoSeedResult, error) {
	var res DemoSeedResult
	if db == nil {
		return res, errInvalidInput
	}

	created, err := seedDemoProduct(ctx, db)
	if err != nil {
		return res, fmt.Errorf("播种演示商品: %w", err)
	}
	res.ProductCreated = created

	accCreated, custCreated, walCreated, bindingCreated, ownerAssigned, err := seedDemoCustomer(ctx, db)
	if err != nil {
		return res, fmt.Errorf("播种演示客户: %w", err)
	}
	res.AccountsCreated += accCreated
	res.CustomerCreated = custCreated > 0
	res.WalletsCreated += walCreated
	res.BindingCreated = bindingCreated
	res.OwnerAssigned = ownerAssigned

	for _, spec := range demoPlayerSpecs {
		a, p, w, err := seedDemoPlayer(ctx, db, spec)
		if err != nil {
			return res, fmt.Errorf("播种演示陪玩 %s: %w", spec.Username, err)
		}
		res.AccountsCreated += a
		res.PlayersCreated += p
		res.WalletsCreated += w
	}
	return res, nil
}

// seedDemoProduct 播种测试商品：挂在三角洲行动分区下，直接上架，便于下单联调。
func seedDemoProduct(ctx context.Context, db *ent.Client) (bool, error) {
	exists, err := db.EsportsSku.Query().Where(esportssku.CodeEQ(DemoSeedSkuCode)).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	zone, err := db.EsportsProductCategory.Query().
		Where(esportsproductcategory.CodeEQ(DemoSeedGameZoneCode)).Only(ctx)
	if err != nil {
		return false, fmt.Errorf("游戏分区 %s 不可用（应由 SeedGameZones 先播种）: %w", DemoSeedGameZoneCode, err)
	}

	// 走业务保存函数而不是直接 Create：编码生成、等级门槛回落、每席承诺量校验
	// 与金额约束都在 saveSku 里，绕过它容易播出一个「能建但不能下单」的商品。
	in := skuInput{
		Code:                   DemoSeedSkuCode,
		Title:                  "【测试】三角洲行动 · 娱乐陪玩（1 小时）",
		ServiceType:            "play",
		CategoryID:             &zone.ID,
		Description:            "演示数据：三角洲行动娱乐陪玩，1 份 = 1 小时，用于联调下单 / 派单 / 结算全链路。",
		UnitPriceCents:         6800,
		MinUnitPriceCents:      6800,
		ServiceUnits:           1,
		ServiceUnitName:        "小时",
		ServiceDurationMinutes: 60,
		RequiredSeats:          1,
		QuotaTotal:             -1,
		SortOrder:              1,
		Status:                 "on_sale",
	}
	if _, err := saveSku(ctx, db, in); err != nil {
		// 并发播种时唯一索引拒绝视为已存在。
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return false, fmt.Errorf("保存演示商品 %s: %w", DemoSeedSkuCode, err)
	}
	return true, nil
}

// seedDemoCustomer 播种测试客户（账号 + 客户档案 + 余额钱包 + 身份绑定 + 归属），各项各自幂等。
//
// 三项容易漏但少了就「看着有数据、实际用不了」的关联：
//   - 身份绑定：客户端所有 /user/* 接口都走 customerForVerifiedUser，缺绑定直接 403；
//   - 归属管理员：客服客户列表对非超管只返回 owner_admin_id=自己 的客户，无归属则报单选不到；
//   - self_order_enabled：风险校验里未开启自助下单会拦截客户自己下单。
func seedDemoCustomer(ctx context.Context, db *ent.Client) (accCreated, custCreated, walCreated int, bindingCreated, ownerAssigned bool, err error) {
	account, created, err := ensureDemoUser(ctx, db, demoCustomerUser, demoCustomerName, demoCustomerPhone, demoCustomerUID)
	if err != nil {
		return 0, 0, 0, false, false, err
	}
	if created {
		accCreated = 1
	}

	customer, err := db.EsportsCustomer.Query().Where(esportscustomer.UserIDEQ(account.ID)).Only(ctx)
	switch {
	case err == nil:
		// 已存在：保留运营改动，只补钱包与缺失关联。
	case ent.IsNotFound(err):
		customer, err = db.EsportsCustomer.Create().
			SetUserID(account.ID).
			SetDisplayName(demoCustomerName).
			SetPhone(demoCustomerPhone).
			SetAgeStatus(esportscustomer.AgeStatusAdult).
			SetSelfOrderEnabled(true).
			SetSource(esportscustomer.SourceManual).
			Save(ctx)
		if err != nil {
			return accCreated, 0, 0, false, false, err
		}
		custCreated = 1
	default:
		return accCreated, 0, 0, false, false, err
	}

	wallet, err := ensureDemoWallet(ctx, db, walletCustomerBalance, customer.ID, customer.DisplayName)
	if err != nil {
		return accCreated, custCreated, 0, false, false, err
	}
	if wallet {
		walCreated = 1
	}

	bindingCreated, err = ensureDemoCustomerBinding(ctx, db, customer.ID, account.UID)
	if err != nil {
		return accCreated, custCreated, walCreated, false, false, err
	}

	ownerAssigned, err = ensureDemoCustomerOwner(ctx, db, customer)
	if err != nil {
		return accCreated, custCreated, walCreated, bindingCreated, false, err
	}
	return accCreated, custCreated, walCreated, bindingCreated, ownerAssigned, nil
}

// ensureDemoCustomerBinding 幂等补建「客户 ↔ 前台用户」身份绑定。
// 业务代码只读不写这张表（注册/绑定流程尚未实现），所以演示客户必须由种子补上，
// 否则客户端 /user/profile、/user/wallets、/user/orders 全部返回 403。
func ensureDemoCustomerBinding(ctx context.Context, db *ent.Client, customerID int, uid string) (bool, error) {
	if customerID < 1 || strings.TrimSpace(uid) == "" {
		return false, nil
	}
	exists, err := db.EsportsIdentityBinding.Query().Where(
		esportsidentitybinding.CustomerIDEQ(customerID),
		esportsidentitybinding.ProviderEQ("user"),
		esportsidentitybinding.SubjectScopeEQ(""),
		esportsidentitybinding.ExternalSubjectEQ(uid),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	_, err = db.EsportsIdentityBinding.Create().
		SetCustomerID(customerID).
		SetProvider("user").
		SetSubjectScope("").
		SetExternalSubject(uid).
		SetVerificationMethod(demoCustomerVerificationMethod).
		SetStatus(esportsidentitybinding.StatusActive).
		SetVerifiedAt(time.Now()).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// 唯一索引 (provider, subject_scope, external_subject) 抢先，视为已绑定。
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ensureDemoCustomerOwner 给无归属的演示客户补一个归属管理员：客服客户列表按归属过滤，
// owner 为空时连超管之外的客服都看不到，报单页自然选不到客户。
// 只在 owner 为空时补，已有归属一律保留（不覆盖运营的归属调整）。
func ensureDemoCustomerOwner(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer) (bool, error) {
	if customer == nil || customer.OwnerAdminID != nil {
		return false, nil
	}
	adminID, err := demoOwnerAdminID(ctx, db)
	if err != nil {
		return false, err
	}
	if adminID < 1 {
		// 库里还没有可用管理员（纯新库），留给运维之后手动归属。
		return false, nil
	}
	if _, err := customer.Update().SetOwnerAdminID(adminID).Save(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// demoOwnerAdminID 解析演示客户归属：优先环境变量指定的用户名，否则取第一个 cs_admin 角色管理员。
// 解析不到返回 0（调用方跳过归属，不视为错误）。
func demoOwnerAdminID(ctx context.Context, db *ent.Client) (int, error) {
	if name := strings.TrimSpace(os.Getenv(DemoSeedOwnerAdminEnv)); name != "" {
		row, err := db.Admin.Query().Where(admin.UsernameEQ(name)).Only(ctx)
		if ent.IsNotFound(err) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		return row.ID, nil
	}
	link, err := db.AdminRole.Query().
		Where(adminrole.HasRoleWith(role.NameEQ("cs_admin"))).
		Order(ent.Asc(adminrole.FieldAdminID)).
		First(ctx)
	if ent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return link.AdminID, nil
}

// seedDemoPlayer 播种单个陪玩（账号 + 档案 + 猫粮钱包）。
// 档案状态直接给 qualified：种子陪玩若停在 pending，派单候选里永远看不到它们。
func seedDemoPlayer(ctx context.Context, db *ent.Client, spec demoPlayerSpec) (accCreated, playerCreated, walCreated int, err error) {
	account, created, err := ensureDemoUser(ctx, db, spec.Username, spec.DisplayName, spec.Phone, spec.UID)
	if err != nil {
		return 0, 0, 0, err
	}
	if created {
		accCreated = 1
	}

	player, err := db.EsportsPlayer.Query().Where(esportsplayer.UserIDEQ(account.ID)).Only(ctx)
	switch {
	case err == nil:
		// 已存在：保留运营改动。
	case ent.IsNotFound(err):
		player, err = db.EsportsPlayer.Create().
			SetUserID(account.ID).
			SetDisplayName(spec.DisplayName).
			SetQualification(esportsplayer.QualificationQualified).
			SetPresence(esportsplayer.PresenceOffline).
			SetLevel(spec.Level).
			SetGames([]string{"三角洲行动"}).
			SetDailyLimit(spec.DailyLimit).
			SetConcurrentLimit(spec.Concurrent).
			SetTimezone("Asia/Shanghai").
			Save(ctx)
		if err != nil {
			return accCreated, 0, 0, err
		}
		playerCreated = 1
	default:
		return accCreated, 0, 0, err
	}

	wallet, err := ensureDemoWallet(ctx, db, walletPlayerCatfood, player.ID, player.DisplayName)
	if err != nil {
		return accCreated, playerCreated, 0, err
	}
	if wallet {
		walCreated = 1
	}
	return accCreated, playerCreated, walCreated, nil
}

// ensureDemoUser 按 username 幂等创建普通用户账号；密码与后端登录口径一致
// （cmf crypto：bcrypt + 随机盐，返回 hash 与 salt 分列存储）。
func ensureDemoUser(ctx context.Context, db *ent.Client, username, nickname, phone, uid string) (*ent.User, bool, error) {
	row, err := db.User.Query().Where(user.UsernameEQ(username)).Only(ctx)
	if err == nil {
		return row, false, nil
	}
	if !ent.IsNotFound(err) {
		return nil, false, err
	}

	hashed, salt, err := cmfcrypto.HashPassword(DemoSeedPassword())
	if err != nil {
		return nil, false, err
	}
	row, err = db.User.Create().
		SetEmail(username + "@esports.local").
		SetUsername(username).
		SetNickname(nickname).
		SetName(nickname).
		SetUID(uid).
		SetPhone(phone).
		SetPassword(hashed).
		SetPasswordSalt(salt).
		SetStatus(user.StatusActive).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// 并发播种：email / phone 唯一索引抢先，回查即视为已存在。
		row, err = db.User.Query().Where(user.UsernameEQ(username)).Only(ctx)
		return row, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return row, true, nil
}

// ensureDemoWallet 幂等创建钱包账户（客户余额 / 陪玩猫粮），返回本次是否新建。
func ensureDemoWallet(ctx context.Context, db *ent.Client, accountType string, ownerID int, displayName string) (bool, error) {
	exists, err := db.EsportsWalletAccount.Query().Where(
		esportswalletaccount.AccountTypeEQ(esportswalletaccount.AccountType(accountType)),
		esportswalletaccount.OwnerIDEQ(ownerID),
	).Exist(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	_, err = db.EsportsWalletAccount.Create().
		SetAccountType(esportswalletaccount.AccountType(accountType)).
		SetOwnerID(ownerID).
		SetDisplayName(displayName).
		Save(ctx)
	if ent.IsConstraintError(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
