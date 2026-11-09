package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// Scan checks all Go files below root, including tests and the quality checker itself.
func Scan(root string, options Options) (Report, error) {
	report := Report{}
	// The user-requested ceiling is ten unless a stricter caller policy is supplied.
	if options.MaxComplexity == 0 {
		options.MaxComplexity = 10
	}
	// Delegate filesystem traversal without making generated files look like application code.
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		return visitPath(path, root, entry, err, options, &report)
	})
	return report, err
}

// visitPath excludes generated output and dependency trees while preserving source/test coverage.
func visitPath(path, root string, entry fs.DirEntry, err error, options Options, report *Report) error {
	// Filesystem errors must fail the gate rather than silently reduce its coverage.
	if err != nil {
		return err
	}
	// Dependencies and hidden metadata are outside the application source contract.
	if entry.IsDir() {
		return directoryPolicy(path, root, entry.Name())
	}
	// Only Go source files contain the syntax this checker analyzes.
	if !strings.HasSuffix(path, ".go") {
		return nil
	}
	return scanFile(path, options, report)
}

// directoryPolicy keeps builds, vendor trees and hidden workspace metadata out of source analysis.
func directoryPolicy(path, root, name string) error {
	// The explicitly requested root must still be traversed when its own name is hidden.
	if path == root {
		return nil
	}
	// These directories contain generated binaries, dependency code or repository metadata.
	if name == "dist" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
		return filepath.SkipDir
	}
	return nil
}

// scanFile parses comments and syntax together so reported locations match the checked source.
func scanFile(path string, options Options, report *Report) error {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, path, nil, parser.ParseComments)
	// Invalid Go syntax is a gate failure, never a successful zero-function scan.
	if err != nil {
		return err
	}
	report.Files++
	comments := commentLines(file, positions)
	// Each declaration, literal and control-flow branch is checked at its own source location.
	ast.Inspect(file, func(node ast.Node) bool {
		inspectNode(node, positions, comments, options, report)
		return true
	})
	return nil
}

// inspectNode applies independent complexity and documentation checks without altering the AST.
func inspectNode(node ast.Node, positions *token.FileSet, comments map[int]bool, options Options, report *Report) {
	name, body, isFunction := functionNode(node, positions)
	// Literals are checked independently as well as contributing to their enclosing function.
	if isFunction {
		checkFunction(name, body, node.Pos(), positions, options, report)
	}
	// Every function/type and logical control-flow statement needs a nearby purpose comment.
	if options.Comments && requiresComment(node) {
		checkComment(node, positions, comments, report)
	}
}

// functionNode identifies named functions and closures while excluding nonexecutable type syntax.
func functionNode(node ast.Node, positions *token.FileSet) (string, ast.Node, bool) {
	// Bodies, rather than signature expressions, define executable complexity.
	switch value := node.(type) {
	// Named declarations expose stable function names in diagnostics.
	case *ast.FuncDecl:
		return value.Name.Name, value.Body, true
	// Anonymous functions receive source locations so callbacks cannot evade the ceiling.
	case *ast.FuncLit:
		return fmt.Sprintf("function literal at line %d", positions.Position(value.Pos()).Line), value.Body, true
	}
	return "", nil, false
}

// checkFunction tracks the measured maximum and reports each function above the configured ceiling.
func checkFunction(name string, body ast.Node, pos token.Pos, positions *token.FileSet, options Options, report *Report) {
	complexity := Complexity(body)
	report.Functions++
	// Keep a whole-tree maximum even when every function passes.
	if complexity > report.MaxComplexity {
		report.MaxComplexity = complexity
	}
	// The ceiling applies equally to production functions, tests and closures.
	if complexity > options.MaxComplexity {
		report.FunctionsOverLimit++
		report.Findings = append(report.Findings, Finding{Position: positions.Position(pos), Kind: "complexity", Message: fmt.Sprintf("%s has complexity %d (maximum %d)", name, complexity, options.MaxComplexity)})
	}
}
