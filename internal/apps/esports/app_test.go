package esports

import "testing"

// 上线配置（P0）：功能启用由 ESPORTS_EXPERIMENTAL=1 显式开启，不再强制 development 环境；
// 生产上线须同时配置非零订单支付时限（app.esports.payment_deadline_minutes）。
func TestExperimentalFeatureGateRequiresExplicitOptIn(t *testing.T) {
	for _, tc := range []struct {
		environment string
		flag        string
		want        bool
	}{
		{"development", "1", true},
		{"production", "1", true},
		{"", "1", true},
		{"development", "", false},
		{"production", "", false},
	} {
		got := experimentalEnabled(func(key string) string {
			if key == "CMF_APP_ENV" {
				return tc.environment
			}
			return tc.flag
		})
		if got != tc.want {
			t.Errorf("environment=%q flag=%q got=%v want=%v", tc.environment, tc.flag, got, tc.want)
		}
	}
}
