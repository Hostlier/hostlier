package model

import (
	"context"
	"strconv"
	"strings"
)

// logTailKey keeps the bounded log-read policy separate from other context values.
type logTailKey struct{}

// ParseLogTail bounds explicit snapshot requests while retaining zero for follow-from-now.
func ParseLogTail(value string) (int, error) {
	n, err := strconv.Atoi(value)
	// Unlimited reads and malformed counts must not reach runtime backends.
	if err != nil || n < 0 || n > 10000 {
		return 0, Err("ARGUMENTS", "--tail must be an integer between 0 and 10000")
	}
	return n, nil
}

// WithLogTail propagates a validated per-request line limit through runtime log readers.
func WithLogTail(ctx context.Context, lines int) context.Context {
	return context.WithValue(ctx, logTailKey{}, lines)
}

// LogTail preserves the existing 200-line default when no explicit limit was selected.
func LogTail(ctx context.Context) int {
	lines, ok := ctx.Value(logTailKey{}).(int)
	// Ordinary runtime callers retain their bounded default.
	if !ok {
		return 200
	}
	return lines
}

// TailLines preserves blank lines and a final newline while selecting the newest logical lines.
func TailLines(text string, count int) string {
	// Zero lines intentionally suppresses the initial snapshot.
	if count == 0 {
		return ""
	}
	lines := strings.SplitAfter(text, "\n")
	end := len(lines)
	// A terminating newline does not introduce an additional empty logical line.
	if lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[max(0, end-count):end], "")
}
