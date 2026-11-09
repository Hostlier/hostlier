// Package contracttest supplies reusable executable-package protocol conformance checks.
package contracttest

import (
	"bytes"
	"context"
	"encoding/json"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
	"testing"
)

// Exchange exercises a package's real bounded stdin/stdout boundary and decodes its sole response.
func Exchange(t testing.TB, handler packagekit.Handler, req workflow.Request) workflow.Response {
	t.Helper()
	raw, err := json.Marshal(req)
	// Fixtures must themselves be valid JSON before invoking package code.
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// The shared SDK exercises the exact protocol validation used by package executables.
	if err = packagekit.Serve(context.Background(), handler, bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var response workflow.Response
	// A successful executable must return valid protocol JSON.
	if err = json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

// RejectMalformed verifies that extra documents and unknown fields cannot invoke a package handler.
func RejectMalformed(t testing.TB, handler packagekit.Handler) {
	t.Helper()
	// Neither form may be accepted as a protocol request.
	for _, raw := range []string{`{"version":1,"action":"plan","key":"test","step":{},"unexpected":true}`, `{"version":1,"action":"plan","key":"test","step":{}} {}`} {
		var out bytes.Buffer
		// An invalid request must fail before any successful response is emitted.
		if packagekit.Serve(context.Background(), handler, bytes.NewBufferString(raw), &out) == nil || out.Len() != 0 {
			t.Fatal("malformed request accepted")
		}
	}
}
