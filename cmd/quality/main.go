// Command quality enforces Hostlier's complexity and comment coverage contract.
package main

import (
	"flag"
	"fmt"
	"os"

	"hostlier/internal/quality"
)

// main reports all violations and exits unsuccessfully when any contract is violated.
func main() {
	max := flag.Int("max", 10, "maximum cyclomatic complexity per function")
	comments := flag.Bool("comments", true, "check function, type and control-flow comments")
	flag.Parse()
	root := "."
	// An explicit root permits checking a single package during refactoring.
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	report, err := quality.Scan(root, quality.Options{MaxComplexity: *max, Comments: *comments})
	// Filesystem or parser failures invalidate the report.
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Print actionable source positions before the summary to support editor/CI navigation.
	for _, finding := range report.Findings {
		fmt.Fprintf(os.Stderr, "%s: %s: %s\n", finding.Position, finding.Kind, finding.Message)
	}
	fmt.Printf("Checked %d Go files and %d functions: maximum complexity %d; %d above %d; %d missing comments.\n", report.Files, report.Functions, report.MaxComplexity, report.FunctionsOverLimit, *max, report.MissingComments)
	// Any finding is a failing quality gate, including undocumented branch logic.
	if len(report.Findings) > 0 {
		os.Exit(1)
	}
}
