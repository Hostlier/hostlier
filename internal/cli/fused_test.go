package cli

import (
	"testing"
)

// TestFusedCommandsUseControllerTransport proves the CLI sends references rather than acquiring Engine tokens.
func TestFusedCommandsUseControllerTransport(t *testing.T) {
	tests := []struct {
		args         []string
		method, path string
	}{
		{[]string{"fused", "status"}, "GET", "/v1/fused"},
		{[]string{"fused", "connect", "stocks", "--engine-url", "https://engine.example", "--client-id", "registered", "--client-secret-ref", "oauth-client"}, "POST", "/v1/fused/stocks/connect"},
		{[]string{"fused", "refresh", "stocks"}, "POST", "/v1/fused/stocks/refresh"},
		{[]string{"fused", "disconnect", "stocks"}, "DELETE", "/v1/fused/stocks"},
		{[]string{"fused", "token", "stocks", "--family-id", "11111111-2222-3333-4444-555555555555", "--secret-name", "stocks-token", "--name", "stocks-v1", "--allow", "getStockData"}, "POST", "/v1/fused/stocks/token"},
	}
	// Each command uses the same authenticated Hostlier client as ordinary deployments.
	for _, test := range tests {
		f := &fakeClient{}
		_, err := invoke(append(test.args, "--socket", "/test/controller.sock"), "", Options{Client: f, Home: t.TempDir()})
		// A command must issue exactly its intended controller operation.
		if err != nil || len(f.calls) != 1 {
			t.Fatal(test.args, err, f.calls)
		}
		// Check the protocol invariant before continuing the scenario.
		if f.calls[0].method != test.method || f.calls[0].path != test.path {
			t.Fatal(f.calls)
		}
	}
}
