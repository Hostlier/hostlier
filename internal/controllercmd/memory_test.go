package controllercmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestMemoryModeRejectsMixedRuntimeBeforeIO(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--mode", "invalid"},
		{"run", "--mode", "memory", "--runtime", "docker"},
		{"agent", "run", "--mode", "memory", "--controller-fingerprint", strings.Repeat("a", 64)},
		{"run", "--mode", "memory", "--tunnel-url", "https://example.com"},
	} {
		var out bytes.Buffer
		err := Run(context.Background(), args, strings.NewReader(""), &out, &out)
		if err == nil || (!strings.Contains(err.Error(), "Memory mode") && !strings.Contains(err.Error(), "Mode must")) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestMemoryPlatformPaths(t *testing.T) {
	for _, tc := range []struct{ platform, override, state, config, socket string }{
		{"darwin", "", "/Users/test/Library/Application Support/Hostlier/memory", "/Users/test/Library/Application Support/Hostlier/memory/config.yaml", "/Users/test/Library/Application Support/Hostlier/memory/controller.sock"},
		{"darwin", "/private/storage-state", "/private/storage-state", "/private/storage-state/config.yaml", "/private/storage-state/controller.sock"},
		{"linux", "", "/var/lib/hostlier-memory", "/etc/hostlier/config.yaml", ""},
	} {
		state, config, socket := memoryPaths(tc.platform, "/Users/test/Library/Application Support", tc.override)
		if state != tc.state || config != tc.config || socket != tc.socket {
			t.Fatalf("%s: %q %q %q", tc.platform, state, config, socket)
		}
	}
}
