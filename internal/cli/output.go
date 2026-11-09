package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// Output formats selectable with --json and --toon.
const (
	formatHuman = "human"
	formatJSON  = "json"
	formatTOON  = "toon"
)

// operationSummaryFields are the operation fields a person needs; --json keeps the full record.
var operationSummaryFields = []string{"id", "kind", "name", "status", "action", "error", "supersededBy", "restoreBackupId", "planId"}

// outputFormat honours --json/--toon, otherwise prints text for terminals and keeps JSON for pipes and scripts.
func outputFormat(flags map[string]string, out io.Writer) string {
	// Explicit machine formats always win.
	if flags["toon"] != "" {
		return formatTOON
	}
	// --json keeps the original machine-readable contract.
	if flags["json"] != "" {
		return formatJSON
	}
	file, ok := out.(*os.File)
	// People at a terminal get readable text by default.
	if ok && term.IsTerminal(int(file.Fd())) {
		return formatHuman
	}
	return formatJSON
}

// validateOutputFlags rejects asking for two machine formats at once.
func validateOutputFlags(flags map[string]string) error {
	// Only one explicit format can describe stdout.
	if flags["json"] != "" && flags["toon"] != "" {
		return model.Err("ARGUMENTS", "Use either --json or --toon, not both")
	}
	return nil
}

// write renders a value in the selected format to one stream.
func (s *session) write(w io.Writer, value any, complete bool) error {
	// Each format has its own encoder.
	switch s.format {
	case formatTOON: // Token-Oriented Object Notation for LLM consumers.
		text, err := encodeTOON(value)
		// Encoding errors are reported instead of partial output.
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, text)
		return err
	case formatHuman: // Aligned text for people.
		text, err := renderHuman(value, complete)
		// Encoding errors are reported instead of partial output.
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, text)
		return err
	}
	return json.NewEncoder(w).Encode(value)
}

// printReview shows a plan or approval review; human mode never hides empty fields.
func (s *session) printReview(value any) error { return s.write(s.out, value, true) }

// printOperation shows a terminal operation; human mode summarizes it with a headline.
func (s *session) printOperation(operation map[string]any) error {
	// Machine formats keep the full operation record.
	if s.format != formatHuman {
		return s.print(operation)
	}
	summary := map[string]any{}
	// Copy only the fields that explain the outcome.
	for _, key := range operationSummaryFields {
		// Absent fields stay absent.
		if value, ok := operation[key]; ok {
			summary[key] = value
		}
	}
	// A successful deployment includes its public address without repeating its specification.
	if spec, ok := operation["spec"].(map[string]any); ok && client.OperationSucceeded(outputTextStatus(operation)) {
		summary["url"] = spec["route"]
	}
	// The headline states the outcome in one line before the details.
	if _, err := fmt.Fprintln(s.out, operationHeadline(operation)); err != nil {
		return err
	}
	return s.write(s.out, summary, false)
}

// operationHeadline reads like "Deploy hello: succeeded".
func operationHeadline(operation map[string]any) string {
	kind, _ := operation["kind"].(string)
	name, _ := operation["name"].(string)
	status, _ := operation["status"].(string)
	words := strings.TrimSpace(humanLabel(kind) + " " + terminalSafe(name))
	// Operations without a kind still report their status.
	if words == "" {
		words = "Operation"
	}
	return words + ": " + terminalSafe(status)
}

// progress writes one operation stream event to stderr in the selected format.
func (s *session) progress(event client.OperationEvent) error {
	// JSON keeps the original event objects for scripts.
	if s.format == formatJSON {
		return json.NewEncoder(s.errOut).Encode(event)
	}
	// TOON encodes the same event structure compactly.
	if s.format == formatTOON {
		return s.write(s.errOut, event, false)
	}
	_, err := fmt.Fprintln(s.errOut, progressLine(event))
	return err
}

// progressLine turns a stream event into one readable line.
func progressLine(event client.OperationEvent) string {
	// Known events have a dedicated wording.
	switch event.Event {
	case "log": // Operation log entries show level and message.
		var entry struct{ Level, Message string }
		_ = json.Unmarshal(event.Data, &entry)
		return logPrefix(entry.Level) + terminalSafe(entry.Message)
	case "reset": // History rotation is explained rather than shown raw.
		return "Earlier progress is no longer available; continuing from the current status."
	}
	return terminalSafe(event.Event) + " update received (use --json for details)"
}

// logPrefix marks warnings and errors so they stand out.
func logPrefix(level string) string {
	// Only non-informational levels get a visible marker.
	switch strings.ToLower(level) {
	case "warn", "warning": // Warnings.
		return "warning: "
	case "error", "fatal": // Failures.
		return "error: "
	}
	return "  "
}

// showApprovalReview prints a hardware-approval review completely; machine formats use escaped, indented JSON.
func (s *session) showApprovalReview(review json.RawMessage) error {
	// Human text shows every field with control characters made visible.
	if s.format == formatHuman {
		return s.write(s.errOut, review, true)
	}
	encoder := json.NewEncoder(s.errOut)
	encoder.SetIndent("", "  ")
	return encoder.Encode(review)
}

// logSnapshot renders structured log entries as lines for people, TOON for agents, or the original JSON.
func (s *session) logSnapshot(result logResponse, tail int) string {
	entries, structured := result.Entries.([]any)
	// Plain-text logs and JSON output keep their existing rendering.
	if !structured || s.format == formatJSON {
		return renderLogSnapshot(result, tail)
	}
	// Tail limits the initial number of events shown.
	if tail >= 0 {
		entries = entries[max(0, len(entries)-tail):]
	}
	// TOON encodes the bounded snapshot as one document.
	if s.format == formatTOON {
		text, _ := encodeTOON(map[string]any{"entries": entries})
		return strings.TrimRight(text, "\n")
	}
	lines := make([]string, len(entries))
	// One event per line keeps follow-mode deltas line-based.
	for i, entry := range entries {
		lines[i] = humanLogLine(entry)
	}
	return strings.Join(lines, "\n")
}

// humanLogLine prints an event's time, level and message on one line.
func humanLogLine(entry any) string {
	m, ok := entry.(map[string]any)
	// Non-object events print as plain values.
	if !ok {
		return humanScalar(entry)
	}
	parts := []string{}
	// Common log fields appear in a fixed, readable order.
	for _, key := range []string{"timestamp", "time", "level", "component", "message", "msg"} {
		// Only present text fields are shown.
		if value, ok := m[key].(string); ok && value != "" {
			parts = append(parts, terminalSafe(value))
		}
	}
	// Unknown event shapes fall back to escaped compact JSON.
	if len(parts) == 0 {
		raw, _ := json.Marshal(m)
		return terminalSafe(string(raw))
	}
	return strings.Join(parts, "  ")
}

// outputTextStatus reads an operation status without formatting an absent field.
func outputTextStatus(operation map[string]any) string {
	status, _ := operation["status"].(string)
	return status
}
