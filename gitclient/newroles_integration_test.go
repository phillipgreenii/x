//go:build integration

package gitclient_test

// Integration tests for the roles bead pg2-f1cq7 adds on top of the
// mutate_integration_test.go / read_integration_test.go coverage:
// Syncer, Committer, Pusher, RemoteManager, BranchLister, and the Clone
// constructor. Same package/reasoning as mutate_integration_test.go's own
// doc comment (gitclient_test, not the white-box gitclient package,
// because gittest/gitfixture import gitclient itself).

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

// --- BranchLister ---

func TestListBranchesReturnsAllLocalBranches(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "list-branches"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := repo.Client.Run(ctx, "branch", "feature"); err != nil {
		t.Fatalf("branch feature: %v", err)
	}
	if _, err := repo.Client.Run(ctx, "branch", "another"); err != nil {
		t.Fatalf("branch another: %v", err)
	}

	got, err := repo.Client.ListBranches(ctx)
	if err != nil {
		t.Fatalf("ListBranches() error = %v", err)
	}
	want := []string{"another", "feature", "main"} // git's own listing order is alphabetical
	if len(got) != len(want) {
		t.Fatalf("ListBranches() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListBranches()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestListBranchesOnUnbornHEADReturnsEmpty(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "list-branches-unborn"})

	got, err := repo.Client.ListBranches(ctx)
	if err != nil {
		t.Fatalf("ListBranches() on a freshly initialized repo error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListBranches() on an unborn HEAD = %v, want empty", got)
	}
}

// TestListBranchesPropagatesAnUnderlyingError covers ListBranches' own
// error branch (read.go): a genuine failure from the underlying run()
// call (here, an already-canceled context) must propagate rather than be
// swallowed or misreported as an empty branch list.
func TestListBranchesPropagatesAnUnderlyingError(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "list-branches-error"})
	if _, err := repo.Commit(t.Context(), "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.Client.ListBranches(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListBranches() with an already-canceled context = %v, want errors.Is(_, context.Canceled)", err)
	}
}

// --- Syncer ---

// TestSyncDefaultFetchesAndRebasesOntoTheTrackedUpstream covers Sync's
// default (Onto empty) shape: `pull --rebase --autostash`. The remote
// advances past what local last saw WHILE local has its own unpublished
// commit -- exactly pn rebase.go's default scenario -- and Sync must both
// pick up the remote's new commit and replay local's commit on top of it.
func TestSyncDefaultFetchesAndRebasesOntoTheTrackedUpstream(t *testing.T) {
	ctx := t.Context()

	producer := gittest.New(t, gitfixture.RepoOptions{Suite: "sync-default-producer"})
	if _, err := producer.Commit(ctx, "shared", map[string]string{"shared.txt": "base\n"}); err != nil {
		t.Fatalf("producer.Commit() error = %v", err)
	}
	bareRemote, err := producer.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing seed: %v", err)
	}

	local := gittest.New(t, gitfixture.RepoOptions{Suite: "sync-default-local"})
	if _, err := local.Client.Run(ctx, "remote", "add", "origin", bareRemote.Dir); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("initial Fetch() error = %v", err)
	}
	if _, err := local.Client.Run(ctx, "checkout", "-b", "main", "--track", "origin/main"); err != nil {
		t.Fatalf("checkout --track: %v", err)
	}

	// The remote advances past what local has seen...
	if _, err := producer.Commit(ctx, "remote advance", map[string]string{"remote-change.txt": "from remote\n"}); err != nil {
		t.Fatalf("producer.Commit() (advance) error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing remote advance: %v", err)
	}

	// ...while local makes its own unpublished commit, based on the OLD
	// remote tip.
	if _, err := local.Commit(ctx, "local wip", map[string]string{"local-change.txt": "from local\n"}); err != nil {
		t.Fatalf("local.Commit() error = %v", err)
	}

	if err := waitHandle(local.Client.Sync(ctx, gitclient.SyncOptions{})); err != nil {
		t.Fatalf("Sync(SyncOptions{}) error = %v", err)
	}

	// local's HEAD must now descend from the fetched remote tip (the
	// rebase actually replayed local's commit on top of it, not merely
	// fetched without rebasing).
	if _, err := local.Client.Run(ctx, "merge-base", "--is-ancestor", "origin/main", "HEAD"); err != nil {
		t.Errorf("origin/main is not an ancestor of local HEAD after Sync -- want the rebase to have replayed onto it: %v", err)
	}

	for _, rel := range []string{"shared.txt", "remote-change.txt", "local-change.txt"} {
		if _, err := os.Stat(filepath.Join(local.Dir, rel)); err != nil {
			t.Errorf("after Sync, %s is missing from the working tree: %v", rel, err)
		}
	}
}

