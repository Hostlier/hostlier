package installer

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// TestFailedInstallationStepReportsCause retains the actionable error and completed progress.
func TestFailedInstallationStepReportsCause(t *testing.T) {
	var events []string
	runner := installationRunner{progress: Installation{Completed: []string{"collector-image", "services"}}, options: ApplyOptions{OnProgress: func(step, status string) {
		events = append(events, step+":"+status)
	}}}
	err := runner.step("edge-readiness", func() error { // Simulate the underlying readiness failure.
		return errors.New("certificate validation timed out")
	})
	// A failed stage must explain its cause without discarding durable progress.
	if err == nil || !strings.Contains(err.Error(), "certificate validation timed out") || !strings.Contains(err.Error(), "collector-image, services") {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "edge-readiness:started,edge-readiness:failed" {
		t.Fatal(events)
	}
}

// TestInstallationStepProgress reports completion only after progress is persisted.
func TestInstallationStepProgress(t *testing.T) {
	var events []string
	runner := installationRunner{plan: Plan{Options: Options{StateDir: t.TempDir()}}, options: ApplyOptions{OnProgress: func(step, status string) {
		events = append(events, step+":"+status)
	}}}
	if err := runner.step("collector-image", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "collector-image:started,collector-image:completed" || strings.Join(runner.progress.Completed, ",") != "collector-image" {
		t.Fatal(events, runner.progress.Completed)
	}
}

// TestAssets verifies private administration and durable service configuration in generated assets.
func TestAssets(t *testing.T) {
	assets, e := InstallationAssets(Options{Domain: "apps.example.com"})
	// Require asset generation for a valid default installation.
	if e != nil {
		t.Fatal(e)
	}
	// Check every controller service property needed for private, restartable administration.
	for _, part := range []string{"ExecStart=/usr/local/bin/hostlier-controller run ", "Restart=on-failure", "127.0.0.1", "--port 7463", "RuntimeDirectoryMode=0700"} {
		// Missing service properties invalidate the generated installation contract.
		if !strings.Contains(assets.Service, part) {
			t.Error("missing service option", part)
		}
	}
	// Require private Caddy administration, resumed edge state and bounded log retention.
	if !strings.Contains(assets.Caddy, "127.0.0.1:2019") || !strings.Contains(assets.CaddyService, "--resume") || !strings.Contains(assets.CaddyService, "reload --config /var/lib/caddy/.config/caddy/autosave.json") || !strings.Contains(assets.CaddyService, "/usr/local/bin/hostlier-caddy") || !strings.Contains(assets.Logrotate, "rotate 7") {
		t.Fatal("missing persistence or rotation")
	}
	// Reject paths that could split systemd executable arguments.
	if _, e = InstallationAssets(Options{Domain: "example.com", Executable: "/bin/unsafe executable"}); e == nil {
		t.Fatal("unsafe systemd path accepted")
	}
}

// TestApprovalRequired rejects applying an installation without explicit approval.
func TestApprovalRequired(t *testing.T) {
	e := Apply(context.Background(), Plan{}, ApplyOptions{})
	// An unapproved installation must report the approval boundary.
	if e == nil || !strings.Contains(e.Error(), "approval") {
		t.Fatal(e)
	}
}

// TestUnsupportedHostPreview preserves a useful non-mutating preview on unsupported systems.
func TestUnsupportedHostPreview(t *testing.T) {
	// This assertion concerns unsupported non-Linux preview behavior.
	if runtime.GOOS == "linux" {
		t.Skip("host-specific read-only preview test")
	}
	p, e := Inspect(context.Background(), Options{Domain: "apps.example.com", StateDir: t.TempDir()})
	// Unsupported platforms should still produce a preview rather than a probe error.
	if e != nil {
		t.Fatal(e)
	}
	// Require unsupported status, explanatory findings and a stable preview fingerprint.
	if p.Supported || len(p.Errors) == 0 || p.Fingerprint == "" {
		t.Fatalf("bad preview %+v", p)
	}
}

// TestUbuntu2604PlatformBoundary keeps both supported LTS releases and CPU targets explicit.
func TestUbuntu2604PlatformBoundary(t *testing.T) {
	resolute := "ID=ubuntu\nVERSION_ID=\"26.04\"\n"
	noble := "ID=ubuntu\nVERSION_ID=\"24.04\"\n"
	docker := Options{Runtime: "docker"}
	if problem := platformProblem(resolute, "linux", "amd64", docker); problem != "" {
		t.Fatal(problem)
	}
	if problem := platformProblem(resolute, "linux", "arm64", docker); problem != "" {
		t.Fatal(problem)
	}
	if problem := platformProblem(resolute, "linux", "amd64", Options{Runtime: "k3s"}); !strings.Contains(problem, "k3s installation") {
		t.Fatal("26.04 admitted untested k3s path", problem)
	}
	if problem := platformProblem(noble, "linux", "amd64", Options{Runtime: "k3s"}); problem != "" {
		t.Fatal(problem)
	}
	for _, tc := range []struct{ release, goos, goarch string }{
		{"ID=ubuntu\nVERSION_ID=\"22.04\"\n", "linux", "amd64"},
		{resolute, "linux", "riscv64"},
		{resolute, "darwin", "amd64"},
		{"ID=debian\nVERSION_ID=\"26.04\"\n", "linux", "amd64"},
	} {
		if problem := platformProblem(tc.release, tc.goos, tc.goarch, docker); problem == "" {
			t.Fatalf("unsupported platform accepted: %+v", tc)
		}
	}
}

// TestUbuntu2604PackageSelection binds preview and install commands to Resolute pins.
func TestUbuntu2604PackageSelection(t *testing.T) {
	p := Plan{Options: Options{Runtime: "docker", DockerMode: "managed", Proxy: "caddy"}, Snapshot: Snapshot{UbuntuVersion: "26.04"}}
	if dockerPackageVersion(p) != DockerVersion2604 || caddyPackageVersion(p) != CaddyVersion2604 || nginxSecurityVersion(p) != NginxSecurityVersion2604 {
		t.Fatal("26.04 selected a 24.04 package pin")
	}
	var commands []string
	a := installationRunner{ctx: context.Background(), plan: p, caddyBinaryOverride: func(context.Context, bool) error { return nil }, runOverride: func(file string, args ...string) error {
		commands = append(commands, file+" "+strings.Join(args, " "))
		return nil
	}}
	if err := a.packages(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(commands, "\n")
	for _, want := range []string{"docker.io=" + DockerVersion2604, "caddy=" + CaddyVersion2604} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s from %s", want, joined)
		}
	}
	if strings.Contains(joined, DockerVersion) || strings.Contains(joined, CaddyVersion) {
		t.Fatal("24.04 package pin used on 26.04", joined)
	}
	p.Proxy = "nginx-gateway"
	if got := nginxGatewayPackages(p); len(got) != 1 || got[0] != "nginx="+NginxSecurityVersion2604 {
		t.Fatal("26.04 gateway selected the wrong Nginx package", got)
	}
	p.Proxy = "nginx"
	p.Snapshot.Nginx = Component{Installed: true, Version: NginxSecurityVersion2604}
	inspectPackageVersions(context.Background(), &p)
	if len(p.Errors) != 0 {
		t.Fatal("26.04 package rejected", p.Errors)
	}
}

// TestAlteredPlanFingerprint verifies option edits invalidate installation approval.
func TestAlteredPlanFingerprint(t *testing.T) {
	p := Plan{Options: Options{Domain: "example.com"}, Changes: []string{"initial"}}
	p.Fingerprint = fingerprint(p)
	p.Changes = append(p.Changes, "unapproved change")
	// Changing the domain must change the plan fingerprint.
	if p.Fingerprint == fingerprint(p) {
		t.Fatal("altered change did not invalidate fingerprint")
	}
}

// TestNFTIngressClassification distinguishes NAT-only rules from host ingress restrictions.
func TestNFTIngressClassification(t *testing.T) {
	lima := `{"nftables":[{"table":{"family":"ip","name":"nat"}},{"chain":{"family":"ip","table":"nat","name":"LIMADNS"}},{"chain":{"family":"ip","table":"nat","name":"PREROUTING","type":"nat","hook":"prerouting","policy":"accept"}},{"rule":{"family":"ip","table":"nat","chain":"PREROUTING","expr":[{"jump":{"target":"LIMADNS"}}]}},{"chain":{"family":"ip","table":"filter","name":"FORWARD","hook":"forward","policy":"drop"}}]}`
	// Lima DNS translation must not be mistaken for host ingress filtering.
	if blocks, e := nftInputFiltering([]byte(lima)); blocks || e != nil {
		t.Fatalf("harmless Lima NAT blocked: %v %v", blocks, e)
	}
	input := `{"nftables":[{"chain":{"family":"inet","table":"filter","name":"INPUT","hook":"input","policy":"drop"}}]}`
	// A non-accept host INPUT policy must be detected.
	if blocks, e := nftInputFiltering([]byte(input)); !blocks || e != nil {
		t.Fatalf("input firewall ignored: %v %v", blocks, e)
	}
	// Malformed firewall output must fail closed.
	if blocks, e := nftInputFiltering([]byte("malformed")); !blocks || e == nil {
		t.Fatal("malformed ruleset did not fail closed")
	}
}

// TestK3sAssetsPrivateAndPinned verifies the cluster service and isolation assumptions.
func TestK3sAssetsPrivateAndPinned(t *testing.T) {
	a, e := InstallationAssets(Options{Domain: "apps.example.com", Runtime: "k3s"})
	// Require valid k3s asset generation.
	if e != nil {
		t.Fatal(e)
	}
	// A k3s controller must depend on k3s rather than Docker.
	if strings.Contains(a.Service, "docker.service") || !strings.Contains(a.Service, "k3s.service") {
		t.Fatal("k3s depends on Docker")
	}
	assets := k3sAssets()
	unit := assets["/etc/systemd/system/k3s.service"]
	// Check the pinned cluster flags that constrain networking and encrypt stored secrets.
	for _, field := range []string{"--disable=traefik", "--disable=servicelb", "--flannel-backend=host-gw", "--secrets-encryption", "nodeport-addresses=127.0.0.0/8"} {
		// Missing security-relevant cluster flags invalidate the generated unit.
		if !strings.Contains(unit, field) {
			t.Error("missing private setting", field)
		}
	}
	// Require a complete SHA-256 digest for every supported architecture.
	for _, hash := range k3sChecksums {
		// Reject incomplete release checksum pins.
		if len(hash) != 64 {
			t.Fatal("invalid pinned checksum")
		}
	}
	firewall := assets["/etc/hostlier/k3s-firewall.sh"]
	// The management guard must preserve unrelated rules and host policies.
	if strings.Contains(firewall, " -F") || strings.Contains(firewall, " -P") {
		t.Fatal("firewall resets unrelated policy")
	}
}

// TestRecordedK3sInputRulesRemainNarrow prevents ownership comments from authorizing arbitrary firewall changes.
func TestRecordedK3sInputRulesRemainNarrow(t *testing.T) {
	marked := `-A INPUT -m comment --comment "KUBE-ROUTER rule to explicitly ACCEPT traffic that comply to network policies" -m mark --mark 0x20000/0x20000 -j ACCEPT`
	local := `-A INPUT -p tcp -m multiport --dports 6443,10250 -m addrtype --src-type LOCAL -m comment --comment hostlier-k3s-local -j ACCEPT`
	private := `-A INPUT -p tcp -m multiport --dports 6443,10250 -m comment --comment hostlier-k3s-private -j REJECT --reject-with icmp6-port-unreachable`
	rules := "-P INPUT ACCEPT\n-A INPUT -j KUBE-FIREWALL\n" + marked + "\n" + local + "\n" + private + "\n"
	// Exact recorded cluster rules may be reused when cluster ownership is proven.
	if iptablesInputFiltering(rules, true) {
		t.Fatal("recorded pinned cluster rules blocked")
	}
	// The same rules require review when no cluster ownership is recorded.
	if !iptablesInputFiltering(rules, false) {
		t.Fatal("unrecorded cluster rules trusted")
	}
	// Mutate marks, ports, verdicts and jump targets to exercise false ownership claims.
	for _, rule := range []string{strings.Replace(marked, "0x20000/0x20000", "0x1/0x1", 1), strings.Replace(marked, "-j ACCEPT", "-j DROP", 1), strings.Replace(local, "6443,10250", "80,443", 1), `-A INPUT -p tcp --dport 443 -m comment --comment hostlier-k3s-unrelated -j DROP`, `-A INPUT -j ADMIN-FIREWALL`, `-A INPUT -p tcp --dport 80 -j REJECT`} {
		// An unrelated restriction must remain visible beside valid owned cluster rules.
		if !iptablesInputFiltering(rules+rule+"\n", true) {
			t.Fatalf("unrelated or altered ingress trusted: %s", rule)
		}
	}
	// Owned rules cannot override an independently restrictive INPUT policy.
	if !iptablesInputFiltering("-P INPUT DROP\n"+rules, true) {
		t.Fatal("default-deny ingress policy trusted")
	}
}

// TestVerifiedK3sNFTCompatibilityAndUnrelatedIngress cross-checks opaque nft adoption boundaries.
func TestVerifiedK3sNFTCompatibilityAndUnrelatedIngress(t *testing.T) {
	// Captures the pinned Ubuntu VM's xtables compatibility representation: nft
	// JSON deliberately omits the original comment text and multiport arguments.
	native := `{"nftables":[{"chain":{"family":"ip","table":"filter","name":"INPUT","hook":"input","policy":"accept"}},{"chain":{"family":"ip","table":"nat","name":"PREROUTING","hook":"prerouting","policy":"accept"}},{"rule":{"family":"ip","table":"filter","chain":"INPUT","expr":[{"xt":{"type":"match","name":"comment"}},{"match":{"op":"==","left":{"&":[{"meta":{"key":"mark"}},131072]},"right":131072}},{"counter":{}},{"accept":null}]}},{"rule":{"family":"ip","table":"filter","chain":"INPUT","expr":[{"xt":{"type":"match","name":"multiport"}},{"xt":{"type":"match","name":"comment"}},{"xt":{"type":"target","name":"REJECT"}}]}},{"rule":{"family":"ip","table":"nat","chain":"PREROUTING","expr":[{"xt":{"type":"match","name":"comment"}},{"counter":{}},{"jump":{"target":"KUBE-SERVICES"}}]}}]}`
	// Verified pinned cluster compatibility rules may be recognized.
	if blocked, err := nftInputFiltering([]byte(native), true); blocked || err != nil {
		t.Fatalf("verified recorded compatibility rules blocked: %v %v", blocked, err)
	}
	// Opaque rules without independent verification require review.
	if blocked, _ := nftInputFiltering([]byte(native), false); !blocked {
		t.Fatal("opaque unverified compatibility rules trusted")
	}
	unrelated := strings.Replace(native, `"name":"INPUT","hook":"input","policy":"accept"`, `"name":"INPUT","hook":"input","policy":"drop"`, 1)
	// Unrelated drop rules must remain blocking beside generated cluster rules.
	if blocked, _ := nftInputFiltering([]byte(unrelated), true); !blocked {
		t.Fatal("unrelated host input default policy ignored")
	}
	extra := `{"chain":{"family":"inet","table":"admin","name":"inbound","hook":"input","policy":"accept"}},{"rule":{"family":"inet","table":"admin","chain":"inbound","expr":[{"drop":null}]}}`
	unrelated = strings.TrimSuffix(native, "]}") + "," + extra + "]}"
	// An ownership-like comment cannot legitimize an unrelated restriction.
	if blocked, _ := nftInputFiltering([]byte(unrelated), true); !blocked {
		t.Fatal("unrelated native host ingress drop ignored")
	}
}

// TestNFTOpaqueRuleCannotHideUnrelatedDrop prevents missing compatibility evidence from hiding ingress restrictions.
func TestNFTOpaqueRuleCannotHideUnrelatedDrop(t *testing.T) {
	rules := `{"nftables":[{"chain":{"family":"ip","table":"filter","name":"INPUT","hook":"input","policy":"accept"}},{"rule":{"family":"ip","table":"filter","chain":"INPUT","expr":[{"xt":{"type":"match","name":"comment"}},{"drop":null}]}}]}`
	// Mixed opaque and unrelated drop expressions must require firewall review.
	if blocked, _ := nftInputFiltering([]byte(rules), true); !blocked {
		t.Fatal("opaque comment hid unrelated DROP")
	}
	// Rule-count disagreement invalidates independent compatibility verification.
	if nftInputViewsMatch([]byte(rules), "-P INPUT ACCEPT\n", "-P INPUT ACCEPT\n") {
		t.Fatal("missing textual rule considered a complete mirror")
	}
	// Matching independently captured rule counts establish the expected view relationship.
	if !nftInputViewsMatch([]byte(rules), "-P INPUT ACCEPT\n-A INPUT -j DROP\n", "-P INPUT ACCEPT\n") {
		t.Fatal("matching INPUT view count rejected")
	}
}

// A direct install pins only the fleet listener, and subsequent init keeps the master.
func TestMasterFingerprintInstallation(t *testing.T) {
	pin := strings.Repeat("a", 64)
	opts := resolveDetectedOptions(Options{Domain: "apps.example.com"}, &Installation{MasterFingerprint: pin}, hostDetection{})
	assets, err := InstallationAssets(opts)
	if err != nil || !strings.Contains(assets.Service, "--host 127.0.0.1 --port 7463 --master-fingerprint "+pin) {
		t.Fatal(assets.Service, err)
	}
	opts.MasterFingerprint = "oops"
	if _, err := InstallationAssets(opts); err == nil {
		t.Fatal("invalid pin accepted")
	}
}
