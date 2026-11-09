package cli

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// toonNumeric matches strings a TOON decoder would read as numbers, so they must be quoted.
var toonNumeric = regexp.MustCompile(`^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$|^0\d+$`)

// toonBareKey matches keys that TOON permits without quotes.
var toonBareKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// toonEscaper applies TOON's only valid string escapes.
var toonEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

// genericValue converts any result to the JSON data model with exact numbers.
func genericValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	// Unencodable values cannot be represented in any output format.
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	return out, decoder.Decode(&out)
}

// toonWriter accumulates indented TOON lines (two spaces per level, comma delimiter).
type toonWriter struct{ lines []string }

// encodeTOON renders a value as Token-Oriented Object Notation.
func encodeTOON(value any) (string, error) {
	v, err := genericValue(value)
	// Encoding failures are reported rather than producing partial output.
	if err != nil {
		return "", err
	}
	w := &toonWriter{}
	// Root objects, arrays and primitives each have their own TOON form.
	switch t := v.(type) {
	case map[string]any: // Root objects list their fields at depth zero.
		w.object(t, 0)
	case []any: // Root arrays use a header without a key.
		w.array("", "", t, 0, 1)
	default: // A root primitive is a single line.
		w.line(0, toonPrimitive(v))
	}
	// An empty root object is an empty document.
	if len(w.lines) == 0 {
		return "", nil
	}
	return strings.Join(w.lines, "\n") + "\n", nil
}

// line appends one indented line.
func (w *toonWriter) line(depth int, text string) {
	w.lines = append(w.lines, strings.Repeat("  ", depth)+text)
}

// object writes each field in deterministic key order.
func (w *toonWriter) object(m map[string]any, depth int) {
	// Sorted keys make repeated output byte-identical.
	for _, key := range sortedKeys(m) {
		w.field("", key, m[key], depth, depth+1)
	}
}

// field writes one key; nested content starts at the child depth.
func (w *toonWriter) field(prefix, key string, v any, depth, child int) {
	name := toonKey(key)
	// Nested objects, arrays and primitives use different TOON forms.
	switch t := v.(type) {
	case map[string]any: // Nested objects open a block.
		w.line(depth, prefix+name+":")
		w.object(t, child)
	case []any: // Arrays carry an explicit length header.
		w.array(prefix, name, t, depth, child)
	default: // Primitives stay on the key line.
		w.line(depth, prefix+name+": "+toonPrimitive(v))
	}
}

// array writes inline, tabular or list arrays as the spec requires.
func (w *toonWriter) array(prefix, name string, items []any, depth, child int) {
	header := prefix + name + "[" + strconv.Itoa(len(items)) + "]"
	// Empty and all-primitive arrays fit on the header line.
	if len(items) == 0 || allPrimitive(items) {
		w.line(depth, strings.TrimRight(header+": "+joinPrimitives(items), " "))
		return
	}
	// Uniform primitive-only objects use the compact tabular form.
	if fields, ok := tabularFields(items); ok {
		w.line(depth, header+"{"+joinKeys(fields)+"}:")
		// Each object becomes one delimited row in field order.
		for _, item := range items {
			w.line(child, tabularRow(item.(map[string]any), fields))
		}
		return
	}
	w.line(depth, header+":")
	// Mixed or nested items are written as hyphen list entries.
	for _, item := range items {
		w.listItem(item, child)
	}
}

// listItem writes one hyphen entry of a list array.
func (w *toonWriter) listItem(v any, depth int) {
	// Each item kind has its own list-entry form.
	switch t := v.(type) {
	case map[string]any: // Objects put their first field on the hyphen line.
		w.objectItem(t, depth)
	case []any: // Nested arrays keep their own header after the hyphen.
		w.array("- ", "", t, depth, depth+1)
	default: // Primitives follow the hyphen directly.
		w.line(depth, "- "+toonPrimitive(v))
	}
}

// objectItem writes an object list entry with sibling fields aligned under the first.
func (w *toonWriter) objectItem(m map[string]any, depth int) {
	keys := sortedKeys(m)
	// An empty object entry is a bare hyphen.
	if len(keys) == 0 {
		w.line(depth, "-")
		return
	}
	w.field("- ", keys[0], m[keys[0]], depth, depth+2)
	// Remaining fields sit one level deeper than the hyphen.
	for _, key := range keys[1:] {
		w.field("", key, m[key], depth+1, depth+2)
	}
}

