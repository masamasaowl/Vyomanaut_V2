// Package payment is declared in doc.go.
// TestNoFloatArithmetic (CI check 6, MVP §8.4) is distinct from and
// complementary to scripts/ci/grep_checks.sh's NO_FLOAT_PAYMENT scan (CI
// check 9, added Session 0.2.2) — the grep check is a fast textual pattern
// match across all file types including SQL; this test is a
// semantically-aware go/ast parse of Go source specifically. Neither
// replaces the other (build.md Milestone 10 review note).
//
// This file is excluded from grep_checks.sh's NO_FLOAT_PAYMENT scan (same
// treatment as doc.go, which is excluded for the identical reason) because
// the forbidden identifiers must appear here as literal comparison targets
// — as quoted string map keys below, never as an actual Go type — for this
// test to do its job at all.
//
// [REF: IC §11, MVP §8.4 CI check 6, build.md Phase 10.4 Session 10.4.2]

package payment

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// forbiddenNumericTypes are the exact identifiers DM §3 Invariant 4 and
// IC §11 ban from appearing anywhere in this package's Go source — as a
// declared type, a type conversion, or any other identifier reference. This
// CI gate, and this test itself, may not be disabled or weakened by any
// future PR (IC §11).
var forbiddenNumericTypes = map[string]bool{
	"float64": true,
	"float32": true,
	"FLOAT":   true,
	"DECIMAL": true,
	"NUMERIC": true,
}

// TestNoFloatArithmetic parses every .go file in this package with go/ast
// and fails if any forbidden identifier (see forbiddenNumericTypes) appears
// anywhere in its syntax tree as an *ast.Ident — a quoted string literal
// (such as this file's own map keys above) is a distinct AST node type
// (*ast.BasicLit) and is never flagged, which is exactly why this test can
// reference the forbidden names as data without becoming a violation of its
// own rule.
func TestNoFloatArithmetic(t *testing.T) {
	const dir = "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	fset := token.NewFileSet()
	violations := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if forbiddenNumericTypes[ident.Name] {
				t.Errorf("%s: forbidden numeric type identifier %q found — "+
					"no non-integer numeric type may appear anywhere in internal/payment/ (DM §3 Invariant 4, IC §11)",
					fset.Position(ident.Pos()), ident.Name)
				violations++
			}
			return true
		})
	}
	if violations == 0 {
		t.Logf("scanned %d .go files in internal/payment/, found zero forbidden numeric type identifiers", len(entries))
	}
}
