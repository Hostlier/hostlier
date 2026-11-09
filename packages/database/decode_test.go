package database_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"hostlier/packages/aiven"
	"hostlier/packages/database"
)

// unreadableConfiguration simulates a reader whose diagnostic contains an accidentally pasted credential.
type unreadableConfiguration struct{}

// Read returns no usable configuration while supplying a detail that must never reach parser diagnostics.
func (unreadableConfiguration) Read([]byte) (int, error) {
	return 0, errors.New("sensitive-reader-detail")
}

// TestDecodeSizeBoundaryAndReadFailure distinguishes the accepted byte boundary from oversized or unreadable input.
func TestDecodeSizeBoundaryAndReadFailure(t *testing.T) {
	prefix := "version: 1\n#"
	valid := prefix + strings.Repeat("x", 65536-len(prefix))
	var target aiven.Config
	// Exactly 64 KiB remains valid when the YAML structure itself is small.
	if err := database.Decode(strings.NewReader(valid), &target); err != nil {
		t.Fatal(err)
	}
	// Both an extra byte and a failing source must be rejected without copying source diagnostics.
	for _, reader := range []io.Reader{strings.NewReader(valid + "x"), unreadableConfiguration{}} {
		err := database.Decode(reader, &target)
		// Neither failure path may expose the reader's sensitive error detail.
		if err == nil || strings.Contains(err.Error(), "sensitive-reader-detail") {
			t.Fatalf("unsafe read failure: %v", err)
		}
	}
}

// TestDecodeRejectsExcessiveNesting verifies the structural guard runs before typed schema decoding.
func TestDecodeRejectsExcessiveNesting(t *testing.T) {
	nested := ""
	// Build a small document whose depth, rather than byte size, exceeds the parser contract.
	for depth := 0; depth < 14; depth++ {
		nested += strings.Repeat("  ", depth) + "nested:\n"
	}
	nested += strings.Repeat("  ", 14) + "value: hidden-input\n"
	var target aiven.Config
	err := database.Decode(strings.NewReader(nested), &target)
	// The tree guard must reject excessive structure without reproducing nested scalar content.
	if err == nil || !strings.Contains(err.Error(), "nesting") || strings.Contains(err.Error(), "hidden-input") {
		t.Fatalf("unsafe nesting result: %v", err)
	}
}
