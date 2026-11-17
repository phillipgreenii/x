// Seeded from the ccpool trust tests in nix-agent-support
// (packages/ccpool/internal/trust).

package claudetrust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRoot(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func readProjects(t *testing.T, path string) map[string]any {
	t.Helper()
	p, _ := readRoot(t, path)["projects"].(map[string]any)
	return p
}

func trusted(t *testing.T, path, cwd string) bool {
	t.Helper()
	ok, err := IsTrusted(path, cwd)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestEnsureAddsFlagPreservesOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{
		"someTopLevelKey": "keepme",
		"projects": map[string]any{
			"/example/other": map[string]any{"hasTrustDialogAccepted": true, "lastCost": 1.5},
		},
	})
	if err := Ensure(path, "/example/proj"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if readRoot(t, path)["someTopLevelKey"] != "keepme" {
		t.Error("top-level key not preserved")
	}
	projs := readProjects(t, path)
	if other, _ := projs["/example/other"].(map[string]any); other["lastCost"] != 1.5 {
		t.Error("unrelated project's keys not preserved")
	}
	if !trusted(t, path, "/example/proj") {
		t.Errorf("/example/proj not trusted: %v", projs["/example/proj"])
	}
}

func TestEnsureKeepsOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{
		"projects": map[string]any{
			"/example/proj": map[string]any{"allowedTools": []any{"Bash"}, "hasTrustDialogAccepted": false},
		},
	})
	if err := Ensure(path, "/example/proj"); err != nil {
		t.Fatal(err)
	}
	proj, _ := readProjects(t, path)["/example/proj"].(map[string]any)
	if proj["hasTrustDialogAccepted"] != true {
		t.Error("flag not set")
	}
	if tools, _ := proj["allowedTools"].([]any); len(tools) != 1 || tools[0] != "Bash" {
		t.Errorf("sibling key lost: %v", proj)
	}
}

func TestEnsureNoFileCreatesOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := Ensure(path, "/example/proj"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !trusted(t, path, "/example/proj") {
		t.Error("project not trusted when file absent")
	}
}

func TestEnsureManyCwds(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{
		"projects": map[string]any{"/example/a": map[string]any{"hasTrustDialogAccepted": true}},
	})
	if err := Ensure(path, "/example/a", "/example/b", "/example/c"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"/example/a", "/example/b", "/example/c"} {
		if !trusted(t, path, d) {
			t.Errorf("%s not trusted", d)
		}
	}
}

func TestEnsureIdempotentWhenAlreadyTrusted(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{
		"projects": map[string]any{"/example/proj": map[string]any{"hasTrustDialogAccepted": true}},
	})
	before, _ := os.ReadFile(path)
	if err := Ensure(path, "/example/proj"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("already-trusted project should be a no-op (no rewrite)")
	}
}

func TestEnsureNoCwdsIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := Ensure(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("no cwds must not create the file")
	}
}

func TestEnsureCorruptFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path, "/example/proj"); err == nil {
		t.Fatal("want parse error")
	}
	if _, err := IsTrusted(path, "/example/proj"); err == nil {
		t.Fatal("want parse error from IsTrusted")
	}
}

func TestEnsureUnreadablePathErrors(t *testing.T) {
	dir := t.TempDir() // a directory where the file should be
	if err := Ensure(dir, "/example/proj"); err == nil {
		t.Fatal("want read error")
	}
	if _, err := IsTrusted(dir, "/example/proj"); err == nil {
		t.Fatal("want read error from IsTrusted")
	}
}

func TestEnsureUnwritableDirErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", ".claude.json")
	if err := Ensure(path, "/example/proj"); err == nil {
		t.Fatal("want write error")
	}
}

func TestIsTrustedMissingFile(t *testing.T) {
	ok, err := IsTrusted(filepath.Join(t.TempDir(), ".claude.json"), "/example/proj")
	if err != nil || ok {
		t.Fatalf("ok = %v, err = %v; want false, nil", ok, err)
	}
}

func TestIsTrustedUntrustedProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	writeJSON(t, path, map[string]any{"projects": map[string]any{"/example/proj": map[string]any{}}})
	if trusted(t, path, "/example/proj") || trusted(t, path, "/example/none") {
		t.Fatal("must not be trusted")
	}
}

func TestSymlinkedCwdIsTrustedUnderResolvedPath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude.json")
	if err := Ensure(path, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := readProjects(t, path)[resolved]; !ok {
		t.Fatalf("projects = %v, want key %q", readProjects(t, path), resolved)
	}
	for _, c := range []string{link, real} {
		if ok, err := IsTrusted(path, c); err != nil || !ok {
			t.Fatalf("IsTrusted(%q) = %v, %v", c, ok, err)
		}
	}
}

func TestUnresolvableCwdKeepsRawPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	missing := filepath.Join(dir, "nope")
	if err := Ensure(path, missing); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsTrusted(path, missing); !ok {
		t.Fatal("missing cwd not trusted under its raw path")
	}
}

func TestEnsureWritesThroughSymlinkedClaudeJSON(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writeJSON(t, target, map[string]any{"keep": 1})
	path := filepath.Join(dir, ".claude.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path, dir); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v, %v", fi, err)
	}
	if readRoot(t, target)["keep"] == nil {
		t.Fatal("target lost its content")
	}
	if ok, _ := IsTrusted(target, dir); !ok {
		t.Fatal("trust not written to the target")
	}
}
