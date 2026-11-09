package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestLogsTail validates counts before requests and preserves component selection.
func TestLogsTail(t *testing.T) {
	f := &fakeClient{reply:
	// Return a longer snapshot to verify compatibility with older controllers.
	func(c call) (any, error) { return map[string]string{"text": "one\ntwo\nthree\n"}, nil }}
	output, err := invoke([]string{"logs", "api", "--tail", "2", "--component", "worker"}, "", Options{Client: f})
	// The server receives the explicit limit and the selected sidecar.
	if err != nil || f.calls[0].path != "/v1/apps/api/logs?component=worker&tail=2" || strings.TrimSpace(output) != "two\nthree" {
		t.Fatal(output, err, f.calls)
	}
	// Reject malformed and unbounded requests before remote I/O.
	for _, value := range []string{"-1", "10001", "all", "1.5", ""} {
		f.calls = nil
		_, err := invoke([]string{"logs", "api", "--tail", value}, "", Options{Client: f})
		// Invalid input cannot issue any remote request.
		if err == nil || len(f.calls) != 0 {
			t.Fatal("invalid tail accepted", value)
		}
	}
}

// TestLogsTailFollow prints the initial selection then only new lines from rolling snapshots.
func TestLogsTailFollow(t *testing.T) {
	// Both zero and nonzero tails must establish a full follow baseline.
	for _, count := range []string{"0", "1"} {
		ctx, cancel := context.WithCancel(context.Background())
		f := &fakeClient{}
		// Supply consecutive snapshots with overlapping lines.
		f.reply = func(c call) (any, error) {
			// The second snapshot rolls out an old line and cancels after its output is read.
			if len(f.calls) == 2 {
				cancel()
				return map[string]string{"text": "two\nthree\nfour\n"}, nil
			}
			return map[string]string{"text": "one\ntwo\nthree\n"}, nil
		}
		var out bytes.Buffer
		err := RunWithOptions(ctx, []string{"logs", "api", "--tail", count, "--follow"}, strings.NewReader(""), &out, io.Discard, Options{Client: f, PollInterval: time.Millisecond})
		cancel()
		want := []string{"four"}
		// One initial line is displayed only for the nonzero-tail case.
		if count == "1" {
			want = []string{"three", "four"}
		}
		// Rolling history must not be printed twice, and cancellation must terminate polling.
		if !errors.Is(err, context.Canceled) || strings.Join(strings.Fields(out.String()), ",") != strings.Join(want, ",") || f.calls[0].path != "/v1/apps/api/logs?tail=200" {
			t.Fatal(count, out.String(), err, f.calls)
		}
	}
}
