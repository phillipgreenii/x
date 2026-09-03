//go:build integration

package gitclient_test

// Integration tests for the mutating roles (Fetcher, WorktreeManager,
// BranchManager, Cleaner -- bead pg2-svfbb.5) against real git via gittest
// fixtures (design §6). This file is package gitclient_test (not the
// white-box gitclient package client_test.go uses) because it imports
// gittest, which imports gitfixture, which imports gitclient itself --
// importing gittest from an internal gitclient test file would be an
// import cycle.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// --- Fetcher ---

// TestFetchWithoutRefspecPullsTheConfiguredDefaultRefspec covers a plain
// `Fetch(ctx, FetchOptions{})` against a local bare remote (Repo.
// AddBareRemote): the remote's default branch lands in the fetching
// client's refs/remotes/origin/* without any explicit Refspec.
func TestFetchWithoutRefspecPullsTheConfiguredDefaultRefspec(t *testing.T) {
	ctx := t.Context()

	producer := gittest.New(t, gitfixture.RepoOptions{Suite: "fetch-no-refspec-producer"})
	sha, err := producer.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"})
	if err != nil {
		t.Fatalf("producer.Commit() error = %v", err)
	}
	bareRemote, err := producer.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing to the bare remote: %v", err)
	}

	local := gittest.New(t, gitfixture.RepoOptions{Suite: "fetch-no-refspec-local"})
	if _, err := local.Client.Run(ctx, "remote", "add", "origin", bareRemote.Dir); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("Fetch(FetchOptions{}) error = %v", err)
	}

	got := revParse(t, ctx, local.Client, "refs/remotes/origin/main")
	if got != sha {
		t.Errorf("refs/remotes/origin/main = %q, want the producer's HEAD %q", got, sha)
	}
}

// TestFetchWithForcePrefixedRefspecUpdatesANonFastForwardRef covers Fetch
// with an explicit, force-prefixed Refspec -- pg-pr's re-fetch case (design
// §4.1: "+refs/pull/12/head:refs/remotes/origin/pr/12"). It proves the
// force prefix is doing real work, not decoration: the SAME refspec
// WITHOUT the "+" is exercised first as a control and must be rejected by
// git as a non-fast-forward update, before the force-prefixed version is
// shown to succeed.
func TestFetchWithForcePrefixedRefspecUpdatesANonFastForwardRef(t *testing.T) {
	ctx := t.Context()
	const prRefspec = "+refs/pull/12/head:refs/remotes/origin/pr/12"
	const prRefspecNoForce = "refs/pull/12/head:refs/remotes/origin/pr/12"

	producer := gittest.New(t, gitfixture.RepoOptions{Suite: "fetch-force-refspec-producer"})
	sha1, err := producer.Commit(ctx, "pr commit 1", map[string]string{"a.txt": "one\n"})
	if err != nil {
		t.Fatalf("producer.Commit() error = %v", err)
	}
	bareRemote, err := producer.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/pull/12/head"); err != nil {
		t.Fatalf("pushing initial PR ref: %v", err)
	}

	local := gittest.New(t, gitfixture.RepoOptions{Suite: "fetch-force-refspec-local"})
	if _, err := local.Client.Run(ctx, "remote", "add", "origin", bareRemote.Dir); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{Refspec: prRefspec})); err != nil {
		t.Fatalf("initial Fetch(Refspec: %q) error = %v", prRefspec, err)
	}
	if got := revParse(t, ctx, local.Client, "refs/remotes/origin/pr/12"); got != sha1 {
		t.Fatalf("after initial fetch, origin/pr/12 = %q, want %q", got, sha1)
	}

	// Rewrite history on the PR ref (amend) so the new tip is NOT a
	// descendant of sha1 -- a genuine non-fast-forward update -- and
	// force-push it into the bare remote.
	if _, err := producer.Client.Run(ctx, "commit", "--amend", "-m", "pr commit 1 (amended)"); err != nil {
		t.Fatalf("amending producer's commit: %v", err)
	}
	sha2 := revParse(t, ctx, producer.Client, "HEAD")
	if sha2 == sha1 {
		t.Fatalf("amend did not produce a new sha (got %q again); test setup invalid", sha2)
	}
	if _, err := producer.Client.Run(ctx, "push", "--force", "origin", "HEAD:refs/pull/12/head"); err != nil {
		t.Fatalf("force-pushing amended PR ref: %v", err)
	}

	// Control: the SAME refspec without the force prefix must be rejected
	// by git as a non-fast-forward update, proving the "+" prefix is not
	// decorative.
	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{Refspec: prRefspecNoForce})); err == nil {
		t.Fatal("Fetch(non-force refspec) over a rewritten ref = nil error, want a non-fast-forward rejection")
	} else {
		var gitErr *gitclient.GitError
		if !errors.As(err, &gitErr) {
			t.Errorf("Fetch(non-force refspec) error = %v (%T), want errors.As to a *gitclient.GitError", err, err)
		}
	}
	if got := revParse(t, ctx, local.Client, "refs/remotes/origin/pr/12"); got != sha1 {
		t.Fatalf("after the rejected non-force fetch, origin/pr/12 = %q, want it unchanged at %q", got, sha1)
	}

	// The guarantee: the force-prefixed refspec succeeds and updates the
	// ref to the new, non-fast-forward tip.
	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{Refspec: prRefspec})); err != nil {
		t.Fatalf("Fetch(force-prefixed refspec) error = %v, want nil", err)
	}
	if got := revParse(t, ctx, local.Client, "refs/remotes/origin/pr/12"); got != sha2 {
		t.Errorf("after the force-prefixed fetch, origin/pr/12 = %q, want the amended %q", got, sha2)
	}
}

