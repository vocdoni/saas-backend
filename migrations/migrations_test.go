package migrations

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// TestMigrationVersionsAreUnique parses the package's source files, finds every
// AddMigration call and fails if two of them claim the same version.
//
// The registry can't catch this at runtime: AddMigration writes into a map, so the
// init that runs last silently replaces the other migration, and the runner, which
// records applied migrations by version alone, never runs it.
func TestMigrationVersionsAreUnique(t *testing.T) {
	fset := token.NewFileSet()
	//nolint:staticcheck // SA1019: parser.ParseDir suffices here, this package has no build-tagged files
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}
	pkg, ok := pkgs["migrations"]
	if !ok {
		t.Fatalf("package 'migrations' not found")
	}

	byVersion := map[int][]string{}
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "AddMigration" || len(call.Args) < 2 {
				return true
			}
			pos := fset.Position(call.Pos()).String()
			// a non-literal version would slip past this check, so reject it outright
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				t.Errorf("%s: AddMigration version must be an integer literal", pos)
				return true
			}
			version, err := strconv.Atoi(lit.Value)
			if err != nil {
				t.Errorf("%s: parse version %q: %v", pos, lit.Value, err)
				return true
			}
			name := "?"
			if s, ok := call.Args[1].(*ast.BasicLit); ok {
				name = s.Value
			}
			byVersion[version] = append(byVersion[version], name+"@"+pos)
			return true
		})
	}

	if len(byVersion) == 0 {
		t.Fatalf("no AddMigration calls found")
	}
	var dups []string
	for version, refs := range byVersion {
		if len(refs) > 1 {
			dups = append(dups, strconv.Itoa(version)+": "+strings.Join(refs, ", "))
		}
	}
	if len(dups) > 0 {
		t.Fatalf("duplicate migration versions found:\n  %s", strings.Join(dups, "\n  "))
	}
}
