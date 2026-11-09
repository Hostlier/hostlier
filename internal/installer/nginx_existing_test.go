package installer

import (
	"context"
	"strings"
	"testing"
)

// TestExistingNginxInstallationPreservesExternalServices audits every installation command in coexistence mode.
func TestExistingNginxInstallationPreservesExternalServices(t *testing.T) {
	assets, err := InstallationAssets(Options{Domain: "example.com", Proxy: "nginx-existing", DockerMode: "existing"})
	if err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	assertNoExternalUnits(t, assets)
	if !strings.Contains(assets.Service, "--proxy nginx-existing") { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(assets.Service)
	}
	if !strings.Contains(assets.Service, "NoNewPrivileges=true") || !strings.Contains(assets.Service, "ProtectHome=true") || !strings.Contains(assets.NginxCertificateService, "ExecStart=/usr/bin/certbot certonly") {
		t.Fatal("controller protections or dedicated certificate job missing")
	}
	var calls []string
	a := installationRunner{ctx: context.Background(), plan: Plan{Options: Options{Proxy: "nginx-existing", Runtime: "docker", DockerMode: "existing"}, Snapshot: Snapshot{Rules: []int{80, 443}, Firewall: "ufw"}}, runOverride: func(file string, args ...string) error { // Capture this fixture operation independently of real host service changes.
		calls = append(calls, file+" "+strings.Join(args, " "))
		return nil
	}}
	if err := a.packages(); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	if err := a.firewall(); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	if err := a.services(); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	want := []string{"systemctl daemon-reload", "systemctl is-active docker.service", "systemctl is-active nginx.service", "systemctl enable --now hostlier.service hostlier-nginx-existing-renew.timer", "systemctl restart hostlier.service"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") { // Fail if unexpected host mutations: .
		t.Fatalf("unexpected host mutations: %v", calls)
	}
}

// assertNoExternalUnits prevents rendered installation assets from taking ownership of operator services.
func assertNoExternalUnits(t *testing.T, assets Assets) {
	t.Helper()
	if assets.NginxService != "" || assets.CaddyService != "" || assets.Caddy != "" || strings.Contains(assets.Service, "Requires=docker.service") { // Fail if external service takeover.
		t.Fatal("external service takeover", assets)
	}
}

// TestExistingDockerCompatibility checks package-independent daemon compatibility and rootless rejection.
func TestExistingDockerCompatibility(t *testing.T) {
	for _, version := range []string{"28.2.2", "29.1.3"} { // Exercise each relevant coexistence behavior independently.
		if err := validateExistingDocker(version, "daemon-id", "linux", nil); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
			t.Fatal(err)
		}
	}
	for _, version := range []string{"27.5.0", "30.0.0", "invalid"} { // Fail if unsupported version.
		if err := validateExistingDocker(version, "daemon-id", "linux", nil); err == nil { // Fail if unsupported version.
			t.Fatal("unsupported version", version)
		}
	}
	if err := validateExistingDocker("28.2.2", "daemon-id", "linux", []string{"name=rootless"}); err == nil { // Fail if rootless accepted.
		t.Fatal("rootless accepted")
	}
	if err := validateExistingDocker("28.2.2", "daemon-id", "windows", nil); err == nil { // Fail if non-Linux daemon accepted.
		t.Fatal("non-Linux daemon accepted")
	}
}

// TestExistingNginxCannotReplaceStandaloneInstallation requires fresh attachment instead of silent proxy migration.
func TestExistingNginxCannotReplaceStandaloneInstallation(t *testing.T) {
	p := Plan{Options: Options{Proxy: "nginx-existing"}}
	inspectProxyMigration(&p, Installation{Proxy: "nginx", OwnedNginx: true})
	if len(p.Errors) == 0 { // Fail if standalone edge silently migrated.
		t.Fatal("standalone edge silently migrated")
	}
}

// TestStandardNginxMasterCommand accepts variable-expanded standard config without adopting other masters.
func TestStandardNginxMasterCommand(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"nginx: master process /usr/sbin/nginx -c /etc/nginx/nginx.conf\x00", true},
		{"/usr/sbin/nginx\x00-c\x00/etc/nginx/nginx.conf\x00", true},
		{"nginx: master process /usr/sbin/nginx -c /etc/nginx/custom.conf\x00", false},
		{"nginx: master process /usr/sbin/nginx -c /etc/nginx/nginx.conf -p /srv/nginx\x00", false},
		{"nginx: master process /usr/local/sbin/nginx -c /etc/nginx/nginx.conf\x00", false},
		{"nginx: master process /usr/sbin/nginx\x00", false},
	} {
		if got := standardNginxMasterCommand([]byte(tc.command)); got != tc.want {
			t.Fatalf("command %q accepted=%v, want %v", tc.command, got, tc.want)
		}
	}
}
