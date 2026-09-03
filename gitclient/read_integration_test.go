//go:build integration

package gitclient_test

// Integration tests for the read-side roles (Locator, RefReader,
// StatusReader, HistoryReader -- bead pg2-svfbb.4) against real git via
// gittest fixtures (design §6). This file is package gitclient_test (not
// the white-box gitclient package client_test.go uses) for the same
// reason as mutate_integration_test.go: it imports gittest, which imports
// gitfixture, which imports gitclient itself -- importing gittest from an
// internal gitclient test file would be an import cycle.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// --- Locator ---

// TestToplevelReturnsTheRepositoryWorkingTreeRoot proves Toplevel resolves
// to the fixture repo's own working tree root, matching Repo.Dir.
func TestToplevelReturnsTheRepositoryWorkingTreeRoot(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "toplevel"})

	got, err := repo.Client.Toplevel(ctx)
	if err != nil {
		t.Fatalf("Toplevel() error = %v", err)
	}
	if got != repo.Dir {
		t.Errorf("Toplevel() = %q, want %q", got, repo.Dir)
	}
}

// TestCommonDirReturnsTheAbsoluteGitCommonDir proves CommonDir resolves to
// an absolute path ending in .git for a normal (non-worktree) repository.
func TestCommonDirReturnsTheAbsoluteGitCommonDir(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "common-dir"})

	got, err := repo.Client.CommonDir(ctx)
	if err != nil {
		t.Fatalf("CommonDir() error = %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("CommonDir() = %q, want an absolute path", got)
	}
	want := filepath.Join(repo.Dir, ".git")
	if got != want {
		t.Errorf("CommonDir() = %q, want %q", got, want)
	}
}

// TestCurrentBranchOnUnbornHEADReturnsTheInitialBranchName is the unborn-
// HEAD case design §6 explicitly requires: a FRESH fixture with zero
// commits. Verified behaviorally (not merely asserted from documentation)
// that real git's `branch --show-current` still prints the initial branch
// name on an unborn HEAD, so CurrentBranch must NOT mistake this for the
// detached case.
func TestCurrentBranchOnUnbornHEADReturnsTheInitialBranchName(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "unborn-head", InitialBranch: "main"})

	got, err := repo.Client.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("CurrentBranch() on an unborn HEAD: error = %v, want nil", err)
	}
	if got != "main" {
		t.Errorf("CurrentBranch() on an unborn HEAD = %q, want %q", got, "main")
	}
}

// TestCurrentBranchOnANormalBranchReturnsItsName covers the ordinary case
// once a commit exists.
func TestCurrentBranchOnANormalBranchReturnsItsName(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "current-branch", InitialBranch: "trunk"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	got, err := repo.Client.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("CurrentBranch() error = %v", err)
	}
	if got != "trunk" {
		t.Errorf("CurrentBranch() = %q, want %q", got, "trunk")
	}
}

// TestCurrentBranchOnDetachedHEADReturnsErrDetachedHEAD is the detached-
// HEAD case design §6 explicitly requires: checking out a commit SHA
// directly (not a branch) produces a genuinely detached HEAD, verified
// behaviorally against real git (empty stdout from `branch
// --show-current`, exit 0) before asserting CurrentBranch maps that to
// ErrDetachedHEAD.
func TestCurrentBranchOnDetachedHEADReturnsErrDetachedHEAD(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "detached-head"})
	sha, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if _, err := repo.Client.Run(ctx, "checkout", "--detach", sha); err != nil {
		t.Fatalf("checkout --detach %s: %v", sha, err)
	}

	// Confirm the real git behavior this test depends on, rather than
	// merely trusting it: `branch --show-current` on a detached HEAD
	// really does exit 0 with empty stdout.
	raw, err := repo.Client.Run(ctx, "branch", "--show-current")
	if err != nil {
		t.Fatalf("branch --show-current on a detached HEAD: unexpected error %v, want exit 0", err)
	}
	if len(raw) != 0 {
		t.Fatalf("branch --show-current on a detached HEAD = %q, want empty stdout; test setup invalid", raw)
	}

	_, err = repo.Client.CurrentBranch(ctx)
	if !errors.Is(err, gitclient.ErrDetachedHEAD) {
		t.Errorf("CurrentBranch() on a detached HEAD: error = %v, want errors.Is(_, ErrDetachedHEAD)", err)
	}
}

