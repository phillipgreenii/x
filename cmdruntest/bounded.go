package cmdruntest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	cmdrunImportPath  = "github.com/phillipgreenii/x/cmdrun"
	unboundedMarker   = "cmdrun:unbounded"
	callTypeName      = "Call"
	timeoutFieldLabel = "Timeout"
)

// AssertCallsBounded fails t for every cmdrun.Call composite literal in the
// non-test Go files under moduleRoot that has no Timeout field and no
// annotation. A literal is annotated by a comment of the form
//
//	//cmdrun:unbounded <reason>
//
// with a non-empty reason, on the line above the literal or on the line it
// starts on. Directories named testdata, vendor or starting with "." or "_"
// are skipped below the root. Each module adopts the guard from one test:
//
//	cmdruntest.AssertCallsBounded(t, ".")
func AssertCallsBounded(t testing.TB, moduleRoot string) {
	t.Helper()
	violations, err := UnboundedCalls(moduleRoot)
	if err != nil {
		t.Fatalf("AssertCallsBounded: %v", err)
	}
	for _, v := range violations {
		t.Errorf("%s: cmdrun.Call has no Timeout; set one or annotate it with //%s <reason>", v, unboundedMarker)
	}
}

// UnboundedCalls returns "file:line" for each unbounded, unannotated
// cmdrun.Call literal under moduleRoot, sorted.
func UnboundedCalls(moduleRoot string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != moduleRoot && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		found, err := unboundedInFile(path)
		out = append(out, found...)
		return err
	})
	sort.Strings(out)
	return out, err
}

func unboundedInFile(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	local := ""
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != cmdrunImportPath {
			continue
		}
		local = "cmdrun"
		if imp.Name != nil {
			local = imp.Name.Name
		}
	}
	if local == "" {
		return nil, nil
	}
	annotated := map[int]bool{} // line of the annotation comment's end
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if hasReason(c.Text) {
				annotated[fset.Position(c.End()).Line] = true
			}
		}
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isCallType(lit.Type, local) || hasTimeout(lit) {
			return true
		}
		pos := fset.Position(lit.Pos())
		if annotated[pos.Line] || annotated[pos.Line-1] {
			return true
		}
		out = append(out, fmt.Sprintf("%s:%d", path, pos.Line))
		return true
	})
	return out, nil
}

func isCallType(e ast.Expr, local string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != callTypeName {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == local
}

func hasTimeout(lit *ast.CompositeLit) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == timeoutFieldLabel {
			return true
		}
	}
	return false
}

func hasReason(comment string) bool {
	rest, ok := strings.CutPrefix(comment, "//"+unboundedMarker)
	return ok && strings.TrimSpace(rest) != "" && (rest[0] == ' ' || rest[0] == '\t')
}
