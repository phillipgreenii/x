//go:build contract

// Shared helpers for the design §6 (epic pg2-svfbb) contract-tagged tests
// in this file's package, gitclient_test. This package is required (not
// the white-box gitclient package client_test.go/contract_allowlist_test.go/
// contract_contextkill_test.go use) because these tests import gittest,
// which imports gitfixture, which imports gitclient itself -- importing
// gittest from an internal gitclient test file would be an import cycle
// (same reasoning as mutate_integration_test.go's doc comment).
package gitclient_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
)

// mustRevParse runs `rev-parse <ref>` on c and returns the trimmed SHA,
// failing the test on error. Named distinctly from
// mutate_integration_test.go's revParse (a //go:build integration file)
// so the two never collide when both tags are active in the same build
// (CI's `go test -tags integration,contract ./...`).
func mustRevParse(t *testing.T, ctx context.Context, c *gitclient.Client, ref string) string {
	t.Helper()
	out, err := c.Run(ctx, "rev-parse", ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimRight(string(out), "\n")
}

// rawGitOutput runs a plain, NON-hermetic `git` invocation in dir with a
// nil Env -- so it inherits the test process's OWN environment, including
// whatever ambient vars a test has poisoned via t.Setenv -- and returns
// its trimmed stdout, failing the test on error. It exists only to prove
// a hostile ambient vector is a genuine leak path for ordinary
// (non-gitclient) code: the control every guarantee in this file needs
// before the corresponding gitclient.Client assertion means anything.
func rawGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath(git): %v", err)
	}
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("raw git %v (dir=%s): %v", args, dir, err)
	}
	return strings.TrimRight(string(out), "\n")
}

// snapshotTree returns a map of path (relative to root) -> sha256 digest
// of file content, for every regular file under root. Two snapshots taken
// before/after an operation can be compared for byte-for-byte equality
// without holding the whole tree's content in memory at once.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		sum := sha256.Sum256(content)
		snap[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotting %s: %v", root, err)
	}
	return snap
}

// assertSnapshotsEqual fails the test with a readable diff if before and
// after (as produced by snapshotTree) differ in any path or digest. label
// names the tree in failure messages.
func assertSnapshotsEqual(t *testing.T, before, after map[string]string, label string) {
	t.Helper()
	for path, sum := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s: %s existed before and is now MISSING", label, path)
			continue
		}
		if got != sum {
			t.Errorf("%s: %s content CHANGED (digest %s -> %s)", label, path, sum, got)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s: %s is NEW (did not exist before)", label, path)
		}
	}
}
