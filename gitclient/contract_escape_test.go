//go:build contract

package gitclient_test

// Contract test 2 (design §6, epic pg2-svfbb): fixture escape immunity.
//
// gittest's own guarantee tests (bead pg2-svfbb.3, gittest/guarantee_test.go)
// already prove, generically and thoroughly, that a fixture built on a
// SYMLINKED root (the darwin /var -> /private/var shape) resists ambient
// HOME/system-config poisoning, that GIT_CEILING_DIRECTORIES stops a
// discovery walk from escaping to a real ancestor repository, that hooks
// never run, and that the child env is exactly the documented set. This
// test does not re-derive that coverage; its genuinely new contribution is
// proving the SAME symlinked-root escape scenario specifically from
// gitclient.Client's MUTATING methods (CreateWorktree, DeleteBranch,
// ResetHard, CleanUntracked) rather than only Locator/Discover reads, and
// under a hostile ambient GIT_DIR family (not merely an absent ceiling) --
// the exact leak class test 1 exercises for non-worktree-shaped verbs, now
// combined with a genuinely reachable escape target (the fixture's own
// filesystem ancestor) rather than a decoy positioned elsewhere.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
)

func TestClientMutationsStayInsideASymlinkedFixtureRootDespiteHostileAmbientGitDir(t *testing.T) {
	ctx := t.Context()

	// An ancestor decoy repo, positioned as the fixture root's immediate
	// parent directory -- the escape target if anchoring or ceiling ever
	// fails to hold.
	ancestor := t.TempDir()
	if _, err := gitclient.Init(ctx, ancestor, gitclient.InitOptions{}); err != nil {
		t.Fatalf("Init(ancestor decoy) error = %v", err)
	}
	ancestorResolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", ancestor, err)
	}

	// A SYMLINKED fixture root nested under the decoy.
	realRoot := filepath.Join(ancestor, "real-fixture-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", realRoot, err)
	}
	symlinkRoot := filepath.Join(ancestor, "fixture-root-link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatalf("Symlink(%s, %s): %v", realRoot, symlinkRoot, err)
	}

	repo, err := gitfixture.NewRepo(ctx, symlinkRoot, gitfixture.RepoOptions{Suite: "escape-via-client"})
	if err != nil {
		t.Fatalf("NewRepo(symlinked root) error = %v", err)
	}
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("repo.Commit() error = %v", err)
	}

	// Poison the ambient environment with the GIT_DIR family, pointed at
	// the ANCESTOR decoy -- a genuinely reachable real repository, not
	// merely one that happens not to exist -- and leave it set for the
	// rest of the test.
	ancestorGitDir := filepath.Join(ancestorResolved, ".git")
	t.Setenv("GIT_DIR", ancestorGitDir)
	t.Setenv("GIT_WORK_TREE", ancestorResolved)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(ancestorGitDir, "index"))
	t.Setenv("GIT_COMMON_DIR", ancestorGitDir)

	// Control: prove this ambient redirection is real for a naive,
	// non-hermetic git invocation before trusting the guarantee below.
	unrelated := t.TempDir()
	gotTop, err := filepath.EvalSymlinks(rawGitOutput(t, unrelated, "rev-parse", "--show-toplevel"))
	if err != nil {
		t.Fatalf("EvalSymlinks of the control's --show-toplevel output: %v", err)
	}
	if gotTop != ancestorResolved {
		t.Fatalf("control: a naive `git rev-parse --show-toplevel` run from %s (inheriting the ambient GIT_DIR family) resolved to %q, want the ancestor decoy %q -- the ambient redirection is not real, so this test would prove nothing", unrelated, gotTop, ancestorResolved)
	}

	// The guarantee: Client mutation methods land in the fixture, not the
	// ancestor, despite the hostile ambient env remaining set throughout.
	wtPath := filepath.Join(t.TempDir(), "wt")
	if err := repo.Client.CreateWorktree(ctx, wtPath, "feature", gitclient.CreateWorktreeOptions{}); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(wtPath, ".git")); statErr != nil {
		t.Errorf("CreateWorktree(): worktree not created at %s: %v", wtPath, statErr)
	}
	if _, err := repo.Client.Run(ctx, "rev-parse", "--verify", "--quiet", "feature"); err != nil {
		t.Errorf("branch feature does not resolve inside the fixture: %v", err)
	}
	// The branch must NOT also exist inside the ancestor decoy -- a client
	// explicitly anchored there (unaffected by ambient env, same as any
	// other gitclient.Client) is the check.
	ancestorClient, err := gitclient.New(ctx, ancestorResolved)
	if err != nil {
		t.Fatalf("New(ancestor) error = %v", err)
	}
	if out, err := ancestorClient.Run(ctx, "rev-parse", "--verify", "--quiet", "feature"); err == nil {
		t.Errorf("branch feature ALSO resolves inside the ancestor decoy (out=%q); CreateWorktree escaped the fixture", out)
	}

	if err := repo.Client.RemoveWorktree(ctx, wtPath, true); err != nil {
		t.Fatalf("RemoveWorktree() error = %v", err)
	}
	if err := repo.Client.DeleteBranch(ctx, "feature", true); err != nil {
		t.Fatalf("DeleteBranch() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo.Dir, "a.txt"), []byte("dirty, uncommitted\n"), 0o644); err != nil {
		t.Fatalf("writing a dirty change: %v", err)
	}
	if err := repo.Client.ResetHard(ctx); err != nil {
		t.Fatalf("ResetHard() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil {
		t.Fatalf("reading a.txt: %v", err)
	}
	if string(content) != "hello\n" {
		t.Errorf("a.txt after ResetHard = %q, want %q -- if ResetHard had escaped to the ancestor decoy this file would not exist there at all", content, "hello\n")
	}

	scratch := filepath.Join(repo.Dir, "scratch.txt")
	if err := os.WriteFile(scratch, []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("writing scratch.txt: %v", err)
	}
	if err := repo.Client.CleanUntracked(ctx); err != nil {
		t.Fatalf("CleanUntracked() error = %v", err)
	}
	if _, statErr := os.Stat(scratch); !os.IsNotExist(statErr) {
		t.Errorf("scratch.txt still exists after CleanUntracked (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(ancestorResolved, "scratch.txt")); !os.IsNotExist(statErr) {
		t.Errorf("scratch.txt was created inside the ANCESTOR decoy instead of the fixture (stat err = %v)", statErr)
	}

	// Discovery from a non-repo directory INSIDE the fixture (its own home
	// dir) must FAIL rather than walk out to the ancestor decoy, from
	// gitclient.Discover's own perspective, while the same hostile ambient
	// GIT_DIR family from above is still in effect.
	homeDir := filepath.Join(realRoot, "home")
	if _, statErr := os.Stat(homeDir); statErr != nil {
		t.Fatalf("fixture home dir missing at %s: %v", homeDir, statErr)
	}
	if _, err := gitclient.Discover(ctx, homeDir, gitclient.WithCeiling(realRoot)); !errors.Is(err, gitclient.ErrNotARepository) {
		t.Errorf("Discover(fixture home, WithCeiling(root)) error = %v, want errors.Is(_, ErrNotARepository)", err)
	}
}
