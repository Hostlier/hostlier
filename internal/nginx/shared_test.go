package nginx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/model"
)

// sharedFixture supplies an existing site and a standard include tree, with commands captured by each test.
func sharedFixture(t *testing.T) SharedOptions {
	t.Helper()
	dir := t.TempDir()
	o := SharedDefaults(SharedOptions{MainConfig: filepath.Join(dir, "nginx.conf"), Fragment: filepath.Join(dir, "conf.d", "zz-hostlier.conf"), StateDirectory: filepath.Join(dir, "state"), CertificatesFile: filepath.Join(dir, "tls.json"), Options: Options{CertificateDirectory: filepath.Join(dir, "certs"), ChallengeDirectory: filepath.Join(dir, "acme"), Run: func(context.Context, string, ...string) error { return nil }}}) // Capture this fixture operation independently of real host service changes.
	if err := os.MkdirAll(filepath.Dir(o.Fragment), 0755); err != nil {                                                                                                                                                                                                                                                                                                                                                                  // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	main := fmt.Sprintf("events {} http { include %s/*.conf; server { listen 80 default_server; listen 443 ssl default_server; server_name existing.example.test; } }", filepath.Dir(o.Fragment))
	if err := os.WriteFile(o.MainConfig, []byte(main), 0600); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	o.Dump = func(_ context.Context, _ string) ([]byte, error) { // Capture this fixture operation independently of real host service changes.
		data, err := os.ReadFile(o.MainConfig)
		if err != nil { // Require this fixture step to succeed before checking coexistence behavior.
			return nil, err
		}
		dump := fmt.Sprintf("# configuration file %s:\n%s\n", o.MainConfig, data)
		if fragment, err := os.ReadFile(o.Fragment); err == nil { // Require this fixture step to succeed before checking coexistence behavior.
			dump += fmt.Sprintf("# configuration file %s:\n%s\n", o.Fragment, fragment)
		}
		return []byte(dump), nil
	}
	return o
}

// sharedRoute represents a new Hostlier app using the existing Docker loopback publication contract.
func sharedRoute() []Route {
	return []Route{{Project: model.Project{Name: "app", Route: "app.example.test/api"}, Upstreams: []string{"127.0.0.1:3000"}}}
}

// TestSharedPreservesExternalConfigurationAndCertificates exercises deployment, scale-to-zero and route removal.
func TestSharedPreservesExternalConfigurationAndCertificates(t *testing.T) {
	o := sharedFixture(t)
	writeLiveCertificates(t, o.Options, 1)
	before, _ := os.ReadFile(o.MainConfig)
	var calls []string
	o.Run = func(_ context.Context, file string, args ...string) error { // Capture this fixture operation independently of real host service changes.
		calls = append(calls, file+" "+strings.Join(args, " "))
		return nil
	}
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", sharedRoute()); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	fragment, _ := os.ReadFile(o.Fragment)
	assertNamedFragment(t, fragment)
	if got := strings.Count(string(fragment), "client_max_body_size 2m;"); got != 1 {
		t.Fatalf("expected one controller-only upload limit, got %d", got)
	}
	if !strings.Contains(string(fragment), "client_max_body_size 1m;") || !strings.Contains(string(fragment), `location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$" { client_max_body_size 2m;`) {
		t.Fatal("controller artifact location did not override its smaller default body limit")
	}
	routes := sharedRoute()
	routes[0].Unavailable = true
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", routes); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", nil); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	after, _ := os.ReadFile(o.MainConfig)
	if string(before) != string(after) { // Fail if external configuration changed.
		t.Fatal("external configuration changed")
	}
	for _, call := range calls { // Fail if unexpected mutation.
		if !strings.HasPrefix(call, "nginx ") || !strings.HasSuffix(call, o.MainConfig) { // Fail if unexpected mutation.
			t.Fatal("unexpected mutation", call)
		}
	}
	if err := CheckSharedController(context.Background(), o, "controller.example.test"); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
}

