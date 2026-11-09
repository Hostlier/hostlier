package quality

import (
	"go/ast"
	"go/token"
)

// commentLines indexes all documented source lines, including multiline explanations.
func commentLines(file *ast.File, positions *token.FileSet) map[int]bool {
	lines := map[int]bool{}
	// Each comment group may explain several consecutive logic statements.
	for _, group := range file.Comments {
		// Count each physical comment line so inline and preceding comments have identical semantics.
		for line := positions.Position(group.Pos()).Line; line <= positions.Position(group.End()).Line; line++ {
			lines[line] = true
		}
	}
	return lines
}

// requiresComment defines executable logic blocks and owned type/function declarations.
func requiresComment(node ast.Node) bool {
	// Ordinary assignments need explanation when meaningful, but are not standalone branch decisions.
	switch node.(type) {
	// Require local explanations for declarations and each explicit branch construct.
	case *ast.FuncDecl, *ast.FuncLit, *ast.TypeSpec, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause:
		return true
	}
	return false
}

// checkComment requires an explanation immediately before or on a logical statement's source line.
func checkComment(node ast.Node, positions *token.FileSet, comments map[int]bool, report *Report) {
	position := positions.Position(node.Pos())
	// Inline comments support else-if and compact switch case layouts without counting distant comments.
	if comments[position.Line] || comments[position.Line-1] {
		return
	}
	report.MissingComments++
	report.Findings = append(report.Findings, Finding{Position: position, Kind: "comment", Message: "missing function, type or control-flow explanation"})
}
