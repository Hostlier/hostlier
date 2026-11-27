package controller

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"hostlier/internal/cli"
)

// TestCLIDeviceRevocation exercises the signed approval payload, including the empty execution body.
func TestCLIDeviceRevocation(t *testing.T) {
	c := newTestController(t, t.TempDir(), &recordingRuntime{}, time.Now)
	defer closeController(t, c)
	admin, _, _ := pairedScopeFixture(t, c, deploymentScope{})
	operator, id, _ := pairedScopeFixture(t, c, deploymentScope{Apps: []string{"api"}, DurationSeconds: 3600, MaxUses: 3})
	options := cli.Options{Client: admin, Home: t.TempDir(), OpenBrowser: func(string) error { t.Fatal("revocation opened browser"); return nil }}
	if err := cli.RunWithOptions(context.Background(), []string{"access", "clients", "revoke", id, "--server", "master"}, strings.NewReader(""), io.Discard, io.Discard, options); err != nil {
		t.Fatal(err)
	}
	var result any
	if err := operator.Request(context.Background(), "GET", "/v1/authorizations/self", nil, &result); err == nil {
		t.Fatal("revoked login retained access")
	}
}
