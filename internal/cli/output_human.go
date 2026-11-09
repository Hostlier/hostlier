package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// humanPriority lists fields shown first because they identify a result.
var humanPriority = []string{"name", "app", "id", "kind", "status", "state", "url", "route", "image", "server", "targetServer", "message"}

// humanAcronyms keeps familiar abbreviations capitalized in labels.
var humanAcronyms = map[string]string{"id": "ID", "url": "URL", "urls": "URLs", "ip": "IP", "api": "API", "cpu": "CPU", "tls": "TLS", "https": "HTTPS", "http": "HTTP", "dns": "DNS", "ssh": "SSH", "sha": "SHA", "pvc": "PVC", "ui": "UI"}

// maxTableColumns keeps tables readable on an ordinary terminal.
const maxTableColumns = 6

// maxCellWidth truncates long table cells; full values remain available with --json.
const maxCellWidth = 48

// humanWriter renders the JSON data model as aligned text.
type humanWriter struct {
	b *strings.Builder
	// complete keeps empty values so approval reviews show every field.
	complete bool
}

// renderHuman formats a result for people; complete mode never hides empty fields.
func renderHuman(value any, complete bool) (string, error) {
	v, err := genericValue(value)
	// Encoding failures are reported rather than printing partial output.
	if err != nil {
		return "", err
	}
	var b strings.Builder
	h := humanWriter{b: &b, complete: complete}
	h.root(v)
	// When every field is empty, show the fields as "None" rather than nothing.
	if b.Len() == 0 && !complete && !emptyValue(v) {
		return renderHuman(value, true)
	}
	// A result with nothing to show still confirms the command finished.
	if b.Len() == 0 {
		return "Done.\n", nil
	}
	return b.String(), nil
}

// root chooses a layout for the top-level value.
func (h humanWriter) root(v any) {
	// Objects, lists and primitives each get their own layout.
	switch t := v.(type) {
	case map[string]any: // Objects become labelled fields.
		h.object(t, 0)
	case []any: // Lists become tables or bullet blocks.
		h.list(t, 0)
	default: // A primitive prints on its own line.
		h.text(0, humanScalar(v))
	}
}

// text writes one indented line.
func (h humanWriter) text(depth int, line string) {
	h.b.WriteString(strings.Repeat("  ", depth) + line + "\n")
}

// object prints scalar fields aligned first, then nested sections.
func (h humanWriter) object(m map[string]any, depth int) {
	keys := humanKeys(m)
	scalars, nested := []string{}, []string{}
	// Split visible fields into one-line values and sections.
	for _, key := range keys {
		// Hidden empties are dropped unless the review must be complete.
		if !h.visible(m[key]) {
			continue
		}
		// Primitive and primitive-list fields fit on one line.
		if inlineValue(m[key]) {
			scalars = append(scalars, key)
		} else {
			nested = append(nested, key)
		}
	}
	h.scalars(m, scalars, depth)
	// Each nested value gets a labelled section.
	for _, key := range nested {
		h.section(humanLabel(key), m[key], depth)
	}
}

// scalars prints aligned "Label  value" lines.
func (h humanWriter) scalars(m map[string]any, keys []string, depth int) {
	width := 0
	// Align values on the longest label.
	for _, key := range keys {
		width = max(width, utf8.RuneCountInString(humanLabel(key)))
	}
	// One line per field.
	for _, key := range keys {
		label := humanLabel(key)
		h.text(depth, label+":"+strings.Repeat(" ", width-utf8.RuneCountInString(label)+2)+inlineText(m[key]))
	}
}

// section prints a labelled nested object or list.
func (h humanWriter) section(label string, v any, depth int) {
	h.text(depth, label+":")
	// Nested objects and lists use their own layouts.
	switch t := v.(type) {
	case map[string]any: // Objects indent their fields.
		h.object(t, depth+1)
	case []any: // Lists indent their table or entries.
		h.list(t, depth+1)
	}
}

// list prints a table for uniform objects, otherwise one entry per item.
func (h humanWriter) list(items []any, depth int) {
	// Empty lists say so instead of printing nothing.
	if len(items) == 0 {
		h.text(depth, "None")
		return
	}
	// Uniform records read best as a table.
	if columns := tableColumns(items); len(columns) > 0 {
		h.table(items, columns, depth)
		return
	}
	// Other lists print each entry as a bullet block.
	for _, item := range items {
		h.entry(item, depth)
	}
}