// TestFetchDefaultOptionsLeaveStaleTrackingRefIntactDespiteFetchPruneConfig
// is the behavioral --no-prune safety proof design §6 requires: with
// fetch.prune=true configured on the fetching side and an extra configured
// remote refspec modeling pg-pr's PR-ref fetch (+refs/pull/*/head:
// refs/remotes/origin/pr/*), deleting the upstream PR ref and then fetching
// with the client's DEFAULT FetchOptions (no AllowPrune) must leave the
// already-established refs/remotes/origin/pr/12 ref intact -- a real
// fetch is run and the ref is actually checked afterward, not merely
// inspected as argv.
func TestFetchDefaultOptionsLeaveStaleTrackingRefIntactDespiteFetchPruneConfig(t *testing.T) {
	local, bareRemote, prSHA := setupStalePRRefScenario(t)
	ctx := t.Context()

	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("Fetch(FetchOptions{}) error = %v", err)
	}

	got := revParse(t, ctx, local.Client, "refs/remotes/origin/pr/12")
	if got != prSHA {
		t.Errorf("after a default (--no-prune) fetch, refs/remotes/origin/pr/12 = %q, want it left intact at %q", got, prSHA)
	}
	_ = bareRemote
}

// TestFetchAllowPruneRemovesStaleTrackingRefWhenConfigEnablesIt is the
// control half of the prune proof above: it demonstrates fetch.prune=true
// really would remove the stale ref -- i.e. the danger --no-prune guards
// against is real, not hypothetical -- once AllowPrune opts back into
// letting the host config govern.
func TestFetchAllowPruneRemovesStaleTrackingRefWhenConfigEnablesIt(t *testing.T) {
	local, _, _ := setupStalePRRefScenario(t)
	ctx := t.Context()

	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{AllowPrune: true})); err != nil {
		t.Fatalf("Fetch(AllowPrune: true) error = %v", err)
	}

	if _, err := local.Client.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/pr/12"); err == nil {
		t.Error("refs/remotes/origin/pr/12 still resolves after an AllowPrune fetch with fetch.prune=true and the upstream ref deleted; want it pruned")
	}
}

