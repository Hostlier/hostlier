package cli

import "testing"

// TestRecoveryHandoffsConnectBeforeReads covers the real dispatcher used by dashboard commands.
func TestRecoveryHandoffsConnectBeforeReads(t *testing.T) {
	cases := []struct {
		args []string
		path string
	}{
		{[]string{"backup", "list", "--server", "vm"}, "/v1/backup-policies"},
		{[]string{"backup", "destinations", "--server", "vm"}, "/v1/backup-destinations"},
		{[]string{"backup", "job", "job-id", "--server", "vm"}, "/v1/recovery-jobs/job-id"},
	}
	// Read handoffs must initialize the injected remote and never submit a mutation.
	for _, item := range cases {
		client := &fakeClient{}
		_, err := invoke(item.args, "", Options{Home: t.TempDir(), Client: client})
		// Exact paths keep backup-policy reads distinct from recovery-job reads.
		if err != nil || len(client.calls) != 1 || client.calls[0].method != "GET" || client.calls[0].path != item.path {
			t.Fatalf("%v: calls=%v, error=%v", item.args, client.calls, err)
		}
	}
}

// TestRecoveryHandoffFlags accepts the documented backup and upgrade arguments before dispatch.
func TestRecoveryHandoffFlags(t *testing.T) {
	args := []string{"backup", "policy", "daily", "--resource", "cache", "--destination", "s3", "--every", "24h", "--retain", "7", "--server", "vm"}
	parsed, err := parse(args)
	// All policy flags must retain their explicit values rather than become positionals.
	if err != nil || len(parsed.args) != 3 || parsed.flags["resource"] != "cache" || parsed.flags["destination"] != "s3" || parsed.flags["every"] != "24h" || parsed.flags["retain"] != "7" {
		t.Fatalf("backup handoff: %+v, error=%v", parsed, err)
	}
	_, err = parse([]string{"resource", "upgrade", "db", "--target", "db-next", "--to", "17", "--destination", "s3"})
	// Upgrades share the destination flag and require a version selector.
	if err != nil {
		t.Fatal(err)
	}
}