// entry prints one bullet item; object entries indent their fields beneath it.
func (h humanWriter) entry(item any, depth int) {
	// Objects become indented field blocks, other values inline bullets.
	switch t := item.(type) {
	case map[string]any: // Objects show their identifying field after the bullet.
		h.text(depth, "- "+entryTitle(t))
		h.object(t, depth+1)
	case []any: // Nested lists indent under a bare bullet.
		h.text(depth, "-")
		h.list(t, depth+1)
	default: // Primitives follow the bullet.
		h.text(depth, "- "+humanScalar(item))
	}
}

// table prints aligned columns with upper-case headers.
func (h humanWriter) table(items []any, columns []string, depth int) {
	rows := [][]string{}
	header := make([]string, len(columns))
	// Headers use the same labels as field lists.
	for i, column := range columns {
		header[i] = strings.ToUpper(humanLabel(column))
	}
	rows = append(rows, header)
	// Each object contributes one row.
	for _, item := range items {
		rows = append(rows, tableRow(item.(map[string]any), columns))
	}
	widths := columnWidths(rows)
	// Pad every cell except the last to its column width.
	for _, row := range rows {
		h.text(depth, strings.TrimRight(padRow(row, widths), " "))
	}
}

// tableRow formats one object's cells.
func tableRow(m map[string]any, columns []string) []string {
	cells := make([]string, len(columns))
	// Missing fields show as a dash.
	for i, column := range columns {
		cells[i] = truncate(inlineText(m[column]), maxCellWidth)
	}
	return cells
}

