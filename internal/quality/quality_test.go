package quality

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestComplexityDecisions verifies gocyclo-compatible path counting independently of application code.
func TestComplexityDecisions(t *testing.T) {
	cases := []struct {
		name, body string
		want       int
	}{
		{"straight", "return", 1},
		{"boolean", "if a && b || c {return}", 4},
		{"loops", "for {break}; for range xs {}", 3},
		{"switch", "switch x {case 1,2:return;case 3:return;default:return}", 3},
		{"select", "select{case <-a:return;case b<-c:return;default:return}", 3},
		{"type switch", "switch x.(type){case int:return;case string:return;default:return}", 3},
		{"closure", "f:=func(){if x{return}};_ = f", 2},
	}
	// Each fixture isolates a language construct so changes cannot silently undercount it.
	for _, test := range cases {
		// Named subtests identify the decision rule responsible for a counting regression.
		t.Run(test.name, func(t *testing.T) { checkComplexity(t, test.body, test.want) })
	}
}

// checkComplexity parses an intentionally untyped fixture and compares its syntactic path count.
func checkComplexity(t *testing.T, body string, want int) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture;func run(){"+body+"}", 0)
	// Invalid fixtures must fail rather than produce misleading expected complexity.
	if err != nil {
		t.Fatal(err)
	}
	function := file.Decls[0].(*ast.FuncDecl)
	// The expected totals are manually enumerated, independent of the implementation visitor.
	if got := Complexity(function.Body); got != want {
		t.Fatalf("complexity=%d; want %d", got, want)
	}
}

// TestScanIncludesTestsAndClosures confirms the gate cannot hide complexity in tests or callbacks.
func TestScanIncludesTestsAndClosures(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "sample_test.go", "package example\nfunc sample(){f:=func(){if x && y{return}};_ = f}\n")
	report, err := Scan(root, Options{MaxComplexity: 2})
	// Both the enclosing function and the literal exceed the strict fixture threshold.
	if err != nil {
		t.Fatal(err)
	}
	// Verify callbacks and their enclosing functions are both included.
	if report.Files != 1 || report.Functions != 2 || report.FunctionsOverLimit != 2 || report.MaxComplexity != 3 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

// TestScanCommentsAndDirectoryPolicy verifies strict source coverage without reading generated output.
func TestScanCommentsAndDirectoryPolicy(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "sample.go", "package example\n// run rejects the fixture condition.\nfunc run(){\n// Stop on the unsupported condition.\nif x{return}\n}\n")
	writeFixture(t, root, "dist/broken.go", "invalid generated output")
	writeFixture(t, root, "vendor/broken.go", "invalid dependency output")
	report, err := Scan(root, Options{Comments: true})
	// Fully documented source passes even when ignored output contains non-Go content.
	if err != nil {
		t.Fatal(err)
	}
	// Ensure ignored trees cannot inflate source counts or produce false violations.
	if report.Files != 1 || len(report.Findings) != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

// TestScanRejectsMissingComments ensures the comment rule catches both declarations and decisions.
func TestScanRejectsMissingComments(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "sample.go", "package example\nfunc run(){\nif x{return}\n}\n")
	report, err := Scan(root, Options{Comments: true})
	// The declaration and its condition require independent explanations.
	if err != nil {
		t.Fatal(err)
	}
	// Both undocumented constructs must be diagnosed independently.
	if report.MissingComments != 2 {
		t.Fatalf("missing comments=%d; want 2", report.MissingComments)
	}
}

// TestScanRejectsInvalidSyntax confirms parsing failures cannot masquerade as passing analysis.
func TestScanRejectsInvalidSyntax(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "sample.go", "package example\nfunc broken(")
	// A syntax error must abort scanning even before build or vet runs.
	if _, err := Scan(root, Options{}); err == nil {
		t.Fatal("invalid source passed")
	}
}

// writeFixture creates isolated source trees with no dependence on the working checkout.
func writeFixture(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	// Fixtures may exercise ignored subdirectories without accessing real generated files.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	// Owner-only fixture files keep the test independent of ambient umask settings.
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
