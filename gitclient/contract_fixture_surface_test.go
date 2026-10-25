//go:build contract

package gitclient_test

// Contract test for the extended gitfixture surface (bead pg2-wi6q8):
// named repositories, sibling repositories, an initial commit, clones, a
// separate git dir, and the generic Client.Run verbs the git-test
// migrations need. Like contract test 1 (contract_envleak_test.go), the
// FULL GIT_DIR family plus a GIT_CONFIG_COUNT/KEY_0/VALUE_0 injection are
// planted in the test process's own environment BEFORE any fixture is
// constructed, pointed at a decoy repository -- so every NEW constructor
// (not just the verbs run afterwards) is proven immune: the decoy must be
// byte-identical at the end, and every commit must carry the fixture's own
// identity rather than the injected one.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestExtendedFixtureSurfaceIsImmuneToHostileAmbientGitEnvironment(t *testing.T) {
	ctx := t.Context()

	decoyDir := filepath.Join(t.TempDir(), "decoy")
	decoy, err := gitclient.Init(ctx, decoyDir, gitclient.InitOptions{})
	if err != nil {
		t.Fatalf("Init(decoy) error = %v", err)
	}
	// The decoy is hand-built with gitclient.Init (not a gitfixture), so it
	// does not get the fixture's empty core.hooksPath. Without it the host's
	// global commit hook (pg-git-check-identity) runs and refuses the
	// placeholder identity -- by design, gitfixture.invalid included, so a
	// fixture identity that escapes is caught.
	decoyHooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(decoyHooks, 0o755); err != nil {
		t.Fatalf("creating empty decoy hooks dir: %v", err)
	}
	for _, kv := range [][2]string{{"user.name", "decoy"}, {"user.email", "decoy@gitfixture.invalid"}, {"core.hooksPath", decoyHooks}} {
		if _, err := decoy.Run(ctx, "config", kv[0], kv[1]); err != nil {
			t.Fatalf("configuring decoy %s: %v", kv[0], err)
		}
	}
	if err := os.WriteFile(filepath.Join(decoyDir, "decoy.txt"), []byte("decoy content\n"), 0o644); err != nil {
		t.Fatalf("writing decoy.txt: %v", err)
	}
	if _, err := decoy.Run(ctx, "add", "decoy.txt"); err != nil {
		t.Fatalf("staging decoy.txt: %v", err)
	}
	if _, err := decoy.Run(ctx, "commit", "-m", "decoy seed"); err != nil {
		t.Fatalf("committing decoy seed: %v", err)
	}
	resolvedDecoy, err := filepath.EvalSymlinks(decoyDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", decoyDir, err)
	}
	before := snapshotTree(t, resolvedDecoy)
	decoyHead := mustRevParse(t, ctx, decoy, "HEAD")

	decoyGitDir := filepath.Join(resolvedDecoy, ".git")
	const hostileEmail = "hostile-config-injection@leak.invalid"
	t.Setenv("GIT_DIR", decoyGitDir)
	t.Setenv("GIT_WORK_TREE", resolvedDecoy)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoyGitDir, "index"))
	t.Setenv("GIT_COMMON_DIR", decoyGitDir)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(decoyGitDir, "objects"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.email")
	t.Setenv("GIT_CONFIG_VALUE_0", hostileEmail)

	// Control: the hostile environment really redirects a naive git.
	if got := rawGitOutput(t, t.TempDir(), "config", "--get", "user.email"); got != hostileEmail {
		t.Fatalf("control: naive `git config --get user.email` = %q, want the injected %q -- the hostile environment is not real", got, hostileEmail)
	}

	// --- Every new constructor, under the hostile environment. ---
	alpha := gittest.New(t, gitfixture.RepoOptions{Suite: "surface", Name: "alpha", InitialCommit: true, SeparateGitDir: "gitdirs/alpha.git"})
	beta := gittest.NewSibling(t, alpha, "beta", gitfixture.RepoOptions{InitialCommit: true})
	remote, err := alpha.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := alpha.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push alpha: %v", err)
	}
	work := gittest.Clone(t, remote, "work")

	// --- Generic verbs the migrations need, on each repo. ---
	wt, err := beta.RootPath("beta-wt")
	if err != nil {
		t.Fatalf("RootPath() error = %v", err)
	}
	for _, args := range [][]string{
		{"switch", "-c", "feature"},
		{"branch", "other"},
		{"worktree", "add", wt, "-b", "wt-branch"},
		{"worktree", "lock", wt},
		{"worktree", "unlock", wt},
		{"checkout", "main"},
		{"merge", "--ff-only", "feature"},
	} {
		if _, err := beta.Client.Run(ctx, args...); err != nil {
			t.Fatalf("beta: git %v: %v", args, err)
		}
	}
	sha, err := work.Commit(ctx, "from clone", map[string]string{"c.txt": "c\n"})
	if err != nil {
		t.Fatalf("clone Commit() error = %v", err)
	}
	if _, err := work.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("clone push: %v", err)
	}
	if _, err := alpha.Client.Run(ctx, "fetch", "origin"); err != nil {
		t.Fatalf("alpha fetch: %v", err)
	}

	// --- Every repo must have used its OWN identity and git dir. ---
	for label, r := range map[string]*gitfixture.Repo{"alpha": alpha, "beta": beta} {
		out, err := r.Client.Run(ctx, "log", "-1", "--format=%ae")
		if err != nil {
			t.Fatalf("%s: log: %v", label, err)
		}
		if got := strings.TrimRight(string(out), "\n"); got != "surface@gitfixture.invalid" {
			t.Errorf("%s: author email = %q, want the fixture identity (hostile value %q leaked?)", label, got, hostileEmail)
		}
	}
	if got := mustRevParse(t, ctx, alpha.Client, "refs/remotes/origin/main"); got != sha {
		t.Errorf("alpha origin/main = %q, want the clone's commit %q", got, sha)
	}
	gitDirOut, err := alpha.Client.Run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatalf("alpha: rev-parse --absolute-git-dir: %v", err)
	}
	if got, want := strings.TrimRight(string(gitDirOut), "\n"), filepath.Join(alpha.Root(), "gitdirs", "alpha.git"); got != want {
		t.Errorf("alpha git dir = %q, want %q (inside the fixture root)", got, want)
	}

	// --- The decoy is untouched. ---
	assertSnapshotsEqual(t, before, snapshotTree(t, resolvedDecoy), "decoy")
	if got := mustRevParse(t, ctx, decoy, "HEAD"); got != decoyHead {
		t.Errorf("decoy HEAD moved: %q -> %q", decoyHead, got)
	}
	if out, err := decoy.Run(ctx, "branch", "--list", "feature", "other", "wt-branch"); err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("decoy gained branches (out=%q, err=%v)", out, err)
	}
}
