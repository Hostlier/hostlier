// Package quality checks Hostlier's function complexity and documentation contract.
package quality

import "go/token"

// Finding identifies one actionable source-level contract violation.
type Finding struct {
	Position token.Position
	Kind     string
	Message  string
}

// Report records totals alongside violations so CI can enforce the same review rules.
type Report struct {
	Files              int
	Functions          int
	MaxComplexity      int
	FunctionsOverLimit int
	MissingComments    int
	Findings           []Finding
}

// Options configures the complexity threshold and whether comment coverage is enforced.
type Options struct {
	MaxComplexity int
	Comments      bool
}
