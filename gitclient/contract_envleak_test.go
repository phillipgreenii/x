//go:build contract

package gitclient_test

// Contract test 1 (design §6, epic pg2-svfbb): env-leak immunity, the
// pg2-12795 regression class. The FULL GIT_DIR family plus a
// GIT_CONFIG_COUNT/KEY_0/VALUE_0 injection (pg2-a12rl) are set in the TEST
// PROCESS's own environment, pointing at a DECOY repository that the
// client under test never touches directly. Every mutating client verb is
// then run against a FIXTURE repository while those hostile ambient vars
// remain set for the whole test.
//
// Two controls prove the hostile vectors are REAL leaks -- not merely
// asserted -- before the guarantee below is trusted to mean anything: a
// raw, non-hermetic git invocation that inherits the ambient environment
// is shown to (a) resolve `rev-parse --show-toplevel`, run from an
// unrelated directory, to the decoy anyway (GIT_DIR/GIT_WORK_TREE outrank
// the working directory -- the exact mechanism the epic's problem
// statement, design §1, first proved against git 2.54.0), and (b) read
// back the injected config value via `config --get`. The guarantee then
// asserts the decoy is byte-identical before/after every mutating verb,
// and that the fixture (not the decoy) received every operation using
// its OWN identity rather than the injected one.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestMutatingVerbsAreImmuneToAmbientGitDirFamilyAndConfigInjection(t *testing.T) {
	ctx := t.Context()

	// --- Build the decoy repo BEFORE any hostile ambient env is set. ---
	decoyDir := filepath.Join(t.TempDir(), "decoy")
	decoy, err := gitclient.Init(ctx, decoyDir, gitclient.InitOptions{})
	if err != nil {
		t.Fatalf("Init(decoy) error = %v", err)
	}
	if _, err := decoy.Run(ctx, "config", "user.name", "decoy"); err != nil {
		t.Fatalf("configuring decoy user.name: %v", err)
	}
	if _, err := decoy.Run(ctx, "config", "user.email", "decoy@example.invalid"); err != nil {
		t.Fatalf("configuring decoy user.email: %v", err)
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
	resolvedDecoyDir, err := filepath.EvalSymlinks(decoyDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", decoyDir, err)
	}

	before := snapshotTree(t, resolvedDecoyDir)
	if len(before) == 0 {
		t.Fatalf("decoy snapshot is empty; test setup invalid")
	}

	// --- Poison the TEST PROCESS's own ambient environment: the full
	// GIT_DIR family, pointed at the decoy, plus a GIT_CONFIG_COUNT/KEY_0/
	// VALUE_0 injection (pg2-a12rl). These stay set for the rest of the
	// test via t.Setenv. ---
	decoyGitDir := filepath.Join(resolvedDecoyDir, ".git")
	const hostileEmail = "hostile-config-injection@leak.invalid"
	t.Setenv("GIT_DIR", decoyGitDir)
	t.Setenv("GIT_WORK_TREE", resolvedDecoyDir)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoyGitDir, "index"))
	t.Setenv("GIT_COMMON_DIR", decoyGitDir)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(decoyGitDir, "objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(decoyGitDir, "objects"))
	t.Setenv("GIT_PREFIX", "")
	t.Setenv("GIT_CEILING_DIRECTORIES", resolvedDecoyDir)
	t.Setenv("GIT_NAMESPACE", "decoy-namespace")
	t.Setenv("GIT_DISCOVERY_ACROSS_FILESYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.email")
	t.Setenv("GIT_CONFIG_VALUE_0", hostileEmail)

	// --- Controls: prove both hostile vectors are real for a naive,
	// non-hermetic git invocation before trusting the guarantee. ---
	unrelated := t.TempDir()
	gotTop, err := filepath.EvalSymlinks(rawGitOutput(t, unrelated, "rev-parse", "--show-toplevel"))
	if err != nil {
		t.Fatalf("EvalSymlinks of the control's --show-toplevel output: %v", err)
	}
	if gotTop != resolvedDecoyDir {
		t.Fatalf("control: a naive `git rev-parse --show-toplevel` run from %s (inheriting the ambient GIT_DIR family) resolved to %q, want the decoy %q -- the ambient GIT_DIR family is not actually redirecting anything, so this test would prove nothing", unrelated, gotTop, resolvedDecoyDir)
	}
	gotEmail := rawGitOutput(t, resolvedDecoyDir, "config", "--get", "user.email")
	if gotEmail != hostileEmail {
		t.Fatalf("control: a naive `git config --get user.email` (inheriting the ambient GIT_CONFIG_COUNT/KEY_0/VALUE_0 injection) = %q, want the injected %q -- the config-injection vector is not real, so this test would prove nothing", gotEmail, hostileEmail)
	}

	// --- The guarantee: build a fixture and run EVERY mutating client
	// verb against it, with the hostile ambient env still in effect for
	// the whole call. ---
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "envleak"})
	seedSHA, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"})
	if err != nil {
		t.Fatalf("repo.Commit() error = %v", err)
	}

	// Fetcher
	if _, err := repo.AddBareRemote(ctx, "origin"); err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := repo.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing seed to bare remote: %v", err)
	}
	if err := waitHandle(repo.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := mustRevParse(t, ctx, repo.Client, "refs/remotes/origin/main"); got != seedSHA {
		t.Errorf("after Fetch, refs/remotes/origin/main = %q, want %q", got, seedSHA)
	}

	// WorktreeManager: CreateWorktree / RemoveWorktree
	wtPath := filepath.Join(t.TempDir(), "wt")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wtPath, "wt-branch", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(wtPath, ".git")); statErr != nil {
		t.Errorf("CreateWorktree(): worktree not created at %s: %v", wtPath, statErr)
	}
	if err := repo.Client.RemoveWorktree(ctx, wtPath, false); err != nil {
		t.Fatalf("RemoveWorktree() error = %v", err)
	}
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Errorf("RemoveWorktree(): %s still exists (stat err = %v)", wtPath, statErr)
	}

	// WorktreeManager: PruneWorktrees
	wtStale := filepath.Join(t.TempDir(), "stale-wt")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wtStale, "stale-branch", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree(stale) error = %v", err)
	}
	if err := os.RemoveAll(wtStale); err != nil {
		t.Fatalf("RemoveAll(%s): %v", wtStale, err)
	}
	beforePrune, err := repo.Client.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list (before prune): %v", err)
	}
	if !strings.Contains(string(beforePrune), wtStale) {
		t.Fatalf("worktree list does not mention the now-missing %s; test setup invalid", wtStale)
	}
	if err := repo.Client.PruneWorktrees(ctx); err != nil {
		t.Fatalf("PruneWorktrees() error = %v", err)
	}
	afterPrune, err := repo.Client.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list (after prune): %v", err)
	}
	if strings.Contains(string(afterPrune), wtStale) {
		t.Errorf("worktree list still mentions %s after PruneWorktrees", wtStale)
	}

	// BranchManager: DeleteBranch
	if _, err := repo.Client.Run(ctx, "branch", "throwaway"); err != nil {
		t.Fatalf("branch throwaway: %v", err)
	}
	if err := repo.Client.DeleteBranch(ctx, "throwaway", false); err != nil {
		t.Fatalf("DeleteBranch() error = %v", err)
	}
	if ok, err := repo.Client.RefExists(ctx, "throwaway"); err != nil || ok {
		t.Errorf("branch throwaway still resolves after DeleteBranch: (%v, %v)", ok, err)
	}

	// Cleaner: ResetHard
	if err := os.WriteFile(filepath.Join(repo.Dir, "a.txt"), []byte("dirty, uncommitted\n"), 0o644); err != nil {
		t.Fatalf("writing an uncommitted change: %v", err)
	}
	if err := repo.Client.ResetHard(ctx); err != nil {
		t.Fatalf("ResetHard() error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil {
		t.Fatalf("reading a.txt after ResetHard: %v", err)
	}
	if string(content) != "hello\n" {
		t.Errorf("a.txt after ResetHard = %q, want %q", content, "hello\n")
	}

	// Cleaner: CleanUntracked
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

	// Commit-equivalent write (Repo.Commit): the decisive proof that the
	// GIT_CONFIG_COUNT/KEY_0/VALUE_0 injection (proven real by the control
	// above) did not reach this client's own child processes -- the new
	// commit's author email must be the FIXTURE's own identity, not the
	// injected hostile one.
	secondSHA, err := repo.Commit(ctx, "second", map[string]string{"b.txt": "world\n"})
	if err != nil {
		t.Fatalf("repo.Commit() (second) error = %v", err)
	}
	if secondSHA == seedSHA {
		t.Fatalf("second commit sha equals the seed sha; test setup invalid")
	}
	logOut, err := repo.Client.Run(ctx, "log", "-1", "--format=%ae", secondSHA)
	if err != nil {
		t.Fatalf("log -1 --format=%%ae: %v", err)
	}
	const wantEmail = "envleak@gitfixture.invalid"
	if got := strings.TrimRight(string(logOut), "\n"); got != wantEmail {
		t.Errorf("second commit author email = %q, want the fixture identity %q (the injected hostile config value %q would appear here if GIT_CONFIG_COUNT/KEY_0/VALUE_0 leaked through)", got, wantEmail, hostileEmail)
	}

	// --- The other half of the guarantee: the decoy must be
	// byte-identical to its pre-test snapshot -- config, index, and
	// worktree contents all included, since snapshotTree walks every file
	// under the decoy's resolved root. ---
	after := snapshotTree(t, resolvedDecoyDir)
	assertSnapshotsEqual(t, before, after, "decoy")
}
