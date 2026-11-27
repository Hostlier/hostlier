package nginx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldSharedControllerFixture models an owned include published before route-specific body limits.
func oldSharedControllerFixture(t *testing.T, o SharedOptions) []byte {
	t.Helper()
	writeLiveCertificates(t, o.Options, 1)
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", sharedRoute()); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(o.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	start, end, err := sharedControllerServerSpan(current, o.HTTPSAddress, "controller.example.test")
	if err != nil {
		t.Fatal(err)
	}
	server := string(current[start:end])
	server = strings.Replace(server, " client_max_body_size 1m;\n", "", 1)
	for _, prefix := range []string{
		` location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$"`,
		` location ~ "^/v1/(hosts/[^/]+/proxy/)?(plans|planning)$"`,
	} {
		location := strings.Index(server, prefix)
		if location < 0 {
			t.Fatalf("missing fixture location %q", prefix)
		}
		lineEnd := strings.IndexByte(server[location:], '\n')
		if lineEnd < 0 {
			t.Fatalf("unterminated fixture location %q", prefix)
		}
		server = server[:location] + server[location+lineEnd+1:]
	}
	server = strings.Replace(server, " location / {", " location ^~ / {", 1)
	legacy := append([]byte{}, current[:start]...)
	legacy = append(legacy, server...)
	legacy = append(legacy, current[end:]...)
	if err := recordShared(o, contentHash(legacy)); err != nil {
		t.Fatal(err)
	}
	if err := writeConfig(o.Fragment, legacy); err != nil {
		t.Fatal(err)
	}
	return legacy
}