// assertNamedFragment verifies Hostlier reserves only its explicit hostnames, never catch-all traffic.
func assertNamedFragment(t *testing.T, fragment []byte) {
	t.Helper()
	if !strings.Contains(string(fragment), "server_name app.example.test") || strings.Contains(string(fragment), "server_name _") || strings.Contains(string(fragment), "default_server") { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(string(fragment))
	}
}

// TestSharedRejectsExternalHostnameOwnership includes exact names, wildcard names and ambiguous regex configuration.
func TestSharedRejectsExternalHostnameOwnership(t *testing.T) {
	for _, name := range []string{"app.example.test", "*.example.test", ".example.test", "app.*", "~^app", "~^.*\\.example\\.test$", "~^.*\\.example\\.test$|.*\\.other\\.test$", "$hostname"} { // Exercise each relevant coexistence behavior independently.
		t.Run(name, func(t *testing.T) { // Capture this fixture operation independently of real host service changes.
			o := sharedFixture(t)
			data, _ := os.ReadFile(o.MainConfig)
			os.WriteFile(o.MainConfig, []byte(strings.ReplaceAll(string(data), "existing.example.test", name)), 0600)
			if _, err := InspectShared(context.Background(), o, []string{"app.example.test"}); err == nil { // Fail if foreign hostname accepted.
				t.Fatal("foreign hostname accepted")
			}
			if _, err := os.Stat(o.Fragment); !os.IsNotExist(err) { // Fail if read-only inspection wrote include.
				t.Fatal("read-only inspection wrote include")
			}
		})
	}
}

// TestSharedAllowsDisjointRegexNames preserves unrelated existing regex virtual hosts.
func TestSharedAllowsDisjointRegexNames(t *testing.T) {
	for index, name := range []string{
		`~^[a-z0-9-]+-exec\.run\.example\.test$`,
		`~^[a-z0-9-]+\.run\.example\.test$`,
		`~^[a-z0-9-]+(?:-exec)?\.run\.example\.test$`,
		`~^[a-z0-9-]+\.run\.other\.test$`,
	} {
		t.Run(fmt.Sprintf("regex_%d", index), func(t *testing.T) {
			o := sharedFixture(t)
			data, _ := os.ReadFile(o.MainConfig)
			if err := os.WriteFile(o.MainConfig, []byte(strings.ReplaceAll(string(data), "existing.example.test", name)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectShared(context.Background(), o, []string{"controller.example.test"}); err != nil {
				t.Fatal("disjoint Nginx regex blocked", err)
			}
			if _, err := InspectShared(context.Background(), o, []string{"app-exec.run.example.test"}); err == nil && strings.Contains(name, "example") {
				t.Fatal("overlapping Nginx regex accepted")
			}
		})
	}
}

// TestSharedRequiresExistingHTTPIncludeAndDefaults prevents a new include from changing unmatched traffic.
func TestSharedRequiresExistingHTTPIncludeAndDefaults(t *testing.T) {
	for _, mutation := range []string{"default_server", "include"} { // Exercise each relevant coexistence behavior independently.
		t.Run(mutation, func(t *testing.T) { // Capture this fixture operation independently of real host service changes.
			o := sharedFixture(t)
			data, _ := os.ReadFile(o.MainConfig)
			os.WriteFile(o.MainConfig, []byte(strings.ReplaceAll(string(data), mutation, "")), 0600)
			if _, err := InspectShared(context.Background(), o, []string{"app.example.test"}); err == nil { // Fail if unsafe existing layout accepted.
				t.Fatal("unsafe existing layout accepted")
			}
		})
	}
}

// TestSharedRejectsUnownedAndEditedIncludes checks both the initial boundary and drift after a deployment.
func TestSharedRejectsUnownedAndEditedIncludes(t *testing.T) {
	o := sharedFixture(t)
	os.WriteFile(o.Fragment, []byte("operator file"), 0600)
	if _, err := InspectShared(context.Background(), o, nil); err == nil { // Fail if unowned include accepted.
		t.Fatal("unowned include accepted")
	}
	os.Remove(o.Fragment)
	writeLiveCertificates(t, o.Options, 1)
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", nil); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	os.WriteFile(o.Fragment, []byte("manual edit"), 0600)
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", sharedRoute()); err == nil { // Fail if manual edit overwritten.
		t.Fatal("manual edit overwritten")
	}
	data, _ := os.ReadFile(o.Fragment)
	if string(data) != "manual edit" { // Fail if drift was changed.
		t.Fatal("drift was changed")
	}
}

// TestSharedRollbackRestoresInclude verifies full-config rejection, reload failure and ACME failure independently.
func TestSharedRollbackRestoresInclude(t *testing.T) {
	for _, failure := range []string{"validation", "reload", "issuance"} { // Exercise each relevant coexistence behavior independently.
		t.Run(failure, func(t *testing.T) { // Capture this fixture operation independently of real host service changes.
			o := sharedFixture(t)
			writeLiveCertificates(t, o.Options, 1)
			if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", nil); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
				t.Fatal(err)
			}
			before, _ := os.ReadFile(o.Fragment)
			if failure == "issuance" { // Require this fixture step to succeed before checking coexistence behavior.
				os.RemoveAll(filepath.Join(o.CertificateDirectory, "live", "app.example.test"))
			}
			failed := false
			o.Run = func(_ context.Context, file string, args ...string) error { // Capture this fixture operation independently of real host service changes.
				match := sharedFailureMatches(failure, file, args)
				if match && !failed { // Require this fixture step to succeed before checking coexistence behavior.
					failed = true
					return errors.New("injected failure")
				}
				return nil
			}
			if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", sharedRoute()); err == nil { // Fail if failure hidden.
				t.Fatal("failure hidden")
			}
			after, _ := os.ReadFile(o.Fragment)
			if !failed || string(before) != string(after) { // Fail if prior include was not restored.
				t.Fatal("prior include was not restored")
			}
			if _, err := sharedPrevious(o); err != nil { // Fail if rollback lost ownership.
				t.Fatal("rollback lost ownership", err)
			}
		})
	}
}