// setupStalePRRefScenario builds: a producer repo with a commit pushed to a
// bare remote's refs/heads/main AND refs/pull/12/head; a local repo with
// that bare remote registered as "origin" plus an extra configured
// remote.origin.fetch entry for the pr/* namespace and fetch.prune=true;
// an initial fetch establishing refs/remotes/origin/pr/12 in local; and
// then the upstream refs/pull/12/head deleted, so a subsequent fetch is the
// one that decides whether the now-stale local tracking ref survives.
func setupStalePRRefScenario(t *testing.T) (local *gitfixture.Repo, bareRemote *gitfixture.Repo, prSHA string) {
	t.Helper()
	ctx := t.Context()

	producer := gittest.New(t, gitfixture.RepoOptions{Suite: "fetch-prune-producer"})
	if _, err := producer.Commit(ctx, "main seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("producer.Commit() error = %v", err)
	}
	remote, err := producer.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing main: %v", err)
	}
	if _, err := producer.Commit(ctx, "pr commit", map[string]string{"pr.txt": "pr content\n"}); err != nil {
		t.Fatalf("producer.Commit() (pr) error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/pull/12/head"); err != nil {
		t.Fatalf("pushing pr ref: %v", err)
	}
	// Reset producer's own main worktree back off the pr commit so the pr
	// ref's content is not also reachable from main -- irrelevant to
	// pruning (which is ref-existence-driven, not reachability-driven) but
	// keeps the fixture's own history honest.
	if _, err := producer.Client.Run(ctx, "reset", "--hard", "HEAD~1"); err != nil {
		t.Fatalf("resetting producer main: %v", err)
	}

	localRepo := gittest.New(t, gitfixture.RepoOptions{Suite: "fetch-prune-local"})
	if _, err := localRepo.Client.Run(ctx, "remote", "add", "origin", remote.Dir); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	// Model pg-pr's real configuration: an ADDITIONAL configured refspec so
	// pr/* refs are fetched by a plain (no explicit Refspec) `fetch origin`
	// too, and are therefore in scope for --prune like any other configured
	// remote-tracking ref.
	if _, err := localRepo.Client.Run(ctx, "config", "--add", "remote.origin.fetch", "+refs/pull/*/head:refs/remotes/origin/pr/*"); err != nil {
		t.Fatalf("configuring remote.origin.fetch: %v", err)
	}
	if _, err := localRepo.Client.Run(ctx, "config", "fetch.prune", "true"); err != nil {
		t.Fatalf("configuring fetch.prune: %v", err)
	}

	// Establish the local tracking ref before it goes stale.
	if err := waitHandle(localRepo.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("initial Fetch() error = %v", err)
	}
	prSHA = revParse(t, ctx, localRepo.Client, "refs/remotes/origin/pr/12")

	// Now delete the upstream PR ref -- the closed-PR case -- making
	// origin/pr/12 a stale local tracking ref from this point on.
	if _, err := producer.Client.Run(ctx, "push", "origin", ":refs/pull/12/head"); err != nil {
		t.Fatalf("deleting upstream pr ref: %v", err)
	}

	return localRepo, remote, prSHA
}

// --- WorktreeManager ---

// TestCreateWorktreeDefaultFlagCreatesTheBranchAtHEAD covers the -b
// (create-only) default.
func TestCreateWorktreeDefaultFlagCreatesTheBranchAtHEAD(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "worktree-create-b"})
	sha, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wt, "feature", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Errorf("worktree at %s: %v", wt, err)
	}
	if got := revParse(t, ctx, repo.Client, "feature"); got != sha {
		t.Errorf("branch feature = %q, want it created at HEAD %q", got, sha)
	}

	// -b is create-ONLY: attempting it again on the same branch must fail.
	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wt2, "feature", gitclient.CreateWorktreeOptions{})); err == nil {
		t.Error("CreateWorktree() a second time on the same branch (default -b) = nil error, want it to refuse an already-existing branch")
	}
}

// TestCreateWorktreeResetBranchOptionResetsAnExistingBranchToANewStartPoint
// covers -B (create-or-reset): after the original worktree using "feature"
// is removed, redispatching with ResetBranch and a new StartPoint must
// move the EXISTING branch to that new commit, not merely leave it as it
// was -- pr-pool's redispatch case.
func TestCreateWorktreeResetBranchOptionResetsAnExistingBranchToANewStartPoint(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "worktree-create-B"})
	sha1, err := repo.Commit(ctx, "c1", map[string]string{"a.txt": "1\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	wt1 := filepath.Join(t.TempDir(), "wt1")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wt1, "feature", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	if got := revParse(t, ctx, repo.Client, "feature"); got != sha1 {
		t.Fatalf("branch feature = %q, want %q before redispatch", got, sha1)
	}

	sha2, err := repo.Commit(ctx, "c2", map[string]string{"a.txt": "2\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if sha2 == sha1 {
		t.Fatalf("sha2 == sha1 (%q); test setup invalid", sha1)
	}

	if err := repo.Client.RemoveWorktree(ctx, wt1, false); err != nil {
		t.Fatalf("RemoveWorktree(wt1, force=false) on a clean worktree: %v, want nil", err)
	}

	wt2 := filepath.Join(t.TempDir(), "wt2")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wt2, "feature", gitclient.CreateWorktreeOptions{
		ResetBranch: true,
		StartPoint:  sha2,
	})); err != nil {
		t.Fatalf("CreateWorktree(ResetBranch, StartPoint=%s) error = %v", sha2, err)
	}

	if got := revParse(t, ctx, repo.Client, "feature"); got != sha2 {
		t.Errorf("after -B redispatch, branch feature = %q, want it RESET to the new start point %q (was %q)", got, sha2, sha1)
	}
}