// TestRemoteURLReturnsTheConfiguredURLRaw proves RemoteURL returns the raw
// config value verbatim -- no insteadOf expansion -- by configuring an
// insteadOf rewrite for the remote's host and asserting the returned URL
// is still the ORIGINAL, unrewritten value.
func TestRemoteURLReturnsTheConfiguredURLRaw(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "remote-url"})
	const configuredURL = "https://example.invalid/upstream/repo.git"

	if _, err := repo.Client.Run(ctx, "config", "remote.origin.url", configuredURL); err != nil {
		t.Fatalf("configuring remote.origin.url: %v", err)
	}
	// An insteadOf rewrite: if RemoteURL expanded it (like `remote
	// get-url` would), the returned value would differ from configuredURL.
	if _, err := repo.Client.Run(ctx, "config", "url.https://rewritten.invalid/.insteadOf", "https://example.invalid/"); err != nil {
		t.Fatalf("configuring insteadOf: %v", err)
	}

	got, err := repo.Client.RemoteURL(ctx, "origin")
	if err != nil {
		t.Fatalf("RemoteURL() error = %v", err)
	}
	if got != configuredURL {
		t.Errorf("RemoteURL() = %q, want the raw configured value %q (unexpanded by insteadOf)", got, configuredURL)
	}
}

// TestRemoteURLOnAnUnconfiguredRemoteReturnsErrNoRemote is the design §6
// case: a remote that has genuinely never been configured.
func TestRemoteURLOnAnUnconfiguredRemoteReturnsErrNoRemote(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "remote-url-unconfigured"})

	_, err := repo.Client.RemoteURL(ctx, "origin")
	if !errors.Is(err, gitclient.ErrNoRemote) {
		t.Errorf("RemoteURL() on an unconfigured remote: error = %v, want errors.Is(_, ErrNoRemote)", err)
	}
}

// --- RefReader ---