func TestUpgradeSharedControllerPreservesAppRoutesAndCertificates(t *testing.T) {
	o := sharedFixture(t)
	legacy := oldSharedControllerFixture(t, o)
	oldStart, oldEnd, err := sharedControllerServerSpan(legacy, o.HTTPSAddress, "controller.example.test")
	if err != nil {
		t.Fatal(err)
	}
	mainBefore, _ := os.ReadFile(o.MainConfig)
	certPath := filepath.Join(o.CertificateDirectory, "live", "controller.example.test", "fullchain.pem")
	certBefore, _ := os.ReadFile(certPath)
	var commands []string
	o.Run = func(_ context.Context, file string, args ...string) error {
		commands = append(commands, file+" "+strings.Join(args, " "))
		return nil
	}
	if err := UpgradeSharedController(context.Background(), o, "controller.example.test", "127.0.0.1:7463"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(o.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	newStart, newEnd, err := sharedControllerServerSpan(updated, o.HTTPSAddress, "controller.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacy[:oldStart], updated[:newStart]) || !bytes.Equal(legacy[oldEnd:], updated[newEnd:]) {
		t.Fatal("application routes or other owned configuration changed")
	}
	for _, directive := range []string{
		"client_max_body_size 1m;",
		`location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$" { client_max_body_size 2m;`,
		`location ~ "^/v1/(hosts/[^/]+/proxy/)?(plans|planning)$" { client_max_body_size 2m;`,
	} {
		if !bytes.Contains(updated[newStart:newEnd], []byte(directive)) {
			t.Fatalf("updated controller route is missing %q", directive)
		}
	}
	mainAfter, _ := os.ReadFile(o.MainConfig)
	certAfter, _ := os.ReadFile(certPath)
	if !bytes.Equal(mainBefore, mainAfter) || !bytes.Equal(certBefore, certAfter) {
		t.Fatal("external Nginx configuration or certificate changed")
	}
	if len(commands) != 2 || !strings.Contains(commands[0], "nginx -t ") || !strings.Contains(commands[1], "nginx -s reload ") {
		t.Fatal("candidate was not validated and gracefully reloaded", commands)
	}
	if err := CheckSharedController(context.Background(), o, "controller.example.test"); err != nil {
		t.Fatal("upgraded include lost ownership or TLS readiness", err)
	}
	// A repeat init must not reload Nginx when the owned controller route is current.
	if err := UpgradeSharedController(context.Background(), o, "controller.example.test", "127.0.0.1:7463"); err != nil || len(commands) != 2 {
		t.Fatal("unchanged controller route was republished", commands, err)
	}
	if err := os.WriteFile(o.ControllerBodyLimitFile, []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeSharedController(context.Background(), o, "controller.example.test", "127.0.0.1:7463"); err != nil {
		t.Fatal(err)
	}
	unlimited, err := os.ReadFile(o.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	start, end, err := sharedControllerServerSpan(unlimited, o.HTTPSAddress, "controller.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(unlimited[start:end], []byte("client_max_body_size 0;")) || bytes.Count(unlimited[start:end], []byte("client_max_body_size ")) != 1 {
		t.Fatal("controller route did not publish the operator's override")
	}
	if !bytes.Equal(updated[:newStart], unlimited[:start]) || !bytes.Equal(updated[newEnd:], unlimited[end:]) {
		t.Fatal("body limit override changed application routes")
	}
}

func TestUpgradeSharedControllerPreservesExternalTLSReferences(t *testing.T) {
	o := sharedFixture(t)
	external := Options{CertificateDirectory: filepath.Join(t.TempDir(), "external")}
	writeLiveCertificates(t, external, 1)
	refs := map[string]CertificateReference{}
	for _, host := range []string{"app.example.test", "controller.example.test"} {
		refs[host] = CertificateReference{
			Certificate: filepath.Join(external.CertificateDirectory, "live", host, "fullchain.pem"),
			Key:         filepath.Join(external.CertificateDirectory, "live", host, "privkey.pem"),
		}
	}
	data, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.CertificatesFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	oldSharedControllerFixture(t, o)
	keyBefore, _ := os.ReadFile(refs["controller.example.test"].Key)
	o.Run = func(_ context.Context, file string, _ ...string) error {
		if file != "nginx" {
			t.Fatal("external certificate renewal attempted", file)
		}
		return nil
	}
	if err := UpgradeSharedController(context.Background(), o, "controller.example.test", "127.0.0.1:7463"); err != nil {
		t.Fatal(err)
	}
	fragment, _ := os.ReadFile(o.Fragment)
	keyAfter, _ := os.ReadFile(refs["controller.example.test"].Key)
	refsAfter, _ := os.ReadFile(o.CertificatesFile)
	if !bytes.Contains(fragment, []byte(refs["controller.example.test"].Certificate)) || !bytes.Contains(fragment, []byte(refs["app.example.test"].Certificate)) || !bytes.Equal(keyBefore, keyAfter) || !bytes.Equal(data, refsAfter) {
		t.Fatal("external certificate reference or key changed")
	}
}

func TestUpgradeSharedControllerRollsBackFailedPublication(t *testing.T) {
	for _, failure := range []string{"-t", "-s"} {
		t.Run(failure, func(t *testing.T) {
			o := sharedFixture(t)
			legacy := oldSharedControllerFixture(t, o)
			failed := false
			o.Run = func(_ context.Context, _ string, args ...string) error {
				if args[0] == failure && !failed {
					failed = true
					return errors.New("injected failure")
				}
				return nil
			}
			if err := UpgradeSharedController(context.Background(), o, "controller.example.test", "127.0.0.1:7463"); err == nil || !failed {
				t.Fatal("publication failure was hidden", err)
			}
			after, _ := os.ReadFile(o.Fragment)
			if !bytes.Equal(after, legacy) {
				t.Fatal("failed upgrade changed owned routes")
			}
			if _, err := sharedPrevious(o); err != nil {
				t.Fatal("rollback lost include ownership", err)
			}
		})
	}
}

func TestUpgradeSharedControllerRejectsEditedInclude(t *testing.T) {
	o := sharedFixture(t)
	legacy := oldSharedControllerFixture(t, o)
	edited := append(append([]byte{}, legacy...), []byte("# operator edit\n")...)
	if err := os.WriteFile(o.Fragment, edited, 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeSharedController(context.Background(), o, "controller.example.test", "127.0.0.1:7463"); err == nil {
		t.Fatal("externally edited include was replaced")
	}
	after, _ := os.ReadFile(o.Fragment)
	if !bytes.Equal(after, edited) {
		t.Fatal("edited include changed")
	}
}

// TestSharedHTTP2DirectiveSelectionAndUpgrade verifies modern "http2 on;" rendering and 1.24 compatibility.
func TestSharedHTTP2DirectiveSelectionAndUpgrade(t *testing.T) {
	o := sharedFixture(t)
	writeLiveCertificates(t, o.Options, 1)
	withNginxVersion(t, "nginx version: nginx/1.24.0 (Ubuntu)")
	// Render the legacy 1.24 listener syntax first for both app and controller routes.
	if err := ApplyShared(context.Background(), o, "controller.example.test", "127.0.0.1:7463", sharedRoute()); err != nil {
		t.Fatal(err)
	}
	legacy, _ := os.ReadFile(o.Fragment)
	// Confirm 1.24 uses the listen parameter before 1.25.1 introduced the http2 directive.
	if strings.Count(string(legacy), "listen 443 ssl http2;") != 2 || strings.Contains(string(legacy), "http2 on;") {
		t.Fatalf("unexpected 1.24 config: %s", legacy)
	}
	withNginxVersion(t, "nginx version: nginx/1.28.3 (Ubuntu)")
	// Upgrading on Nginx 1.25.1+ replaces the deprecated listen parameter with "http2 on;".
	if err := UpgradeSharedController(context.Background(), o, "controller.example.test", "127.0.0.1:7463"); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(o.Fragment)
	// Verify the upgraded include has no deprecated "listen ... http2" directive on any route.
	if strings.Contains(string(updated), "ssl http2;") || strings.Count(string(updated), "listen 443 ssl; http2 on;") != 2 {
		t.Fatalf("deprecated listen http2 remained in include: %s", updated)
	}
}
