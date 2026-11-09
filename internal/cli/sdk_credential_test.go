package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hostlier/internal/client"
)

func TestSDKCredentialExportIsPrivateAndNeverPrintsSecret(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credential := client.EncodeDeviceCredential("secret-token", private)
	if err := client.NewStore(home).Save("central-service", "https://controller.example.com", "client-1", credential); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "central-controller.json")
	output, err := invoke([]string{"sdk", "credential", "export", "--server", "central-service", "--output", path}, "", Options{CWD: root, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, credential) || strings.Contains(output, "secret-token") {
		t.Fatal("credential leaked to CLI output")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential file permissions", err)
	}
	var exported struct{ ControllerURL, Credential, ClientID string }
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &exported) != nil || exported.ControllerURL != "https://controller.example.com" || exported.Credential != credential || exported.ClientID != "client-1" {
		t.Fatal("credential export did not isolate the selected signed profile", err)
	}
	if _, err := invoke([]string{"sdk", "credential", "export", "--server", "central-service", "--output", path}, "", Options{CWD: root, Home: home}); err == nil {
		t.Fatal("credential export overwrote an existing file")
	}
}

func TestSDKCredentialExportRejectsLegacyBearerProfile(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	if err := client.NewStore(home).Save("legacy", "https://controller.example.com", "client-1", "bare-token"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "legacy.json")
	if _, err := invoke([]string{"sdk", "credential", "export", "--server", "legacy", "--output", path}, "", Options{CWD: root, Home: home}); err == nil {
		t.Fatal("exported bearer-only credential")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created a rejected credential file")
	}
}