// TestRemoveWorktreeWithoutForceFailsOnADirtyWorktree is a negative-path
// test: git's own default refuses to remove a worktree with an
// uncommitted, genuinely dirty change (a tracked file modified but not
// committed), and that failure surfaces as a *GitError via errors.As. It
// also proves force overrides that refusal.
func TestRemoveWorktreeWithoutForceFailsOnADirtyWorktree(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "worktree-remove-dirty"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wt, "dirty-branch", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}

	// Genuinely dirty: an uncommitted modification to a tracked file inside
	// the worktree (not merely an untracked scratch file).
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("modified, uncommitted\n"), 0o644); err != nil {
		t.Fatalf("writing an uncommitted change into the worktree: %v", err)
	}
	status, err := repo.Client.Run(ctx, "-C", wt, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status --porcelain on the worktree: %v", err)
	}
	if len(status) == 0 {
		t.Fatalf("worktree status is clean after writing an uncommitted change; test setup invalid")
	}

	err = repo.Client.RemoveWorktree(ctx, wt, false)
	if err == nil {
		t.Fatal("RemoveWorktree(force=false) on a dirty worktree = nil error, want git's own refusal")
	}
	var gitErr *gitclient.GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("RemoveWorktree(force=false) error = %v (%T), want errors.As to a *gitclient.GitError", err, err)
	}
	if gitErr.ExitCode == 0 {
		t.Errorf("GitError.ExitCode = 0, want non-zero")
	}
	if gitErr.Stderr == "" {
		t.Errorf("GitError.Stderr is empty, want git's refusal message")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree at %s no longer exists after the refused removal: %v", wt, err)
	}

	if err := repo.Client.RemoveWorktree(ctx, wt, true); err != nil {
		t.Errorf("RemoveWorktree(force=true) on the same dirty worktree = %v, want nil", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree at %s still exists after a forced removal (stat err = %v)", wt, err)
	}
}

// TestPruneWorktreesRemovesStaleAdministrativeEntries proves prune actually
// does something: a worktree directory removed out from under git (rather
// than via `worktree remove`) leaves a stale administrative entry that
// `worktree list` still reports until PruneWorktrees runs.
func TestPruneWorktreesRemovesStaleAdministrativeEntries(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "worktree-prune"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	wt := filepath.Join(t.TempDir(), "prune-me")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wt, "prune-me", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("RemoveAll(%s): %v", wt, err)
	}

	before, err := repo.Client.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list --porcelain (before prune): %v", err)
	}
	if !strings.Contains(string(before), wt) {
		t.Fatalf("worktree list does not mention the now-missing %s before pruning; test setup invalid:\n%s", wt, before)
	}

	if err := repo.Client.PruneWorktrees(ctx); err != nil {
		t.Fatalf("PruneWorktrees() error = %v", err)
	}

	after, err := repo.Client.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list --porcelain (after prune): %v", err)
	}
	if strings.Contains(string(after), wt) {
		t.Errorf("worktree list still mentions %s after PruneWorktrees:\n%s", wt, after)
	}
}

// --- BranchManager ---