// TestRefExistsTrueForAnExistingCommitFalseForAMissingOne covers both
// sides of RefExists.
func TestRefExistsTrueForAnExistingCommitFalseForAMissingOne(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "ref-exists"})
	sha, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if ok, err := repo.Client.RefExists(ctx, sha); err != nil || !ok {
		t.Errorf("RefExists(%q) = (%v, %v), want (true, nil)", sha, ok, err)
	}
	if ok, err := repo.Client.RefExists(ctx, "HEAD"); err != nil || !ok {
		t.Errorf("RefExists(HEAD) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := repo.Client.RefExists(ctx, "does-not-exist"); err != nil || ok {
		t.Errorf("RefExists(does-not-exist) = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestHasUpstreamFalseWithoutOneTrueOnceOneIsConfigured proves both sides
// of HasUpstream against a real remote-tracking branch, established via an
// actual Fetch rather than hand-set config, so the "stored as a
// remote-tracking branch" requirement `rev-parse @{u}` enforces is
// genuinely satisfied.
func TestHasUpstreamFalseWithoutOneTrueOnceOneIsConfigured(t *testing.T) {
	ctx := t.Context()
	producer := gittest.New(t, gitfixture.RepoOptions{Suite: "has-upstream-producer"})
	if _, err := producer.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("producer.Commit() error = %v", err)
	}
	bareRemote, err := producer.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing to the bare remote: %v", err)
	}

	local := gittest.New(t, gitfixture.RepoOptions{Suite: "has-upstream-local"})
	if _, err := local.Client.Run(ctx, "remote", "add", "origin", bareRemote.Dir); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	if ok, err := local.Client.HasUpstream(ctx); err != nil || ok {
		t.Errorf("HasUpstream() before any fetch/tracking setup = (%v, %v), want (false, nil)", ok, err)
	}

	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, err := local.Client.Run(ctx, "checkout", "-b", "main", "--track", "origin/main"); err != nil {
		t.Fatalf("checkout --track: %v", err)
	}

	if ok, err := local.Client.HasUpstream(ctx); err != nil || !ok {
		t.Errorf("HasUpstream() after --track checkout = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestHasUpstreamFalseForADanglingUpstreamConfig covers the item-4
// investigation for pg2-i0q71: an upstream that resolves fine, then
// stops resolving because its tracking config was corrupted to name a
// ref that was never fetched (the "dangling upstream config" shape --
// distinct from TestHasUpstreamFalseWithoutOneTrueOnceOneIsConfigured's
// "never configured" shape above). Verified behaviorally against real
// git 2.54.0 that this fails with "fatal: ambiguous argument '@{u}'" --
// a DIFFERENT stderr message from the "never configured" case's "fatal:
// no upstream configured", but the SAME exit 128 -- and HasUpstream's doc
// comment documents both as folding into (false, nil) by design.
func TestHasUpstreamFalseForADanglingUpstreamConfig(t *testing.T) {
	ctx := t.Context()
	producer := gittest.New(t, gitfixture.RepoOptions{Suite: "has-upstream-dangling-producer"})
	if _, err := producer.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("producer.Commit() error = %v", err)
	}
	bareRemote, err := producer.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing to the bare remote: %v", err)
	}

	local := gittest.New(t, gitfixture.RepoOptions{Suite: "has-upstream-dangling-local"})
	if _, err := local.Client.Run(ctx, "remote", "add", "origin", bareRemote.Dir); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, err := local.Client.Run(ctx, "checkout", "-b", "main", "--track", "origin/main"); err != nil {
		t.Fatalf("checkout --track: %v", err)
	}
	if ok, err := local.Client.HasUpstream(ctx); err != nil || !ok {
		t.Fatalf("HasUpstream() before corrupting the tracking config = (%v, %v), want (true, nil); test setup invalid", ok, err)
	}

	// Corrupt the tracking config to name a ref that was never fetched --
	// the dangling shape.
	if _, err := local.Client.Run(ctx, "config", "branch.main.merge", "refs/heads/never-fetched"); err != nil {
		t.Fatalf("corrupting branch.main.merge: %v", err)
	}

	ok, err := local.Client.HasUpstream(ctx)
	if err != nil {
		t.Errorf("HasUpstream() with a dangling upstream config: error = %v, want nil (an ordinary git failure here is reported as false, not an error, per its documented design)", err)
	}
	if ok {
		t.Errorf("HasUpstream() with a dangling upstream config = true, want false")
	}
}

// TestCommitsAheadCountsCommitsReachableFromTipButNotBase proves
// CommitsAhead reports the count of commits on tip that base does not
// have, using two diverging branches so the count is unambiguous.
func TestCommitsAheadCountsCommitsReachableFromTipButNotBase(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "commits-ahead"})
	baseSHA, err := repo.Commit(ctx, "base", map[string]string{"a.txt": "1\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := repo.Client.Run(ctx, "branch", "feature", baseSHA); err != nil {
		t.Fatalf("branch feature: %v", err)
	}
	if _, err := repo.Client.Run(ctx, "checkout", "feature"); err != nil {
		t.Fatalf("checkout feature: %v", err)
	}
	if _, err := repo.Commit(ctx, "c1", map[string]string{"a.txt": "2\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	tipSHA, err := repo.Commit(ctx, "c2", map[string]string{"a.txt": "3\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	got, err := repo.Client.CommitsAhead(ctx, baseSHA, tipSHA)
	if err != nil {
		t.Fatalf("CommitsAhead() error = %v", err)
	}
	if got != 2 {
		t.Errorf("CommitsAhead(base, tip) = %d, want 2", got)
	}

	if got, err := repo.Client.CommitsAhead(ctx, tipSHA, baseSHA); err != nil || got != 0 {
		t.Errorf("CommitsAhead(tip, base) = (%d, %v), want (0, nil)", got, err)
	}
}

// --- StatusReader ---

// TestStatusReportsModifiedAndUntrackedEntries covers the ordinary (non-
// rename) status shapes against real git.
func TestStatusReportsModifiedAndUntrackedEntries(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "status-basic"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo.Dir, "a.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("writing a.txt: %v", err)
	}
	if err := repo.WriteFile("untracked.txt", "new\n"); err != nil {
		t.Fatalf("WriteFile(untracked.txt): %v", err)
	}

	entries, err := repo.Client.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	var sawModified, sawUntracked bool
	for _, e := range entries {
		switch e.Path {
		case "a.txt":
			sawModified = true
			if e.Unstaged != gitclient.StatusModified {
				t.Errorf("a.txt Unstaged = %q, want %q", e.Unstaged, gitclient.StatusModified)
			}
		case "untracked.txt":
			sawUntracked = true
			if e.Staged != gitclient.StatusUntracked || e.Unstaged != gitclient.StatusUntracked {
				t.Errorf("untracked.txt (Staged,Unstaged) = (%q,%q), want (%q,%q)", e.Staged, e.Unstaged, gitclient.StatusUntracked, gitclient.StatusUntracked)
			}
		}
	}
	if !sawModified {
		t.Errorf("Status() did not report a.txt as modified: %#v", entries)
	}
	if !sawUntracked {
		t.Errorf("Status() did not report untracked.txt: %#v", entries)
	}
}

// TestStatusReportsARenameWithReversedNULOrder is THE documented gotcha
// (design §4.1's StatusEntry.OrigPath doc comment): a real `git mv`
// against a real repository must produce a StatusEntry whose Path is the
// NEW name and whose OrigPath is the ORIGINAL name -- proving the parser
// correctly un-reverses -z mode's "new, then original" NUL-delimited
// field order rather than assuming the "orig -> new" order the non -z
// arrow display suggests.
func TestStatusReportsARenameWithReversedNULOrder(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "status-rename"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"original.txt": "some content that survives the rename\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if _, err := repo.Client.Run(ctx, "mv", "original.txt", "renamed.txt"); err != nil {
		t.Fatalf("git mv original.txt renamed.txt: %v", err)
	}

	entries, err := repo.Client.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	var found *gitclient.StatusEntry
	for i := range entries {
		if entries[i].Staged == gitclient.StatusRenamed {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("Status() reported no StatusRenamed entry after `git mv`; test setup invalid: %#v", entries)
	}
	if found.Path != "renamed.txt" {
		t.Errorf("renamed entry Path = %q, want the NEW name %q", found.Path, "renamed.txt")
	}
	if found.OrigPath != "original.txt" {
		t.Errorf("renamed entry OrigPath = %q, want the ORIGINAL name %q -- a swapped Path/OrigPath would report %q here", found.OrigPath, "original.txt", "renamed.txt")
	}
}

// TestStatusReportsAWorkTreeOnlyRenameViaAddDashN is the item-1 regression
// test for pg2-i0q71: git's -z porcelain format also emits a rename/copy
// record when only the UNSTAGED (Y) column -- not the staged (X) column
// TestStatusReportsARenameWithReversedNULOrder above covers -- is 'R'/'C'.
// `mv tracked-file new-name && git add -N new-name` produces exactly this
// shape: a rename that exists only in the worktree, made visible to git
// only via an intent-to-add. Before the fix, parseStatus checked only the
// staged column for R/C, so it never consumed this record's second
// NUL-terminated orig-path field; the NEXT record's own XY-prefix bytes
// were then misread as path text, and Status() failed with "expected a
// space after the XY status code, got ...".
func TestStatusReportsAWorkTreeOnlyRenameViaAddDashN(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "status-worktree-rename"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"original.txt": "some content that survives the rename\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	oldPath := filepath.Join(repo.Dir, "original.txt")
	newPath := filepath.Join(repo.Dir, "renamed.txt")
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("os.Rename(%s, %s): %v", oldPath, newPath, err)
	}
	if _, err := repo.Client.Run(ctx, "add", "-N", "renamed.txt"); err != nil {
		t.Fatalf("git add -N renamed.txt: %v", err)
	}

	entries, err := repo.Client.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v, want nil -- a work-tree-only rename (Unstaged=='R') must parse, not hard-fail", err)
	}

	var found *gitclient.StatusEntry
	for i := range entries {
		if entries[i].Unstaged == gitclient.StatusRenamed {
			found = &entries[i]
		}
	}
	if found == nil {
		t.Fatalf("Status() reported no work-tree-only StatusRenamed entry (Unstaged=='R'); test setup invalid: %#v", entries)
	}
	if found.Staged != gitclient.StatusUnmodified {
		t.Errorf("renamed entry Staged = %q, want %q (this rename is unstaged-only)", found.Staged, gitclient.StatusUnmodified)
	}
	if found.Path != "renamed.txt" {
		t.Errorf("renamed entry Path = %q, want the NEW name %q", found.Path, "renamed.txt")
	}
	if found.OrigPath != "original.txt" {
		t.Errorf("renamed entry OrigPath = %q, want the ORIGINAL name %q", found.OrigPath, "original.txt")
	}
}

