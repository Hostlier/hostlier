package cli

import (
	"encoding/json"
	"hostlier/internal/model"
	"net/url"
	"strconv"
	"strings"
)

// logSnapshotPath validates --tail and keeps follow polling useful even when the initial tail is zero.
func (s *session) logSnapshotPath(path string) (string, int, error) {
	value, supplied := s.flags["tail"]
	// Omitted tail preserves the existing server defaults.
	if !supplied {
		return path, -1, nil
	}
	count, err := model.ParseLogTail(value)
	// Reject invalid limits before making a controller request.
	if err != nil {
		return "", 0, err
	}
	u, err := url.Parse(path)
	// Preserve component and operation filters in the existing URL.
	if err != nil {
		return "", 0, err
	}
	fetch := count
	// Follow needs a recent baseline to detect lines added after the first snapshot.
	if s.flags["follow"] != "" {
		fetch = max(200, count)
	}
	query := u.Query()
	query.Set("tail", strconv.Itoa(fetch))
	u.RawQuery = query.Encode()
	return u.String(), count, nil
}

// renderLogSnapshot limits initial text lines or structured events without breaking JSON output.
func renderLogSnapshot(result logResponse, tail int) string {
	// Structured controller logs count events rather than lines in JSON serialization.
	if result.Entries != nil {
		entries, ok := result.Entries.([]any)
		// Decoded JSON arrays can be safely sliced before re-encoding.
		if ok && tail >= 0 {
			result.Entries = entries[max(0, len(entries)-tail):]
		}
		raw, _ := json.Marshal(result.Entries)
		return string(raw)
	}
	// A negative sentinel preserves the full bounded runtime snapshot.
	if tail < 0 {
		return result.Text
	}
	return model.TailLines(result.Text, tail)
}

// logSnapshotDelta removes shared lines when bounded snapshots roll forward.
func logSnapshotDelta(previous, current string) string {
	// Growing logs and unchanged snapshots share their entire previous prefix.
	if strings.HasPrefix(current, previous) {
		return strings.TrimPrefix(current, previous)
	}
	// Find the longest complete-line overlap without repeating an entire rolling window.
	for index, char := range previous {
		// Only line boundaries represent a safe suffix of the previous snapshot.
		if char == '\n' && strings.HasPrefix(current, previous[index+1:]) {
			return strings.TrimPrefix(current, previous[index+1:])
		}
	}
	return current
}

// followLogs prints the requested initial tail, then polls for new bounded output until cancellation.
func (s *session) followLogs(path string) error {
	path, tail, err := s.logSnapshotPath(path)
	// Invalid tail counts never reach the controller.
	if err != nil {
		return err
	}
	// Following appends lines, which a length-prefixed TOON document cannot do.
	if s.format == formatTOON && s.flags["follow"] != "" {
		return model.Err("ARGUMENTS", "--toon cannot be combined with --follow; use --json")
	}
	previous, first := "", true
	// Each iteration respects the existing polling and cancellation policy.
	for {
		var result logResponse
		// A failed read must not overwrite the last successful baseline.
		if err = s.request("GET", path, nil, &result); err != nil {
			return err
		}
		snapshot := s.logSnapshot(result, -1)
		output := logSnapshotDelta(previous, snapshot)
		// Tail controls the initial display; following continues to show new output.
		if first {
			output = s.logSnapshot(result, tail)
		}
		// Empty snapshots and unchanged polls produce no extra blank lines.
		if output != "" {
			s.message(output)
		}
		previous, first = snapshot, false
		// Ordinary logs returns after one bounded snapshot.
		if s.flags["follow"] == "" {
			return nil
		}
		// Ctrl-C cancels polling without changing the deployed application.
		if err = s.pause(); err != nil {
			return err
		}
	}
}
