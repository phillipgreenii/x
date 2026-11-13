package exporttest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const helperImport = "github.com/phillipgreenii/x/otelsetup/exporttest"

// forbidden are the imports no production file may carry, so apps never pay
// for the oracle: the HTTP test server and this helper package.
var forbidden = []string{"net/http/httptest", helperImport}

// productionImports returns "file: import" for every forbidden import in the
// non-test Go files under root, skipping the helper package's own directory.
// It reads imports only (go/parser ImportsOnly): no build, no process spawn.
func productionImports(t *testing.T, root, skipDir string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == skipDir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			for _, bad := range forbidden {
				if p == bad {
					hits = append(hits, path+": "+p)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func TestProductionPackagesDoNotImportTheOracle(t *testing.T) {
	root, err := filepath.Abs("..") // the otelsetup package tree: otelsetup, spool, internal/...
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "otelsetup.go")); err != nil {
		t.Fatalf("not the otelsetup directory: %v", err)
	}
	for _, h := range productionImports(t, root, filepath.Join(root, "exporttest")) {
		t.Errorf("production file imports the test oracle: %s", h)
	}
	if got := productionImports(t, root, ""); len(got) == 0 {
		t.Error("scan without the skip found nothing: it should see this package's own httptest import")
	}
}

func TestIsolationScanFlagsAProductionImport(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("clean.go", "package p\nimport \"net/http\"\nvar _ = http.StatusOK\n")
	write("x_test.go", "package p\nimport _ \"net/http/httptest\"\n")
	if hits := productionImports(t, dir, ""); len(hits) != 0 {
		t.Fatalf("clean directory flagged: %v", hits)
	}
	write("bad.go", "package p\nimport _ \"net/http/httptest\"\n")
	write("bad2.go", "package p\nimport _ \""+helperImport+"\"\n")
	if hits := productionImports(t, dir, ""); len(hits) != 2 {
		t.Fatalf("want both bad files flagged, got %v", hits)
	}
}
