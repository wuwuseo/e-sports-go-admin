package esports

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// 上线自检（§18 上线启用条件）。
//
// 目的：把「代码已闭环、但需要外部配置才能真正用」的能力逐项摊开，让运营在上线前就看到
// 哪些能力只是壳（在线收款 / 推送 / 企微），而不是等业务方点下去才发现 503。
//
// 口径：
//   - blocking=true 的项缺失会让交易主链路完全不可用（功能总开关、订单支付时限）；
//   - 其余项缺失只让某个能力降级（例如在线收款不可用，但人工核验链路仍可跑），不阻断上线；
//   - ready 只是「配置自检通过」，等于「可以开始按功能列表做业务验收」，不等于 P0 已交付；
//     真实支付 / 打款退款 / 企微宿主 / 推送网关仍需在业务环境端到端验收（见 Scope / Caveat 字段）；
//   - 本接口只回报「是否配置」，绝不回显密钥、商户号、回调地址等敏感值。

// 自检状态。
const (
	readinessReady    = "ready"    // 能力可用
	readinessDegraded = "degraded" // 降级可用（走人工 / 缺部分配置）
	readinessMissing  = "missing"  // 未配置，该项能力不可用
)

// readinessCheck 单项自检结果。
type readinessCheck struct {
	Key string `json:"key"`
	// Name 面向运营的中文名称。
	Name   string `json:"name"`
	Status string `json:"status"`
	// Blocking 该项缺失是否阻断上线（仅功能开关与支付时限为 true）。
	Blocking bool `json:"blocking"`
	// Detail 当前状态说明，不含任何密钥值。
	Detail string `json:"detail"`
	// Action 运营下一步该做什么；ready 时为空。
	Action string `json:"action"`
}

// readinessReport 上线自检报告。
type readinessReport struct {
	// Ready 是否满足上线**配置**最低条件（无 blocking 缺失项）。
	// 注意：ready 只代表「跑通交易主链路所需的配置已齐备」，不代表 P0 功能清单已验收、
	// 也不代表真实支付 / 企微宿主 / 推送网关已在业务环境联调通过——后者必须人工端到端验收。
	Ready bool `json:"ready"`
	// Scope 说明本报告覆盖范围，避免把配置自检当成验收结论。
	Scope string `json:"scope"`
	// Caveat 说明 ready=true 仍不覆盖的内容（P0 功能与真实通道验收）。
	Caveat string `json:"caveat"`
	// Blockers / Degraded 阻断项与降级项数量，便于后台直接显示徽标。
	Blockers    int              `json:"blockers"`
	Degraded    int              `json:"degraded"`
	GeneratedAt time.Time        `json:"generated_at"`
	Checks      []readinessCheck `json:"checks"`
}

// readinessScope / readinessCaveat 是本接口对外声明的口径，由 handler 原样返回。
const (
	readinessScope  = "配置自检：只判断功能开关、订单支付时限、收款通道参数、推送网关、企微应用等运行配置是否齐备"
	readinessCaveat = "ready=true 不代表 P0 功能已验收，也不代表真实支付 / 打款退款 / 企微宿主 / 推送通道已在业务环境联调通过；这些须按功能列表逐项做端到端验收"
)