// TestSyncOntoRebasesTheCurrentBranchOntoAnExplicitRef covers Sync's
// Onto-set shape: `rebase --autostash <onto>`, with no fetch/pull -- pn
// rebase.go's local-ref rebase mode.
func TestSyncOntoRebasesTheCurrentBranchOntoAnExplicitRef(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "sync-onto"})
	if _, err := repo.Commit(ctx, "base", map[string]string{"base.txt": "base\n"}); err != nil {
		t.Fatalf("Commit(base) error = %v", err)
	}

	if _, err := repo.Client.Run(ctx, "checkout", "-b", "feature"); err != nil {
		t.Fatalf("checkout -b feature: %v", err)
	}
	if _, err := repo.Commit(ctx, "feature work", map[string]string{"feature.txt": "feature\n"}); err != nil {
		t.Fatalf("Commit(feature) error = %v", err)
	}

	if _, err := repo.Client.Run(ctx, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	if _, err := repo.Commit(ctx, "main advance", map[string]string{"main2.txt": "main2\n"}); err != nil {
		t.Fatalf("Commit(main2) error = %v", err)
	}

	if _, err := repo.Client.Run(ctx, "checkout", "feature"); err != nil {
		t.Fatalf("checkout feature: %v", err)
	}

	if err := waitHandle(repo.Client.Sync(ctx, gitclient.SyncOptions{Onto: "main"})); err != nil {
		t.Fatalf("Sync(Onto: main) error = %v", err)
	}

	if _, err := repo.Client.Run(ctx, "merge-base", "--is-ancestor", "main", "HEAD"); err != nil {
		t.Errorf("main is not an ancestor of feature's HEAD after Sync(Onto: main): %v", err)
	}
	for _, rel := range []string{"base.txt", "feature.txt", "main2.txt"} {
		if _, err := os.Stat(filepath.Join(repo.Dir, rel)); err != nil {
			t.Errorf("after Sync(Onto: main), %s is missing from the working tree: %v", rel, err)
		}
	}
}

// --- Committer ---

func TestRestorePathDiscardsAnUncommittedChangeToOnePath(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "committer-restore"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{
		"a.txt": "a\n",
		"b.txt": "b\n",
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo.Dir, "a.txt"), []byte("dirty a\n"), 0o644); err != nil {
		t.Fatalf("writing a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, "b.txt"), []byte("dirty b\n"), 0o644); err != nil {
		t.Fatalf("writing b.txt: %v", err)
	}

	if err := repo.Client.RestorePath(ctx, "a.txt"); err != nil {
		t.Fatalf("RestorePath(a.txt) error = %v", err)
	}

	gotA, err := os.ReadFile(filepath.Join(repo.Dir, "a.txt"))
	if err != nil {
		t.Fatalf("reading a.txt: %v", err)
	}
	if string(gotA) != "a\n" {
		t.Errorf("a.txt after RestorePath = %q, want restored %q", gotA, "a\n")
	}
	gotB, err := os.ReadFile(filepath.Join(repo.Dir, "b.txt"))
	if err != nil {
		t.Fatalf("reading b.txt: %v", err)
	}
	if string(gotB) != "dirty b\n" {
		t.Errorf("b.txt after RestorePath(a.txt) = %q, want it LEFT UNTOUCHED at %q", gotB, "dirty b\n")
	}
}

func TestAddStagesOnlyTheGivenPaths(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "committer-add"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"seed.txt": "seed\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo.Dir, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatalf("writing staged.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, "unstaged.txt"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatalf("writing unstaged.txt: %v", err)
	}

	if err := repo.Client.Add(ctx, "staged.txt"); err != nil {
		t.Fatalf("Add(staged.txt) error = %v", err)
	}

	entries, err := repo.Client.Status(ctx)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	var stagedSeen, unstagedSeen bool
	for _, e := range entries {
		switch e.Path {
		case "staged.txt":
			stagedSeen = true
			if e.Staged != gitclient.StatusAdded {
				t.Errorf("staged.txt Staged = %q, want %q", e.Staged, gitclient.StatusAdded)
			}
		case "unstaged.txt":
			unstagedSeen = true
			if e.Staged != gitclient.StatusUntracked || e.Unstaged != gitclient.StatusUntracked {
				t.Errorf("unstaged.txt = %+v, want it to remain untracked (Add must not have touched it)", e)
			}
		}
	}
	if !stagedSeen {
		t.Error("staged.txt did not appear in Status() output at all")
	}
	if !unstagedSeen {
		t.Error("unstaged.txt did not appear in Status() output at all")
	}
}

