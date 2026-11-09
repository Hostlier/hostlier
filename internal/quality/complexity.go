package quality

import (
	"go/ast"
	"go/token"
)

// Complexity counts independent paths using gocyclo's rules, including nested function literals.
func Complexity(node ast.Node) int {
	total := 1
	// Every decision contributes another path; compound boolean conditions contribute individually.
	ast.Inspect(node, func(child ast.Node) bool {
		total += decisionCost(child)
		return true
	})
	return total
}

// decisionCost assigns the standard cyclomatic contribution of one syntax node.
func decisionCost(node ast.Node) int {
	// Default switch/select branches add no independent decision.
	switch value := node.(type) {
	// Conditions and loops each introduce one independent path.
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
		return 1
	// Switch alternatives contribute once regardless of the number of labels.
	case *ast.CaseClause:
		return caseCost(value)
	// Select alternatives contribute only when they perform communication.
	case *ast.CommClause:
		return communicationCost(value)
	// Only short-circuit boolean operators split expression evaluation.
	case *ast.BinaryExpr:
		return booleanCost(value)
	}
	return 0
}

// caseCost counts a nondefault case once, even when it lists several matching values.
func caseCost(node *ast.CaseClause) int {
	// A nil expression list denotes the default branch.
	if node.List != nil {
		return 1
	}
	return 0
}

// communicationCost counts a selectable communication alternative.
func communicationCost(node *ast.CommClause) int {
	// A nil communication statement denotes the select default branch.
	if node.Comm != nil {
		return 1
	}
	return 0
}

// booleanCost counts short-circuit operators that create another execution path.
func booleanCost(node *ast.BinaryExpr) int {
	// Arithmetic and comparison operators do not independently branch.
	if node.Op == token.LAND || node.Op == token.LOR {
		return 1
	}
	return 0
}