// sharedFailureMatches targets one stage while allowing its recovery commands to run normally.
func sharedFailureMatches(failure, file string, args []string) bool {
	switch failure { // Exercise each relevant coexistence behavior independently.
	case "validation": // Exercise each relevant coexistence behavior independently.
		return args[0] == "-t"
	case "reload": // Exercise each relevant coexistence behavior independently.
		return args[0] == "-s"
	case "issuance": // Exercise each relevant coexistence behavior independently.
		return file == "systemctl" && args[0] == "start"
	}
	return false
}

// TestSharedExternalTLSNeverInvokesCertbot keeps referenced certificate and key files externally managed.
func TestSharedExternalTLSNeverInvokesCertbot(t *testing.T) {
	o := sharedFixture(t)
	external := Options{CertificateDirectory: filepath.Join(t.TempDir(), "external")}
	writeLiveCertificates(t, external, 1)
	refs := map[string]CertificateReference{}
	for _, host := range []string{"app.example.test", "controller.example.test"} { // Exercise each relevant coexistence behavior independently.
		refs[host] = CertificateReference{Certificate: filepath.Join(external.CertificateDirectory, "live", host, "fullchain.pem"), Key: filepath.Join(external.CertificateDirectory, "live", host, "privkey.pem")}
	}
	data, _ := json.Marshal(refs)
	os.WriteFile(o.CertificatesFile, data, 0600)
	o.Run = func(_ context.Context, file string, _ ...string) error { // Fail if external certificate renewal attempted.
		if file != "nginx" { // Fail if external certificate renewal attempted.
			t.Fatal("external certificate renewal attempted", file)
		}
		return nil
	}
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", sharedRoute()); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	if _, err := os.Stat(o.CertificateDirectory); !os.IsNotExist(err) { // Fail if external certificate was copied.
		t.Fatal("external certificate was copied")
	}
	fragment, _ := os.ReadFile(o.Fragment)
	if !strings.Contains(string(fragment), refs["app.example.test"].Certificate) { // Fail if external certificate reference missing.
		t.Fatal("external certificate reference missing")
	}
	if err := validateCertificate("wrong.example.test", refs["app.example.test"]); err == nil { // Fail if certificate hostname mismatch accepted.
		t.Fatal("certificate hostname mismatch accepted")
	}
}