func TestCommitCreatesACommitOnTopOfHEAD(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "committer-commit"})
	sha1, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "1\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo.Dir, "b.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatalf("writing b.txt: %v", err)
	}
	if err := repo.Client.Add(ctx, "b.txt"); err != nil {
		t.Fatalf("Add(b.txt) error = %v", err)
	}

	if err := waitHandle(repo.Client.Commit(ctx, "add b.txt")); err != nil {
		t.Fatalf("Commit(\"add b.txt\") error = %v", err)
	}

	sha2 := revParse(t, ctx, repo.Client, "HEAD")
	if sha2 == sha1 {
		t.Fatalf("HEAD unchanged after Commit(); test setup invalid")
	}
	parent := revParse(t, ctx, repo.Client, "HEAD^")
	if parent != sha1 {
		t.Errorf("HEAD^ = %q, want the seed commit %q (Commit must run on top of the current HEAD)", parent, sha1)
	}
}

// --- Pusher ---

func TestPushWithoutSetUpstreamPublishesToTheConfiguredUpstream(t *testing.T) {
	ctx := t.Context()
	producer := gittest.New(t, gitfixture.RepoOptions{Suite: "push-plain-producer"})
	if _, err := producer.Commit(ctx, "seed", map[string]string{"a.txt": "seed\n"}); err != nil {
		t.Fatalf("producer.Commit() error = %v", err)
	}
	bareRemote, err := producer.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := producer.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("pushing seed: %v", err)
	}

	local := gittest.New(t, gitfixture.RepoOptions{Suite: "push-plain-local"})
	if _, err := local.Client.Run(ctx, "remote", "add", "origin", bareRemote.Dir); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if err := waitHandle(local.Client.Fetch(ctx, gitclient.FetchOptions{})); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, err := local.Client.Run(ctx, "checkout", "-b", "main", "--track", "origin/main"); err != nil {
		t.Fatalf("checkout --track: %v", err)
	}
	sha, err := local.Commit(ctx, "local change", map[string]string{"b.txt": "local\n"})
	if err != nil {
		t.Fatalf("local.Commit() error = %v", err)
	}

	if err := waitHandle(local.Client.Push(ctx, gitclient.PushOptions{})); err != nil {
		t.Fatalf("Push(PushOptions{}) error = %v", err)
	}

	got := revParse(t, ctx, bareRemote.Client, "refs/heads/main")
	if got != sha {
		t.Errorf("bare remote refs/heads/main = %q, want the pushed %q", got, sha)
	}
}

func TestPushSetUpstreamPublishesAndRecordsTracking(t *testing.T) {
	ctx := t.Context()
	local := gittest.New(t, gitfixture.RepoOptions{Suite: "push-setupstream-local"})
	bareRemote, err := local.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	sha, err := local.Commit(ctx, "seed", map[string]string{"a.txt": "seed\n"})
	if err != nil {
		t.Fatalf("local.Commit() error = %v", err)
	}

	if ok, err := local.Client.HasUpstream(ctx); err != nil || ok {
		t.Fatalf("HasUpstream() before any push = (%v, %v), want (false, nil); test setup invalid", ok, err)
	}

	if err := waitHandle(local.Client.Push(ctx, gitclient.PushOptions{
		SetUpstream: true,
		Remote:      "origin",
		Branch:      "main",
	})); err != nil {
		t.Fatalf("Push(SetUpstream) error = %v", err)
	}

	got := revParse(t, ctx, bareRemote.Client, "refs/heads/main")
	if got != sha {
		t.Errorf("bare remote refs/heads/main = %q, want the pushed %q", got, sha)
	}
	if ok, err := local.Client.HasUpstream(ctx); err != nil || !ok {
		t.Errorf("HasUpstream() after Push(SetUpstream) = (%v, %v), want (true, nil)", ok, err)
	}
}

// TestPushSetUpstreamRequiresRemoteAndBranch covers Push's own
// validation guard (mutate.go): SetUpstream without both Remote and
// Branch must be rejected BEFORE any process is spawned -- Handle is nil,
// not a Handle for a doomed invocation.
func TestPushSetUpstreamRequiresRemoteAndBranch(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "push-setupstream-missing-fields"})
	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "seed\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	h, err := repo.Client.Push(ctx, gitclient.PushOptions{SetUpstream: true})
	if err == nil {
		t.Fatal("Push(SetUpstream, no Remote/Branch) error = nil, want a validation error")
	}
	if h != nil {
		t.Errorf("Push(SetUpstream, no Remote/Branch) Handle = %v, want nil alongside the validation error", h)
	}
}

// --- RemoteManager ---