// TestIsTrackedTrueForATrackedFileFalseForAnUntrackedOne covers both sides
// of IsTracked.
func TestIsTrackedTrueForATrackedFileFalseForAnUntrackedOne(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "is-tracked"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"tracked.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := repo.WriteFile("untracked.txt", "new\n"); err != nil {
		t.Fatalf("WriteFile(untracked.txt): %v", err)
	}

	if ok, err := repo.Client.IsTracked(ctx, "tracked.txt"); err != nil || !ok {
		t.Errorf("IsTracked(tracked.txt) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := repo.Client.IsTracked(ctx, "untracked.txt"); err != nil || ok {
		t.Errorf("IsTracked(untracked.txt) = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := repo.Client.IsTracked(ctx, "never-existed.txt"); err != nil || ok {
		t.Errorf("IsTracked(never-existed.txt) = (%v, %v), want (false, nil)", ok, err)
	}
}

// --- HistoryReader ---

// TestCommitsReturnsHistoryWithSubjectBodyAndSignatures proves Commits
// parses a real multi-line commit message and both signatures correctly,
// in git log order (newest first).
func TestCommitsReturnsHistoryWithSubjectBodyAndSignatures(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "commits-basic"})
	sha1, err := repo.Commit(ctx, "first commit", map[string]string{"a.txt": "1\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := repo.Client.Run(ctx, "commit", "--allow-empty", "-m", "second commit\n\nwith a body\nspanning multiple lines"); err != nil {
		t.Fatalf("commit --allow-empty: %v", err)
	}
	sha2 := revParse(t, ctx, repo.Client, "HEAD")

	commits, err := repo.Client.Commits(ctx, gitclient.LogOptions{})
	if err != nil {
		t.Fatalf("Commits() error = %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("Commits() returned %d commits, want 2: %#v", len(commits), commits)
	}
	if commits[0].SHA != sha2 {
		t.Errorf("commits[0].SHA = %q, want the newest commit %q (log order)", commits[0].SHA, sha2)
	}
	if commits[0].Subject != "second commit" {
		t.Errorf("commits[0].Subject = %q, want %q", commits[0].Subject, "second commit")
	}
	// git's own %b placeholder includes a trailing newline after the body
	// text (verified behaviorally); parseCommits passes it through as-is.
	if commits[0].Body != "with a body\nspanning multiple lines\n" {
		t.Errorf("commits[0].Body = %q, want the multi-line body", commits[0].Body)
	}
	if commits[0].Author.Name != "gitfixture commits-basic" {
		t.Errorf("commits[0].Author.Name = %q, want the fixture identity", commits[0].Author.Name)
	}
	if commits[0].Author.Email != "commits-basic@gitfixture.invalid" {
		t.Errorf("commits[0].Author.Email = %q, want the fixture identity", commits[0].Author.Email)
	}
	if commits[0].Committer.When.IsZero() {
		t.Errorf("commits[0].Committer.When is zero, want a real timestamp")
	}
	if commits[1].SHA != sha1 {
		t.Errorf("commits[1].SHA = %q, want the older commit %q", commits[1].SHA, sha1)
	}
}

// TestCommitsHonorsBaseHeadNoMergesAndLimit proves LogOptions' filters
// actually change the selected commit set against real history.
func TestCommitsHonorsBaseHeadNoMergesAndLimit(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "commits-filters"})
	baseSHA, err := repo.Commit(ctx, "base", map[string]string{"a.txt": "1\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := repo.Commit(ctx, "c1", map[string]string{"a.txt": "2\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := repo.Commit(ctx, "c2", map[string]string{"a.txt": "3\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// Base..HEAD excludes the base commit itself.
	commits, err := repo.Client.Commits(ctx, gitclient.LogOptions{Base: baseSHA})
	if err != nil {
		t.Fatalf("Commits(Base) error = %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("Commits(Base=%s) returned %d commits, want 2: %#v", baseSHA, len(commits), commits)
	}
	for _, c := range commits {
		if c.SHA == baseSHA {
			t.Errorf("Commits(Base=%s) included the base commit itself", baseSHA)
		}
	}

	// Limit caps the count.
	limited, err := repo.Client.Commits(ctx, gitclient.LogOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Commits(Limit=1) error = %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("Commits(Limit=1) returned %d commits, want 1: %#v", len(limited), limited)
	}

	// NoMerges: build an actual merge commit and confirm it is excluded.
	if _, err := repo.Client.Run(ctx, "checkout", "-b", "side", baseSHA); err != nil {
		t.Fatalf("checkout -b side: %v", err)
	}
	sideSHA, err := repo.Commit(ctx, "side change", map[string]string{"b.txt": "side\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := repo.Client.Run(ctx, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	if _, err := repo.Client.Run(ctx, "merge", "--no-ff", "-m", "merge side", "side"); err != nil {
		t.Fatalf("merge --no-ff side: %v", err)
	}
	mergeSHATrimmed := revParse(t, ctx, repo.Client, "HEAD")

	all, err := repo.Client.Commits(ctx, gitclient.LogOptions{})
	if err != nil {
		t.Fatalf("Commits() error = %v", err)
	}
	var sawMergeInAll bool
	for _, c := range all {
		if c.SHA == mergeSHATrimmed {
			sawMergeInAll = true
		}
	}
	if !sawMergeInAll {
		t.Fatalf("Commits() (no filter) did not include the merge commit %s; test setup invalid: %#v", mergeSHATrimmed, all)
	}

	noMerges, err := repo.Client.Commits(ctx, gitclient.LogOptions{NoMerges: true})
	if err != nil {
		t.Fatalf("Commits(NoMerges) error = %v", err)
	}
	var sawMergeInNoMerges, sawSideInNoMerges bool
	for _, c := range noMerges {
		if c.SHA == mergeSHATrimmed {
			sawMergeInNoMerges = true
		}
		if c.SHA == sideSHA {
			sawSideInNoMerges = true
		}
	}
	if sawMergeInNoMerges {
		t.Errorf("Commits(NoMerges=true) included the merge commit %s, want it excluded", mergeSHATrimmed)
	}
	if !sawSideInNoMerges {
		t.Errorf("Commits(NoMerges=true) excluded the non-merge commit %s, want it included (only the merge commit itself has >1 parent)", sideSHA)
	}
}

// TestCommitsRejectsNegativeLimit proves a negative LogOptions.Limit is
// rejected outright rather than silently falling through logArgs' `if
// opts.Limit > 0` guard and being treated as unlimited -- only 0 is the
// documented unlimited sentinel (LogOptions' own doc comment).
func TestCommitsRejectsNegativeLimit(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "commits-negative-limit"})

	if _, err := repo.Client.Commits(ctx, gitclient.LogOptions{Limit: -1}); err == nil {
		t.Fatal("Commits(Limit: -1): error = nil, want a rejection")
	}
}

// TestChangedFilesReportsAdditionsDeletionsAndBinary proves ChangedFiles'
// merge-base ("...") semantics and both the ordinary numstat shape and the
// binary-file "-\t-" shape against real git.
func TestChangedFilesReportsAdditionsDeletionsAndBinary(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "changed-files"})
	baseSHA, err := repo.Commit(ctx, "base", map[string]string{"a.txt": "line1\nline2\nline3\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if err := repo.WriteFile("a.txt", "line1\nline2 modified\nline3\nline4\n"); err != nil {
		t.Fatalf("WriteFile(a.txt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, "binary.dat"), []byte{0x00, 0x01, 0x02, 0xff}, 0o644); err != nil {
		t.Fatalf("writing binary.dat: %v", err)
	}
	if _, err := repo.Client.Run(ctx, "add", "-A"); err != nil {
		t.Fatalf("add -A: %v", err)
	}
	if _, err := repo.Client.Run(ctx, "commit", "-m", "modify a.txt, add binary.dat"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	changes, err := repo.Client.ChangedFiles(ctx, baseSHA)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}

	byPath := make(map[string]gitclient.FileChange, len(changes))
	for _, c := range changes {
		byPath[c.Path] = c
	}

	a, ok := byPath["a.txt"]
	if !ok {
		t.Fatalf("ChangedFiles() did not report a.txt: %#v", changes)
	}
	if a.Binary {
		t.Errorf("a.txt reported Binary=true, want false")
	}
	if a.Additions == 0 && a.Deletions == 0 {
		t.Errorf("a.txt reported no additions or deletions: %#v", a)
	}

	bin, ok := byPath["binary.dat"]
	if !ok {
		t.Fatalf("ChangedFiles() did not report binary.dat: %#v", changes)
	}
	if !bin.Binary {
		t.Errorf("binary.dat reported Binary=false, want true")
	}
	if bin.Additions != 0 || bin.Deletions != 0 {
		t.Errorf("binary.dat reported (Additions,Deletions) = (%d,%d), want (0,0) for a binary file", bin.Additions, bin.Deletions)
	}
}

// TestChangedFilesNonASCIIFilenameIsNotEscaped is the item-2 regression
// test for pg2-i0q71: numstatArgs was missing -z, so git's default
// core.quotepath=true C-quoted/octal-escaped a non-ASCII filename in
// --numstat's plain output (verified behaviorally: "café.txt" comes back
// as the literal string `"caf\303\251.txt"`), and ChangedFiles returned
// that literal quoted/escaped text as FileChange.Path instead of the real
// name.
func TestChangedFilesNonASCIIFilenameIsNotEscaped(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "changed-files-non-ascii"})
	const name = "café.txt"
	baseSHA, err := repo.Commit(ctx, "base", map[string]string{name: "line1\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := repo.WriteFile(name, "line1\nline2\n"); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
	if _, err := repo.Client.Run(ctx, "commit", "-am", "edit "+name); err != nil {
		t.Fatalf("commit: %v", err)
	}

	changes, err := repo.Client.ChangedFiles(ctx, baseSHA)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("ChangedFiles() returned %d entries, want 1: %#v", len(changes), changes)
	}
	if changes[0].Path != name {
		t.Errorf("ChangedFiles()[0].Path = %q, want the real name %q -- not C-quoted/octal-escaped", changes[0].Path, name)
	}
}

// TestChangedFilesReportsARenamedAndEditedFileWithRealPaths is the item-3
// regression test for pg2-i0q71: git detects a rename by default on
// `diff --numstat` (no flag required), and non -z mode compacts it into a
// single "old.txt => new.txt" descriptor string in the THIRD tab field --
// which the pre-fix parser's naive 3-way split took whole as
// FileChange.Path, never a real path in either tree.
func TestChangedFilesReportsARenamedAndEditedFileWithRealPaths(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "changed-files-rename"})
	baseSHA, err := repo.Commit(ctx, "base", map[string]string{"old.txt": "line1\nline2\nline3\nline4\nline5\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if _, err := repo.Client.Run(ctx, "mv", "old.txt", "new.txt"); err != nil {
		t.Fatalf("git mv old.txt new.txt: %v", err)
	}
	if err := repo.WriteFile("new.txt", "line1\nline2 EDITED\nline3\nline4\nline5\nline6\n"); err != nil {
		t.Fatalf("WriteFile(new.txt): %v", err)
	}
	if _, err := repo.Client.Run(ctx, "commit", "-am", "rename and edit"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	changes, err := repo.Client.ChangedFiles(ctx, baseSHA)
	if err != nil {
		t.Fatalf("ChangedFiles() error = %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("ChangedFiles() returned %d entries, want 1 (a single detected rename); test setup invalid if git did not detect the rename: %#v", len(changes), changes)
	}
	fc := changes[0]
	if fc.Path != "new.txt" {
		t.Errorf("fc.Path = %q, want the real NEW path %q, not a literal \"old.txt => new.txt\" descriptor", fc.Path, "new.txt")
	}
	if fc.OrigPath != "old.txt" {
		t.Errorf("fc.OrigPath = %q, want the real ORIGINAL path %q", fc.OrigPath, "old.txt")
	}
	if fc.Additions == 0 && fc.Deletions == 0 {
		t.Errorf("fc reported no additions/deletions for an edited rename: %#v", fc)
	}
}

// --- Combined coverage: Discover, worktree anchoring, New's ErrNotARepository ---

// TestDiscoverFromASubdirectoryThenLocatorMethodsAgreeWithTheToplevel
// proves Discover, run from a nested subdirectory, anchors a Client whose
// own Locator methods report the SAME toplevel/branch as a Client anchored
// directly at the repo root.
func TestDiscoverFromASubdirectoryThenLocatorMethodsAgreeWithTheToplevel(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "discover-subdir", InitialBranch: "main"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"nested/a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	nested := filepath.Join(repo.Dir, "nested")
	discovered, err := gitclient.Discover(ctx, nested)
	if err != nil {
		t.Fatalf("Discover(%s) error = %v", nested, err)
	}

	top, err := discovered.Toplevel(ctx)
	if err != nil {
		t.Fatalf("discovered.Toplevel() error = %v", err)
	}
	if top != repo.Dir {
		t.Errorf("discovered.Toplevel() = %q, want %q", top, repo.Dir)
	}

	branch, err := discovered.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("discovered.CurrentBranch() error = %v", err)
	}
	if branch != "main" {
		t.Errorf("discovered.CurrentBranch() = %q, want %q", branch, "main")
	}
}

// TestToplevelAndCommonDirDifferBetweenALinkedWorktreeAndItsCanonicalClone
// is the design §6 anchoring case: a Client anchored at a linked worktree
// must report a DIFFERENT Toplevel than one anchored at the canonical
// clone (each worktree has its own working tree root), while CommonDir --
// the shared .git metadata directory -- must be THE SAME for both,
// confirming they are recognized as the same repository.
func TestToplevelAndCommonDirDifferBetweenALinkedWorktreeAndItsCanonicalClone(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "worktree-anchoring"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	wtPath := filepath.Join(t.TempDir(), "linked-wt")
	if err := waitHandle(repo.Client.CreateWorktree(ctx, wtPath, "feature", gitclient.CreateWorktreeOptions{})); err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	wtClient, err := gitclient.New(ctx, wtPath)
	if err != nil {
		t.Fatalf("New(%s) error = %v", wtPath, err)
	}
	// New (design §4.4 D2) resolves symlinks in its anchor, so the
	// expected Toplevel is the RESOLVED wtPath (e.g. darwin's
	// /var -> /private/var), not the raw path this test built.
	wantWtTop, err := filepath.EvalSymlinks(wtPath)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", wtPath, err)
	}

	canonicalTop, err := repo.Client.Toplevel(ctx)
	if err != nil {
		t.Fatalf("canonical Toplevel() error = %v", err)
	}
	wtTop, err := wtClient.Toplevel(ctx)
	if err != nil {
		t.Fatalf("worktree Toplevel() error = %v", err)
	}
	if wtTop == canonicalTop {
		t.Errorf("linked worktree Toplevel() = %q, same as the canonical clone %q; want it distinct", wtTop, canonicalTop)
	}
	if wtTop != wantWtTop {
		t.Errorf("linked worktree Toplevel() = %q, want %q", wtTop, wantWtTop)
	}

	canonicalCommon, err := repo.Client.CommonDir(ctx)
	if err != nil {
		t.Fatalf("canonical CommonDir() error = %v", err)
	}
	wtCommon, err := wtClient.CommonDir(ctx)
	if err != nil {
		t.Fatalf("worktree CommonDir() error = %v", err)
	}
	if wtCommon != canonicalCommon {
		t.Errorf("linked worktree CommonDir() = %q, want it to match the canonical clone's %q (shared .git metadata)", wtCommon, canonicalCommon)
	}
}

// TestNewAtANonRepositoryPathReturnsErrNotARepository exercises New's
// existing (bead pg2-svfbb.2) validation as part of this bead's coverage,
// per design §6: it is a load-bearing control-flow probe for a future
// consumer (pr-pool's idempotent Ensure), so a regression here would be
// silent for this bead's own methods but break that caller.
func TestNewAtANonRepositoryPathReturnsErrNotARepository(t *testing.T) {
	ctx := t.Context()
	_, err := gitclient.New(ctx, t.TempDir())
	if !errors.Is(err, gitclient.ErrNotARepository) {
		t.Errorf("New() at a non-repository path: error = %v, want errors.Is(_, ErrNotARepository)", err)
	}
}

// TestReadMethodsPropagateAnAlreadyCanceledContext proves every read-side
// method's GENERIC c.run error-passthrough branch -- as distinct from the
// specific sentinel-mapping branches the tests above already cover
// (ErrDetachedHEAD, ErrNoRemote, the false-on-exit-1 shapes) -- by forcing
// c.run to fail via an already-canceled context rather than a real git
// exit code. TestRunWrapsAnAlreadyCanceledContext (client_test.go) already
// proves this mechanism produces errors.Is(err, context.Canceled) for the
// Run escape hatch; here every read-side role method is checked the same
// way, since each has its own "if err != nil { return ..., err }" wrapper
// that TestRefExistsTrueForAnExistingCommitFalseForAMissingOne and its
// siblings never reach (they only ever see a nil error or a *GitError with
// a specific exit code, never a non-git error like this one).
func TestReadMethodsPropagateAnAlreadyCanceledContext(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "canceled-context"})
	if _, err := repo.Commit(t.Context(), "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	sha := revParse(t, t.Context(), repo.Client, "HEAD")

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	checks := []struct {
		name string
		call func() error
	}{
		{"Toplevel", func() error { _, err := repo.Client.Toplevel(canceled); return err }},
		{"CommonDir", func() error { _, err := repo.Client.CommonDir(canceled); return err }},
		{"CurrentBranch", func() error { _, err := repo.Client.CurrentBranch(canceled); return err }},
		{"RemoteURL", func() error { _, err := repo.Client.RemoteURL(canceled, "origin"); return err }},
		{"RefExists", func() error { _, err := repo.Client.RefExists(canceled, sha); return err }},
		{"HasUpstream", func() error { _, err := repo.Client.HasUpstream(canceled); return err }},
		{"CommitsAhead", func() error { _, err := repo.Client.CommitsAhead(canceled, sha, sha); return err }},
		{"Status", func() error { _, err := repo.Client.Status(canceled); return err }},
		{"IsTracked", func() error { _, err := repo.Client.IsTracked(canceled, "a.txt"); return err }},
		{"Commits", func() error { _, err := repo.Client.Commits(canceled, gitclient.LogOptions{}); return err }},
		{"ChangedFiles", func() error { _, err := repo.Client.ChangedFiles(canceled, sha); return err }},
	}
	for _, c := range checks {
		if err := c.call(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s(canceled context) error = %v, want errors.Is(_, context.Canceled)", c.name, err)
		}
	}
}
