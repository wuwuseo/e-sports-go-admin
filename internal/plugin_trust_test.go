package internal

import "testing"

func TestValidatePluginTrustPolicyFailsClosedInProduction(t *testing.T) {
	for _, test := range []struct {
		name        string
		environment string
		require     bool
		keys        int
		wantError   bool
	}{
		{name: "development unsigned", environment: "development"},
		{name: "production unsigned", environment: "production", wantError: true},
		{name: "signature without key", environment: "development", require: true, wantError: true},
		{name: "production signed", environment: "production", require: true, keys: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validatePluginTrustPolicy(test.environment, test.require, test.keys)
			if (err != nil) != test.wantError {
				t.Fatalf("validatePluginTrustPolicy() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}
