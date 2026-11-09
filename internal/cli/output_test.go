package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// TestTOONEncoding checks spec forms: objects, inline, tabular and list arrays, quoting and canonical numbers.
func TestTOONEncoding(t *testing.T) {
	cases := map[string]string{
		`{"name":"Ada","id":1,"active":true}`:                     "active: true\nid: 1\nname: Ada\n",
		`{"users":[{"id":1,"name":"Ada"},{"id":2,"name":"Bob"}]}`: "users[2]{id,name}:\n  1,Ada\n  2,Bob\n",
		`{"tags":["a","b"],"none":[]}`:                            "none[0]:\ntags[2]: a,b\n",
		`{"a":{"b":1}}`:                                           "a:\n  b: 1\n",
		`{"items":[{"id":1,"tags":["x"]},{"id":2}]}`:              "items[2]:\n  - id: 1\n    tags[1]: x\n  - id: 2\n",
		`{"items":[{"a":{"b":1},"c":2},{"x":1}]}`:                 "items[2]:\n  - a:\n      b: 1\n    c: 2\n  - x: 1\n",
		`[1,2]`: "[2]: 1,2\n",
		`{"f":1.50,"g":1e3,"h":-0,"i":12345678901234567890}`:        "f: 1.5\ng: 1000\nh: 0\ni: 12345678901234567890\n",
		`{"s":"a,b","n":"42","e":"","t":"true","d":"-x","c":"a:b"}`: "c: \"a:b\"\nd: \"-x\"\ne: \"\"\nn: \"42\"\ns: \"a,b\"\nt: \"true\"\n",
		`{"q":"say \"hi\"\n","my key":"plain text","z":null}`:       "\"my key\": plain text\nq: \"say \\\"hi\\\"\\n\"\nz: null\n",
		`{"m":[[1,2],{"k":"v"},"s"]}`:                               "m[3]:\n  - [2]: 1,2\n  - k: v\n  - s\n",
	}
	// Each input must encode to exactly the expected document.
	for input, want := range cases {
		got, err := encodeTOON(json.RawMessage(input))
		// Encoding valid JSON never fails.
		if err != nil || got != want {
			t.Errorf("encodeTOON(%s)\n got: %q\nwant: %q (%v)", input, got, want, err)
		}
	}
}

// TestHumanRendering checks tables, aligned fields, hidden empties and terminal escaping.
func TestHumanRendering(t *testing.T) {
	list, _ := renderHuman(json.RawMessage(`[{"name":"web","status":"running","url":"https://web.example.com"},{"name":"api","status":"stopped"}]`), false)
	want := "NAME  STATUS   URL\nweb   running  https://web.example.com\napi   stopped  -\n"
	// Uniform records print as an aligned table with identifying columns first.
	if list != want {
		t.Fatalf("table\n got: %q\nwant: %q", list, want)
	}
	object, _ := renderHuman(json.RawMessage(`{"imageDigest":"sha256:abc","name":"web","secrets":[],"replicas":2,"limits":{"cpu":"0.5"}}`), false)
	want = "Name:          web\nImage digest:  sha256:abc\nReplicas:      2\nLimits:\n  CPU:  0.5\n"
	// Fields are labelled and aligned; empty lists are hidden.
	if object != want {
		t.Fatalf("object\n got: %q\nwant: %q", object, want)
	}
	review, _ := renderHuman(json.RawMessage(`{"name":"evil\u001b[2Jname\u202e","secrets":[]}`), true)
	// Complete reviews keep empty fields, and control or bidi characters are made visible.
	if !strings.Contains(review, `evil\u001b[2Jname\u202e`) || !strings.Contains(review, "Secrets:  None") || strings.ContainsRune(review, 0x1b) {
		t.Fatalf("unsafe or incomplete review: %q", review)
	}
	empty, _ := renderHuman(json.RawMessage(`{}`), false)
	// Empty results still confirm completion.
	if empty != "Done.\n" {
		t.Fatalf("empty result: %q", empty)
	}
}

// TestOperationAndProgressText checks the human operation summary and progress lines.
func TestOperationAndProgressText(t *testing.T) {
	var out bytes.Buffer
	s := &session{out: &out, format: formatHuman}
	operation := map[string]any{"id": "op-1", "kind": "deploy", "name": "hello", "status": "succeeded", "spec": map[string]any{"image": "nginx", "route": "https://hello.example.com"}, "fingerprint": "abc"}
	// Printing a terminal operation must succeed.
	if err := s.printOperation(operation); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	// The headline leads and the bulky spec stays out of the human summary.
	if !strings.HasPrefix(text, "Deploy hello: succeeded\n") || strings.Contains(text, "nginx") || strings.Contains(text, "abc") || !strings.Contains(text, "https://hello.example.com") {
		t.Fatalf("operation summary: %q", text)
	}
	lines := map[string]client.OperationEvent{
		"warning: disk low": {Event: "log", Data: json.RawMessage(`{"level":"warn","message":"disk low"}`)},
		"  pulling\\n":      {Event: "log", Data: json.RawMessage(`{"level":"info","message":"pulling\n"}`)},
	}
	// Each event becomes one escaped line.
	for want, event := range lines {
		// Progress text must match exactly.
		if got := progressLine(event); got != want {
			t.Errorf("progressLine = %q, want %q", got, want)
		}
	}
}

// TestOutputFormatSelection keeps JSON for pipes, honours explicit flags and rejects conflicts.
func TestOutputFormatSelection(t *testing.T) {
	var buffer bytes.Buffer
	// Captured output keeps the original JSON contract.
	if outputFormat(map[string]string{}, &buffer) != formatJSON {
		t.Fatal("piped output is no longer JSON")
	}
	// Explicit flags select their format regardless of the stream.
	if outputFormat(map[string]string{"toon": "true"}, &buffer) != formatTOON || outputFormat(map[string]string{"json": "true"}, &buffer) != formatJSON {
		t.Fatal("explicit format ignored")
	}
	err := RunWithOptions(context.Background(), []string{"status", "--json", "--toon"}, strings.NewReader(""), &buffer, &buffer, Options{Home: t.TempDir()})
	problem, ok := err.(*model.Error)
	// Conflicting flags fail before any connection attempt.
	if !ok || problem.Code != "ARGUMENTS" {
		t.Fatal("conflicting formats accepted", err)
	}
}
