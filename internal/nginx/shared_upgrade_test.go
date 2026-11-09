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

// oldSharedControllerFixture models an owned include published before chunk-specific body limits.
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
	location := strings.Index(server, ` location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$"`)
	if location < 0 {
		t.Fatal("chunk location missing from fixture")
	}
	lineEnd := strings.IndexByte(server[location:], '\n')
	if lineEnd < 0 {
		t.Fatal("unterminated chunk location")
	}
	server = server[:location] + server[location+lineEnd+1:]
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
	if !bytes.Contains(updated[newStart:newEnd], []byte(`location ~ "^/v1/artifacts/[a-f0-9]{64}/chunks$" { client_max_body_size 2m;`)) || !bytes.Contains(updated[newStart:newEnd], []byte("client_max_body_size 1m;")) {
		t.Fatal("controller chunk body limit was not published")
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
