package esports

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
)

// walletDisplayConfigKey 是钱包展示配置：工作室可配置每类账户的展示名称、图标、
// 说明文案、计费单位名与固定换算倍率（每单位对应多少分）。示例：
//
//	{"customer_balance":{"display_name":"账户余额","icon":"wallet","description":"可用于支付订单",
//	 "unit_name":"元","unit_cents":100}}
//
// 配置缺字段时按字段回落内置默认，整体缺失或非法 JSON 时全部回落默认。
const walletDisplayConfigKey = "app.esports.wallet_display"

// walletDisplay 是某类钱包账户的展示口径。
type walletDisplay struct {
	DisplayName string `json:"display_name"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
	UnitName    string `json:"unit_name"`
	UnitCents   int64  `json:"unit_cents"`
	// DisplayAmount 是按固定倍率换算后的展示金额（两位小数字符串，整数运算避免浮点误差）。
	// 仅账户视图（含余额）下发；纯展示配置接口没有余额可换算，故省略该字段。
	DisplayAmount string `json:"display_amount,omitempty"`
}

func defaultWalletDisplay(accountType string) walletDisplay {
	if accountType == walletPlayerCatfood {
		return walletDisplay{
			DisplayName: "猫粮", Icon: "cat", Description: "陪玩已结算收入",
			UnitName: "猫粮", UnitCents: 100,
		}
	}
	return walletDisplay{
		DisplayName: "余额", Icon: "wallet", Description: "可用于支付订单的账户余额",
		UnitName: "元", UnitCents: 100,
	}
}

// walletDisplayConfig 读取展示配置并补齐默认值。返回按账户类型索引的完整配置。
func walletDisplayConfig(ctx context.Context, db *ent.Client) map[string]walletDisplay {
	out := map[string]walletDisplay{
		walletCustomerBalance: defaultWalletDisplay(walletCustomerBalance),
		walletPlayerCatfood:   defaultWalletDisplay(walletPlayerCatfood),
	}
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(walletDisplayConfigKey)).Only(ctx)
	if err != nil {
		return out
	}
	var raw map[string]struct {
		DisplayName string `json:"display_name"`
		Icon        string `json:"icon"`
		Description string `json:"description"`
		UnitName    string `json:"unit_name"`
		UnitCents   int64  `json:"unit_cents"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(item.Value)), &raw); err != nil {
		return out
	}
	for accountType, display := range out {
		override, ok := raw[accountType]
		if !ok {
			continue
		}
		if name := strings.TrimSpace(override.DisplayName); name != "" {
			display.DisplayName = name
		}
		if icon := strings.TrimSpace(override.Icon); icon != "" {
			display.Icon = icon
		}
		if description := strings.TrimSpace(override.Description); description != "" {
			display.Description = description
		}
		if unitName := strings.TrimSpace(override.UnitName); unitName != "" {
			display.UnitName = unitName
		}
		if override.UnitCents > 0 {
			display.UnitCents = override.UnitCents
		}
		out[accountType] = display
	}
	return out
}

// walletDisplayAmount 按固定倍率把「分」换算成展示单位数量，保留两位小数（整数运算）。
func walletDisplayAmount(balanceCents, unitCents int64) string {
	if unitCents < 1 {
		unitCents = 100
	}
	sign := ""
	if balanceCents < 0 {
		sign = "-"
		balanceCents = -balanceCents
	}
	whole := balanceCents / unitCents
	frac := (balanceCents % unitCents) * 100 / unitCents
	text := strconv.FormatInt(frac+100, 10)[1:]
	return sign + strconv.FormatInt(whole, 10) + "." + text
}

// walletDisplayFor 组装某账户类型的展示口径（含按余额换算的展示金额）。
func walletDisplayFor(displays map[string]walletDisplay, accountType string, balanceCents int64) walletDisplay {
	display, ok := displays[accountType]
	if !ok {
		display = defaultWalletDisplay(accountType)
	}
	display.DisplayAmount = walletDisplayAmount(balanceCents, display.UnitCents)
	return display
}