// TestSharedACMEDirectoriesAreIsolated checks issuance and renewal never share the operator's Certbot configuration.
func TestSharedACMEDirectoriesAreIsolated(t *testing.T) {
	o := sharedFixture(t)
	var invocation string
	o.Run = func(_ context.Context, file string, args ...string) error { // Capture this fixture operation independently of real host service changes.
		invocation = file + " " + strings.Join(args, " ")
		return nil
	}
	if err := ensureCertificate(context.Background(), o.Options, "app.example.test"); err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	service, _, err := SharedRenewalAssets(o)
	if err != nil { // Require this fixture step to succeed before checking coexistence behavior.
		t.Fatal(err)
	}
	issue, err := SharedCertificateServiceAssets(o)
	if err != nil {
		t.Fatal(err)
	}
	if invocation != "systemctl start hostlier-nginx-existing-cert@app.example.test.service" {
		t.Fatal("issuance did not use the dedicated certificate job", invocation)
	}
	for _, expected := range []string{"--config /dev/null", "--config-dir " + o.CertificateDirectory, "--work-dir " + o.CertificateDirectory + "/work", "--logs-dir " + o.CertificateDirectory + "/logs"} { // Fail if shared ACME state.
		if !strings.Contains(issue, expected) || !strings.Contains(service, expected) { // Fail if shared ACME state.
			t.Fatal("shared ACME state", issue, service)
		}
	}
	if !strings.Contains(issue, "--cert-name %i -d %i") || !strings.Contains(issue, "NoNewPrivileges=false") || !strings.Contains(issue, "ProtectHome=false") {
		t.Fatal("certificate job lost exact-host identity or inherited Snap-incompatible restrictions", issue)
	}
}

// TestSharedFirstHostnameIssuesAfterHTTPBootstrap exercises the transition from
// an existing controller certificate to the first application certificate.
func TestSharedFirstHostnameIssuesAfterHTTPBootstrap(t *testing.T) {
	o := sharedFixture(t)
	writeLiveCertificates(t, o.Options, 1)
	if err := os.RemoveAll(filepath.Join(o.CertificateDirectory, "live", "app.example.test")); err != nil {
		t.Fatal(err)
	}
	var calls []string
	o.Run = func(_ context.Context, file string, args ...string) error {
		calls = append(calls, file+" "+strings.Join(args, " "))
		if file == "systemctl" {
			if len(args) != 2 || args[0] != "start" || args[1] != "hostlier-nginx-existing-cert@app.example.test.service" {
				t.Fatalf("unexpected issuance command: %q", calls[len(calls)-1])
			}
			bootstrap, err := os.ReadFile(o.Fragment)
			if err != nil {
				return err
			}
			if !strings.Contains(string(bootstrap), "server_name app.example.test; location ^~ /.well-known/acme-challenge/") || strings.Contains(string(bootstrap), "server_name app.example.test; ssl_certificate") {
				t.Fatal("ACME challenge was not published before issuance")
			}
			writeLiveCertificates(t, o.Options, 2)
		}
		return nil
	}
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", sharedRoute()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 || !strings.HasPrefix(calls[0], "nginx -t ") || !strings.HasPrefix(calls[1], "nginx -s reload ") || !strings.HasPrefix(calls[2], "systemctl start ") || !strings.HasPrefix(calls[3], "nginx -t ") || !strings.HasPrefix(calls[4], "nginx -s reload ") {
		t.Fatal("unexpected activation order", calls)
	}
	active, err := os.ReadFile(o.Fragment)
	if err != nil || !strings.Contains(string(active), "server_name app.example.test; ssl_certificate") {
		t.Fatal("application TLS route was not activated", err)
	}
}