// systemReadiness 生成上线自检报告。getenv 注入便于测试，生产传 os.Getenv。
func systemReadiness(ctx context.Context, db *ent.Client, getenv func(string) string) *readinessReport {
	report := &readinessReport{
		GeneratedAt: time.Now().UTC(),
		Scope:       readinessScope,
		Caveat:      readinessCaveat,
		Checks:      []readinessCheck{},
	}
	add := func(check readinessCheck) {
		if check.Status == readinessMissing && check.Blocking {
			report.Blockers++
		}
		if check.Status == readinessDegraded {
			report.Degraded++
		}
		report.Checks = append(report.Checks, check)
	}

	// 1. 功能总开关：不设则路由与任务都不挂载，属硬阻断。
	if strings.TrimSpace(getenv("ESPORTS_EXPERIMENTAL")) == "1" {
		add(readinessCheck{
			Key: "feature_gate", Name: "功能总开关", Status: readinessReady, Blocking: true,
			Detail: "ESPORTS_EXPERIMENTAL=1",
		})
	} else {
		add(readinessCheck{
			Key: "feature_gate", Name: "功能总开关", Status: readinessMissing, Blocking: true,
			Detail: "未设置 ESPORTS_EXPERIMENTAL=1，路由与定时任务不会挂载",
			Action: "设置环境变量 ESPORTS_EXPERIMENTAL=1 后重启服务",
		})
	}

	// 2. 订单支付时限：为 0 时待付款订单永久不可支付，属硬阻断（与 README 口径一致）。
	if minutes, err := paymentDeadlineMinutes(ctx, db); err != nil || minutes <= 0 {
		add(readinessCheck{
			Key: "payment_deadline", Name: "订单支付时限", Status: readinessMissing, Blocking: true,
			Detail: "未配置或为 0，待付款订单无法支付",
			Action: "在系统配置填写 app.esports.payment_deadline_minutes（1~1440）",
		})
	} else {
		add(readinessCheck{
			Key: "payment_deadline", Name: "订单支付时限", Status: readinessReady, Blocking: true,
			Detail: fmt.Sprintf("%d 分钟", minutes),
		})
	}

	// 3. 收款方式：逐渠道判断「已启用 + 通道参数完整 + 有可用账户」。
	// 只有通道参数完整时才能发起在线收款；否则该渠道只能走人工核验（degraded，不阻断上线）。
	channels, err := listPaymentChannels(ctx, db)
	if err != nil {
		add(readinessCheck{
			Key: "payment_channels", Name: "收款方式", Status: readinessMissing,
			Detail: "读取收款方式失败", Action: "检查数据库连接",
		})
		return report
	}
	enabled := 0
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		enabled++
		name := customerPaymentMethodName[channel.Channel]
		if name == "" {
			name = channel.Channel
		}
		key := "payment_channel:" + channel.Channel
		cfg, cfgErr := paymentGatewayConfigFor(ctx, db, channel.Channel)
		gatewayReady := cfgErr == nil && strings.TrimSpace(cfg.Kind) != ""
		switch {
		case gatewayReady && channel.ActiveAccounts > 0:
			add(readinessCheck{
				Key: key, Name: "在线收款 · " + name, Status: readinessReady,
				Detail: fmt.Sprintf("通道参数已配置，可用收款账户 %d 个", channel.ActiveAccounts),
			})
		case gatewayReady:
			add(readinessCheck{
				Key: key, Name: "在线收款 · " + name, Status: readinessDegraded,
				Detail: "通道参数已配置，但没有审核通过的收款账户",
				Action: "在「收款账户」提交并审核至少一个账户",
			})
		default:
			add(readinessCheck{
				Key: key, Name: "在线收款 · " + name, Status: readinessDegraded,
				Detail: "未配置通道参数，发起在线收款会返回 503；到账仍可走人工核验",
				Action: "在系统配置填写 payment_gateways（base_url / app_id / app_secret / notify_url）",
			})
		}
	}
	if enabled == 0 {
		add(readinessCheck{
			Key: "payment_channels", Name: "收款方式", Status: readinessMissing,
			Detail: "没有任何启用的收款方式，订单无法收款",
			Action: "在「收款管理」启用至少一种收款方式",
		})
	}

	// 4. 人工核验链路：未接真实通道时的保底路径，常态可用（故标 degraded 提示而非阻断）。
	add(readinessCheck{
		Key: "manual_verification", Name: "人工到账核验", Status: readinessDegraded,
		Detail: "可用：客户上传凭证 → 财务核销；真实打款/退款仍需人工登记外部凭证",
	})

	// 5. 站内通知：始终可用，按订单与角色定向投递（不再全员广播）。
	add(readinessCheck{
		Key: "notification_inapp", Name: "站内通知", Status: readinessReady,
		Detail: "按订单与角色定向投递客户 / 责任客服 / 接单陪玩",
	})

	// 6. 推送网关：未配置时只有站内信，客户端收不到实时推送。
	if _, err := newWebhookNotificationSender(db).config(ctx); err != nil {
		add(readinessCheck{
			Key: "notification_push", Name: "通知推送网关", Status: readinessMissing,
			Detail: "未配置推送网关，仅站内信投递；客户与陪玩收不到实时提醒",
			Action: "在系统配置填写 notification_push_webhook（url / secret）",
		})
	} else {
		add(readinessCheck{
			Key: "notification_push", Name: "通知推送网关", Status: readinessReady,
			Detail: "推送地址与签名密钥已配置",
		})
	}

	// 7. 企微自建应用：影响侧边栏客户识别与企微内通知。
	corpID := wecomConfigString(ctx, db, wecomCorpIDKey)
	agentID := wecomConfigString(ctx, db, wecomAgentIDKey)
	secret := wecomConfigString(ctx, db, wecomSecretKey)
	if corpID != "" && agentID != "" && secret != "" {
		add(readinessCheck{
			Key: "wecom_app", Name: "企微自建应用", Status: readinessReady,
			Detail: "corpId / agentId / secret 均已配置",
		})
	} else {
		add(readinessCheck{
			Key: "wecom_app", Name: "企微自建应用", Status: readinessDegraded,
			Detail: "未配置完整，企微侧边栏降级为手动建档，企微内通知不可用",
			Action: "在系统配置填写 wecom_corp_id / wecom_agent_id / wecom_secret",
		})
	}

	// 8. 钱包展示：有内置默认值，缺失不影响交易。
	add(readinessCheck{
		Key: "wallet_display", Name: "钱包展示口径", Status: readinessReady,
		Detail: "未配置时使用内置默认（余额 / 猫粮，100 分每单位）",
	})

	report.Ready = report.Blockers == 0
	return report
}

// adminReadinessHandler 管理端读取上线自检报告（只读，不返回任何密钥值）。
func adminReadinessHandler(c fiber.Ctx) error {
	return reply(c, systemReadiness(c.Context(), global.ORM, os.Getenv))
}