func TestAddRemoteRegistersANamedRemote(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "add-remote"})
	if _, err := repo.Client.RemoteURL(ctx, "upstream"); !errors.Is(err, gitclient.ErrNoRemote) {
		t.Fatalf("RemoteURL(upstream) before AddRemote = %v, want ErrNoRemote; test setup invalid", err)
	}

	const url = "https://example.invalid/upstream.git"
	if err := repo.Client.AddRemote(ctx, "upstream", url); err != nil {
		t.Fatalf("AddRemote() error = %v", err)
	}

	got, err := repo.Client.RemoteURL(ctx, "upstream")
	if err != nil {
		t.Fatalf("RemoteURL(upstream) after AddRemote error = %v", err)
	}
	if got != url {
		t.Errorf("RemoteURL(upstream) = %q, want %q", got, url)
	}
}

// --- Clone ---

func TestCloneClonesAndAnchorsAUsableClient(t *testing.T) {
	ctx := t.Context()
	source := gittest.New(t, gitfixture.RepoOptions{Suite: "clone-source"})
	sha, err := source.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"})
	if err != nil {
		t.Fatalf("source.Commit() error = %v", err)
	}

	dest := filepath.Join(t.TempDir(), "cloned")
	c, h, err := gitclient.Clone(ctx, source.Dir, dest, gitclient.CloneOptions{})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("Clone() Handle.Wait() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, ".git")); err != nil {
		t.Errorf("cloned repo missing .git at %s: %v", dest, err)
	}
	if got := revParse(t, ctx, c, "HEAD"); got != sha {
		t.Errorf("cloned HEAD = %q, want the source's %q", got, sha)
	}
	content, err := os.ReadFile(filepath.Join(dest, "a.txt"))
	if err != nil {
		t.Fatalf("reading a.txt in the clone: %v", err)
	}
	if string(content) != "hello\n" {
		t.Errorf("a.txt content = %q, want %q", content, "hello\n")
	}
}

func TestCloneWithBranchOptionChecksOutThatBranch(t *testing.T) {
	ctx := t.Context()
	source := gittest.New(t, gitfixture.RepoOptions{Suite: "clone-branch-source"})
	mainSHA, err := source.Commit(ctx, "on main", map[string]string{"a.txt": "main\n"})
	if err != nil {
		t.Fatalf("source.Commit() error = %v", err)
	}
	if _, err := source.Client.Run(ctx, "checkout", "-b", "other"); err != nil {
		t.Fatalf("checkout -b other: %v", err)
	}
	otherSHA, err := source.Commit(ctx, "on other", map[string]string{"b.txt": "other\n"})
	if err != nil {
		t.Fatalf("source.Commit() (other) error = %v", err)
	}
	if otherSHA == mainSHA {
		t.Fatalf("otherSHA == mainSHA; test setup invalid")
	}

	dest := filepath.Join(t.TempDir(), "cloned-other")
	c, h, err := gitclient.Clone(ctx, source.Dir, dest, gitclient.CloneOptions{Branch: "other"})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if err := h.Wait(); err != nil {
		t.Fatalf("Clone() Handle.Wait() error = %v", err)
	}

	branch, err := c.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("CurrentBranch() error = %v", err)
	}
	if branch != "other" {
		t.Errorf("CurrentBranch() = %q, want %q", branch, "other")
	}
	if got := revParse(t, ctx, c, "HEAD"); got != otherSHA {
		t.Errorf("cloned HEAD = %q, want %q", got, otherSHA)
	}
}

// TestCloneIntoAnAlreadyNonEmptyDirectoryFails covers Clone's own error
// path: git itself refuses to clone into an existing non-empty directory,
// and that failure must surface as an ordinary error (via the returned
// Handle's Wait(), same as any other mutating invocation's failure) --
// not be swallowed or panic.
func TestCloneIntoAnAlreadyNonEmptyDirectoryFails(t *testing.T) {
	ctx := t.Context()
	source := gittest.New(t, gitfixture.RepoOptions{Suite: "clone-nonempty-source"})
	if _, err := source.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("source.Commit() error = %v", err)
	}

	dest := filepath.Join(t.TempDir(), "occupied")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dest, err)
	}
	if err := os.WriteFile(filepath.Join(dest, "occupied.txt"), []byte("already here\n"), 0o644); err != nil {
		t.Fatalf("writing occupied.txt: %v", err)
	}

	_, h, err := gitclient.Clone(ctx, source.Dir, dest, gitclient.CloneOptions{})
	if err != nil {
		t.Fatalf("Clone() top-level error = %v, want the failure to surface via Wait() instead", err)
	}
	if err := h.Wait(); err == nil {
		t.Fatal("Clone() into a non-empty directory: Handle.Wait() = nil, want git's own refusal")
	}
}