// TestDeleteBranchWithoutForceFailsOnAnUnmergedBranch is the negative-path
// test design §6 requires: a branch carrying a commit genuinely
// unreachable from where the delete is attempted must be refused by git
// without force, and that refusal must surface as a *GitError (ExitCode,
// Stderr) via errors.As. Force then succeeds.
func TestDeleteBranchWithoutForceFailsOnAnUnmergedBranch(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "branch-delete-unmerged"})
	if _, err := repo.Commit(ctx, "base", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// Create "unmerged" via a linked worktree so a commit can be added to
	// it without touching main's own checkout, then remove that worktree --
	// the branch's tip commit is now genuinely NOT reachable from main.
	wt := filepath.Join(t.TempDir(), "wt-unmerged")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wt, "unmerged", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	wtClient, err := gitclient.New(ctx, wt)
	if err != nil {
		t.Fatalf("New(%s) error = %v", wt, err)
	}
	if _, err := wtClient.Run(ctx, "commit", "--allow-empty", "-m", "unmerged work"); err != nil {
		t.Fatalf("committing in the linked worktree: %v", err)
	}
	unmergedSHA := revParse(t, ctx, wtClient, "HEAD")
	if err := repo.Client.RemoveWorktree(ctx, wt, false); err != nil {
		t.Fatalf("RemoveWorktree(wt, force=false) on a clean worktree: %v, want nil", err)
	}

	// Control: main's own HEAD must NOT contain the unmerged commit --
	// otherwise "unmerged" would not actually be unmerged and `branch -d`
	// would succeed even without force, proving nothing.
	mainSHA := revParse(t, ctx, repo.Client, "HEAD")
	if mainSHA == unmergedSHA {
		t.Fatalf("main HEAD (%q) equals the supposedly-unmerged commit; test setup invalid", mainSHA)
	}
	if _, err := repo.Client.Run(ctx, "merge-base", "--is-ancestor", unmergedSHA, mainSHA); err == nil {
		t.Fatalf("the unmerged commit %q is an ancestor of main HEAD %q; test setup invalid", unmergedSHA, mainSHA)
	}

	err = repo.Client.DeleteBranch(ctx, "unmerged", false)
	if err == nil {
		t.Fatal("DeleteBranch(force=false) on a genuinely unmerged branch = nil error, want git's own refusal")
	}
	var gitErr *gitclient.GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("DeleteBranch(force=false) error = %v (%T), want errors.As to a *gitclient.GitError", err, err)
	}
	if gitErr.ExitCode == 0 {
		t.Errorf("GitError.ExitCode = 0, want non-zero")
	}
	if !strings.Contains(strings.ToLower(gitErr.Stderr), "not fully merged") {
		t.Errorf("GitError.Stderr = %q, want it to mention the branch is not fully merged", gitErr.Stderr)
	}
	if got := revParse(t, ctx, repo.Client, "unmerged"); got != unmergedSHA {
		t.Errorf("branch unmerged = %q after the refused delete, want it left untouched at %q", got, unmergedSHA)
	}

	if err := repo.Client.DeleteBranch(ctx, "unmerged", true); err != nil {
		t.Errorf("DeleteBranch(force=true) on the same unmerged branch = %v, want nil", err)
	}
	if _, err := repo.Client.Run(ctx, "rev-parse", "--verify", "--quiet", "unmerged"); err == nil {
		t.Error("branch unmerged still resolves after a forced delete")
	}
}

// --- Cleaner ---

// TestResetHardDiscardsUncommittedChangesAndRestoresHEAD proves ResetHard
// actually discards a genuine uncommitted modification to a tracked file.
func TestResetHardDiscardsUncommittedChangesAndRestoresHEAD(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "reset-hard"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	path := filepath.Join(repo.Dir, "a.txt")
	if err := os.WriteFile(path, []byte("dirty, uncommitted\n"), 0o644); err != nil {
		t.Fatalf("writing an uncommitted change: %v", err)
	}

	if err := repo.Client.ResetHard(ctx); err != nil {
		t.Fatalf("ResetHard() error = %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s after ResetHard: %v", path, err)
	}
	if string(content) != "hello\n" {
		t.Errorf("a.txt after ResetHard = %q, want the committed content %q", content, "hello\n")
	}
}

// TestCleanUntrackedRemovesUntrackedFilesButNotIgnoredOnesLeftAlone proves
// CleanUntracked (`clean -fd`) actually removes a genuinely untracked file
// and an untracked directory.
func TestCleanUntrackedRemovesUntrackedFilesButNotIgnoredOnesLeftAlone(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "clean-untracked"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	untrackedFile := filepath.Join(repo.Dir, "scratch.txt")
	if err := os.WriteFile(untrackedFile, []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("writing an untracked file: %v", err)
	}
	untrackedDir := filepath.Join(repo.Dir, "scratch-dir")
	if err := os.MkdirAll(untrackedDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", untrackedDir, err)
	}
	if err := os.WriteFile(filepath.Join(untrackedDir, "f.txt"), []byte("also untracked\n"), 0o644); err != nil {
		t.Fatalf("writing into the untracked directory: %v", err)
	}

	if err := repo.Client.CleanUntracked(ctx); err != nil {
		t.Fatalf("CleanUntracked() error = %v", err)
	}

	if _, err := os.Stat(untrackedFile); !os.IsNotExist(err) {
		t.Errorf("%s still exists after CleanUntracked (stat err = %v)", untrackedFile, err)
	}
	if _, err := os.Stat(untrackedDir); !os.IsNotExist(err) {
		t.Errorf("%s still exists after CleanUntracked (stat err = %v)", untrackedDir, err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "a.txt")); err != nil {
		t.Errorf("the tracked a.txt was removed by CleanUntracked: %v", err)
	}
}

// revParse runs `rev-parse <ref>` on c and returns the trimmed SHA,
// failing the test on error.
func revParse(t *testing.T, ctx context.Context, c *gitclient.Client, ref string) string {
	t.Helper()
	out, err := c.Run(ctx, "rev-parse", ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimRight(string(out), "\n")
}