// tabularFields reports the shared field list when every item is a primitive-only object with identical keys.
func tabularFields(items []any) ([]string, bool) {
	first, ok := items[0].(map[string]any)
	// Tabular form needs at least one field to name columns.
	if !ok || len(first) == 0 {
		return nil, false
	}
	fields := sortedKeys(first)
	// Every row must match the first row's shape exactly.
	for _, item := range items {
		// Any mismatch falls back to list form.
		if !sameShape(item, fields) {
			return nil, false
		}
	}
	return fields, true
}

// sameShape checks an item is an object with exactly these primitive fields.
func sameShape(item any, fields []string) bool {
	m, ok := item.(map[string]any)
	// Non-objects and differently sized objects cannot share a header.
	if !ok || len(m) != len(fields) {
		return false
	}
	// Each named field must exist and hold a primitive.
	for _, field := range fields {
		value, present := m[field]
		// Missing or nested values break the tabular contract.
		if !present || !isPrimitive(value) {
			return false
		}
	}
	return true
}

// tabularRow joins one object's values in header order.
func tabularRow(m map[string]any, fields []string) string {
	values := make([]string, len(fields))
	// Values keep the header's column order.
	for i, field := range fields {
		values[i] = toonPrimitive(m[field])
	}
	return strings.Join(values, ",")
}

// joinPrimitives renders an inline primitive array body.
func joinPrimitives(items []any) string {
	values := make([]string, len(items))
	// Every value is quoted as needed for the comma delimiter.
	for i, item := range items {
		values[i] = toonPrimitive(item)
	}
	return strings.Join(values, ",")
}

// joinKeys renders a tabular header's field names.
func joinKeys(fields []string) string {
	keys := make([]string, len(fields))
	// Header names follow the same quoting rule as object keys.
	for i, field := range fields {
		keys[i] = toonKey(field)
	}
	return strings.Join(keys, ",")
}

// allPrimitive reports whether an array holds only primitives.
func allPrimitive(items []any) bool {
	// Any nested object or array requires a block form.
	for _, item := range items {
		// Stop at the first non-primitive.
		if !isPrimitive(item) {
			return false
		}
	}
	return true
}

// isPrimitive reports whether a JSON value is a string, number, boolean or null.
func isPrimitive(v any) bool {
	// Objects and arrays are the only structured JSON values.
	switch v.(type) {
	case map[string]any, []any: // Structured values need their own lines.
		return false
	}
	return true
}

// toonPrimitive renders a primitive with TOON quoting and canonical numbers.
func toonPrimitive(v any) string {
	// Each JSON primitive has one canonical spelling.
	switch t := v.(type) {
	case nil: // JSON null.
		return "null"
	case bool: // Booleans are bare literals.
		return strconv.FormatBool(t)
	case json.Number: // Numbers use canonical decimal form.
		return toonNumber(t)
	case string: // Strings are quoted only when ambiguous.
		return toonString(t)
	}
	return "null"
}

// toonNumber prints a number without exponents, trailing zeros or negative zero.
func toonNumber(n json.Number) string {
	text := n.String()
	// Negative zero normalizes to zero.
	if text == "-0" {
		return "0"
	}
	// Plain integers are already canonical and kept exact.
	if !strings.ContainsAny(text, ".eE") {
		return text
	}
	f, err := strconv.ParseFloat(text, 64)
	// Non-finite values have no TOON number form.
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "null"
	}
	// Negative zero normalizes to zero.
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// toonString quotes strings a decoder could misread.
func toonString(s string) string {
	// Unambiguous strings stay bare to save tokens.
	if toonSafe(s) {
		return s
	}
	return `"` + toonEscaper.Replace(s) + `"`
}

// toonSafe applies the spec's quoting rules for the comma delimiter.
func toonSafe(s string) bool {
	// Empty, padded, literal-looking and numeric-looking strings need quotes.
	if s == "" || strings.TrimSpace(s) != s || s == "true" || s == "false" || s == "null" || toonNumeric.MatchString(s) || strings.HasPrefix(s, "-") {
		return false
	}
	// Structural characters and control characters need quotes.
	return !strings.ContainsAny(s, ":\"\\[]{},") && !hasControl(s)
}

// toonKey quotes keys that are not plain identifiers.
func toonKey(key string) string {
	// Identifier-like keys are written bare.
	if toonBareKey.MatchString(key) {
		return key
	}
	return `"` + toonEscaper.Replace(key) + `"`
}

// hasControl reports whether text contains any control character.
func hasControl(s string) bool {
	// Any C0, DEL or C1 control character makes a string unsafe to print bare.
	for _, r := range s {
		// Control runes are checked individually.
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

// sortedKeys returns an object's keys in byte order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	// Collect keys before sorting.
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