// columnWidths finds each column's widest cell.
func columnWidths(rows [][]string) []int {
	widths := make([]int, len(rows[0]))
	// Every row can widen a column.
	for _, row := range rows {
		// Measure runes so non-ASCII names still align.
		for i, cell := range row {
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	return widths
}

// padRow joins cells with two-space gutters.
func padRow(row []string, widths []int) string {
	var b strings.Builder
	// Pad each cell to its column width.
	for i, cell := range row {
		b.WriteString(cell + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+2))
	}
	return b.String()
}

// tableColumns picks primitive columns shared by uniform objects, or none when a table would mislead.
func tableColumns(items []any) []string {
	counts := map[string]int{}
	// Count primitive fields; any non-object item rules out a table.
	for _, item := range items {
		m, ok := item.(map[string]any)
		// Mixed lists are printed as entries.
		if !ok {
			return nil
		}
		countPrimitiveFields(m, counts)
	}
	columns := []string{}
	// Keep fields present on most rows so sparse details do not widen the table.
	for _, key := range humanKeysFrom(counts) {
		// A column must appear on at least half the rows.
		if counts[key]*2 >= len(items) && len(columns) < maxTableColumns {
			columns = append(columns, key)
		}
	}
	return columns
}

// countPrimitiveFields tallies non-empty primitive fields of one row.
func countPrimitiveFields(m map[string]any, counts map[string]int) {
	// Only one-line values can sit in a table cell.
	for key, value := range m {
		// Nested and empty values are left out of tables.
		if isPrimitive(value) && value != nil && value != "" {
			counts[key]++
		}
	}
}

// entryTitle names a bullet entry by its most identifying field.
func entryTitle(m map[string]any) string {
	// The first available priority field is the title.
	for _, key := range humanPriority {
		// Only plain values make a readable title.
		if value, ok := m[key]; ok && isPrimitive(value) && value != nil && value != "" {
			return inlineText(value)
		}
	}
	return "Item"
}

// visible reports whether a field should be printed.
func (h humanWriter) visible(v any) bool {
	// Complete reviews show every field, including empty ones.
	if h.complete {
		return true
	}
	return !emptyValue(v)
}

// emptyValue reports values that carry no information for a reader.
func emptyValue(v any) bool {
	// Each container type has its own notion of empty.
	switch t := v.(type) {
	case nil: // Absent values.
		return true
	case string: // Empty text.
		return t == ""
	case []any: // Empty lists.
		return len(t) == 0
	case map[string]any: // Empty objects.
		return len(t) == 0
	}
	return false
}

// inlineValue reports values that fit on a label line.
func inlineValue(v any) bool {
	// Primitive lists join onto one line; other containers need sections.
	if items, ok := v.([]any); ok {
		return allPrimitive(items)
	}
	// Empty objects in complete reviews print as "None" on one line.
	if m, ok := v.(map[string]any); ok {
		return len(m) == 0
	}
	return true
}

// inlineText formats a primitive or primitive list for one line.
func inlineText(v any) string {
	// Lists join with commas; empty lists read as None.
	if items, ok := v.([]any); ok {
		// Empty lists say so explicitly.
		if len(items) == 0 {
			return "None"
		}
		values := make([]string, len(items))
		// Each element uses scalar formatting.
		for i, item := range items {
			values[i] = humanScalar(item)
		}
		return strings.Join(values, ", ")
	}
	// Empty objects only reach here in complete reviews.
	if _, ok := v.(map[string]any); ok {
		return "None"
	}
	return humanScalar(v)
}

// humanScalar formats one primitive safely for a terminal.
func humanScalar(v any) string {
	// Each primitive kind has a reader-friendly spelling.
	switch t := v.(type) {
	case nil: // Absent values.
		return "-"
	case bool: // Yes/no reads better than true/false.
		return map[bool]string{true: "yes", false: "no"}[t]
	case json.Number: // Numbers print as received.
		return t.String()
	case string: // Text is escaped so it cannot control the terminal.
		return terminalSafe(t)
	}
	return terminalSafe(fmt.Sprint(v))
}

// terminalSafe escapes control and bidirectional-override characters so values cannot spoof the display.
func terminalSafe(s string) string {
	var b strings.Builder
	// Inspect each rune; ordinary text passes through unchanged.
	for _, r := range s {
		// Control and bidi formatting runes are shown as visible escapes.
		if unsafeRune(r) {
			b.WriteString(escapeRune(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unsafeRune reports runes that could move the cursor, recolor output or reorder text.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f
}

// escapeRune renders one unsafe rune visibly.
func escapeRune(r rune) string {
	// Common whitespace controls keep their familiar escapes.
	switch r {
	case '\n': // Newline.
		return `\n`
	case '\r': // Carriage return.
		return `\r`
	case '\t': // Tab.
		return `\t`
	}
	return fmt.Sprintf(`\u%04x`, r)
}

// truncate shortens long cells with an ellipsis.
func truncate(s string, limit int) string {
	// Short values print in full.
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit-1]) + "…"
}

// humanKeys orders an object's keys: identifying fields first, then alphabetical.
func humanKeys(m map[string]any) []string {
	counts := map[string]int{}
	// Reuse the shared ordering over the object's keys.
	for key := range m {
		counts[key] = 1
	}
	return humanKeysFrom(counts)
}

// humanKeysFrom orders keys by priority, then alphabetically.
func humanKeysFrom(set map[string]int) []string {
	rank := map[string]int{}
	// Earlier priority fields sort first.
	for i, key := range humanPriority {
		rank[key] = i - len(humanPriority)
	}
	keys := make([]string, 0, len(set))
	// Collect keys before sorting.
	for key := range set {
		keys = append(keys, key)
	}
	// Sort by priority rank, then alphabetically.
	sort.Slice(keys, func(i, j int) bool {
		// Priority rank wins; ties fall back to alphabetical order.
		if rank[keys[i]] != rank[keys[j]] {
			return rank[keys[i]] < rank[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// humanLabel turns camelCase, kebab-case or snake_case keys into sentence-case labels.
func humanLabel(key string) string {
	words := splitWords(terminalSafe(key))
	// Keep acronyms upper case and lower-case the rest.
	for i, word := range words {
		lower := strings.ToLower(word)
		// Known abbreviations keep their usual spelling.
		if acronym, ok := humanAcronyms[lower]; ok {
			words[i] = acronym
			continue
		}
		words[i] = lower
	}
	// Capitalize the first word of the label.
	if len(words) > 0 && words[0] == strings.ToLower(words[0]) {
		first := []rune(words[0])
		first[0] = unicode.ToUpper(first[0])
		words[0] = string(first)
	}
	return strings.Join(words, " ")
}

// splitWords breaks a key at separators and lower-to-upper case changes.
func splitWords(key string) []string {
	words := []string{}
	current := []rune{}
	var previous rune
	// Walk runes, cutting at separators and camelCase boundaries.
	for _, r := range key {
		boundary := wordSeparator(r)
		// A new word starts after a separator or at a lower-to-upper change.
		if (boundary || camelBoundary(previous, r)) && len(current) > 0 {
			words, current = append(words, string(current)), []rune{}
		}
		// Separators themselves are dropped.
		if !boundary {
			current = append(current, r)
		}
		previous = r
	}
	// Keep the final word.
	if len(current) > 0 {
		words = append(words, string(current))
	}
	return words
}

// wordSeparator reports characters that separate words in keys.
func wordSeparator(r rune) bool { return r == '-' || r == '_' || r == ' ' || r == '.' }

// camelBoundary reports a lower-to-upper case change, as in imageDigest.
func camelBoundary(previous, r rune) bool { return unicode.IsUpper(r) && unicode.IsLower(previous) }
