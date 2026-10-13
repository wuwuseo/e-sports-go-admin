package esports

import "testing"

func TestExperimentalFeatureGateRequiresDevelopmentOptIn(t *testing.T) {
	for _, tc := range []struct {
		environment string
		flag        string
		want        bool
	}{
		{"development", "1", true},
		{"production", "1", false},
		{"", "1", false},
		{"development", "", false},
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
