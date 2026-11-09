package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// TestUIStartsBeforeControllerPairing verifies first-run CLI hosting requires no saved server profile.
func TestUIStartsBeforeControllerPairing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	// The actual UI command owns its local listener and stops on caller cancellation.
	go func() {
		err := RunWithOptions(ctx, []string{"ui", "--port", "0"}, strings.NewReader(""), writer, io.Discard, Options{Home: t.TempDir()})
		writer.CloseWithError(err)
		done <- err
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	// A printed session URL proves the embedded UI starts before any controller enrollment.
	if err != nil || !strings.HasPrefix(line, "http://127.0.0.1:") || !strings.Contains(line, "#session=") {
		t.Fatal("first-run UI did not start", line, err)
	}
	cancel()
	// Stopping an unpaired local UI must join its observer without creating a remote request.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// pairingReviewClient records exact administrator review calls without simulating hardware approval.
type pairingReviewClient struct{ paths []string }

// Request returns one public review and one explicit administrator approval receipt.
func (c *pairingReviewClient) Request(_ context.Context, _ string, path string, body, result any) error {
	c.paths = append(c.paths, path)
	data, _ := json.Marshal(map[string]any{"name": "local-ui", "status": "pending", "approved": path == "/v1/pairing/approve"})
	return json.Unmarshal(data, result)
}

// TestPairingCodeFromUIRequiresInteractiveServerReview checks the exact command displayed by the connection panel.
func TestPairingCodeFromUIRequiresInteractiveServerReview(t *testing.T) {
	remote := &pairingReviewClient{}
	_, err := invoke([]string{"pairing", "approve", "0123456789abcdef", "--socket", "/run/hostlier/controller.sock"}, "", Options{Client: remote, Home: t.TempDir(), Prompt: func(string, bool) (string, error) { return "yes", nil }}) // Explicit server-terminal consent preserves the local administrator boundary.
	// The trusted Unix route performs identity review before granting this device enrollment.
	if err != nil || strings.Join(remote.paths, ",") != "/v1/pairing/inspect,/v1/pairing/approve" {
		t.Fatal("UI pairing command failed", remote.paths, err)
	}
	remote = &pairingReviewClient{}
	_, err = invoke([]string{"pairing", "approve", "0123456789abcdef", "--socket", "/run/hostlier/controller.sock"}, "", Options{Client: remote, Home: t.TempDir()})
	// Merely supplying a displayed code never removes the original interactive review requirement.
	if err == nil || len(remote.paths) != 0 {
		t.Fatal("noninteractive local pairing bypassed review")
	}
}
