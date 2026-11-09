package model

import "testing"

// TestTailLines covers newline boundaries, empty output and zero-line requests.
func TestTailLines(t *testing.T) {
	// Cover terminated, unterminated and blank logical lines.
	for _, test := range []struct {
		text  string
		count int
		want  string
	}{
		{"a\nb\nc\n", 2, "b\nc\n"}, {"a\nb\nc", 1, "c"},
		{"a\n\n", 1, "\n"}, {"a\n", 0, ""}, {"", 10, ""}, {"a\n", 5, "a\n"},
	} {
		// Logical lines must retain their original newline representation.
		if got := TailLines(test.text, test.count); got != test.want {
			t.Fatalf("%q: got %q want %q", test.text, got, test.want)
		}
	}
}
