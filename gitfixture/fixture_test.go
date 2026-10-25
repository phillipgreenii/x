package gitfixture_test

// These are real-git smoke tests (untagged, run in the default `go test`
// tier), matching gitclient/client_test.go's own precedent of exercising
// its real-git constructors directly rather than only through the
// //go:build integration/contract suites -- gitfixture.NewRepo itself
// always calls gitclient.Init (a real git subprocess), so there is no
// tagged-vs-untagged distinction to preserve here that client_test.go
// doesn't already cross.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
)

// TestNewRepoRequiresSuite covers NewRepo's required-field validation:
// RepoOptions.Suite is documented as REQUIRED (design §5 guarantee 3, D7),
// and this is the only test anywhere in this repo that calls
// gitfixture.NewRepo directly with an empty Suite -- gittest.New always
// defaults it to t.Name() before delegating, so this branch is otherwise
// unreached.
func TestNewRepoRequiresSuite(t *testing.T) {
	ctx := t.Context()
	_, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{})
	if err == nil {
		t.Fatal("NewRepo() with an empty Suite: error = nil, want the required-field rejection")
	}
}

// TestWriteFileFailsWhenAParentPathComponentIsAFile covers WriteFile's
// os.MkdirAll error branch: a path segment above rel already exists as a
// regular file, so MkdirAll cannot create the parent directory.
func TestWriteFileFailsWhenAParentPathComponentIsAFile(t *testing.T) {
	ctx := t.Context()
	repo, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "writefile-mkdirall-error"})
	if err != nil {
		t.Fatalf("NewRepo() error = %v", err)
	}

	if err := repo.WriteFile("blocker", "not a directory"); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	if err := repo.WriteFile("blocker/nested.txt", "content"); err == nil {
		t.Fatal("WriteFile() under a file (not a directory): error = nil, want an error")
	}
}

// TestWriteFileFailsWhenTargetIsADirectory covers WriteFile's os.WriteFile
// error branch: rel names an existing directory, so writing a file there
// fails ("is a directory").
func TestWriteFileFailsWhenTargetIsADirectory(t *testing.T) {
	ctx := t.Context()
	repo, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "writefile-target-is-dir"})
	if err != nil {
		t.Fatalf("NewRepo() error = %v", err)
	}

	dirPath := filepath.Join(repo.Dir, "somedir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dirPath, err)
	}
	if err := repo.WriteFile("somedir", "content"); err == nil {
		t.Fatal("WriteFile() writing to a path that is a directory: error = nil, want an error")
	}
}

// TestWriteFileRejectsRelEscapingTheFixtureRoot covers WriteFile's
// containment check: package gitfixture's own doc comment claims
// "hermetic by construction", so a rel that walks above the fixture
// root via "../" components must be rejected rather than silently
// writing outside repo.Dir.
func TestWriteFileRejectsRelEscapingTheFixtureRoot(t *testing.T) {
	ctx := t.Context()
	repo, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "writefile-escape"})
	if err != nil {
		t.Fatalf("NewRepo() error = %v", err)
	}

	if err := repo.WriteFile("../../escape.txt", "content"); err == nil {
		t.Fatal("WriteFile(\"../../escape.txt\"): error = nil, want the containment rejection")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(repo.Dir)), "escape.txt")); statErr == nil {
		t.Fatal("WriteFile(\"../../escape.txt\") was rejected but the escaping file was created anyway")
	}
}

// TestAddBareRemoteRejectsNameEscapingTheFixtureRoot is AddBareRemote's
// counterpart to TestWriteFileRejectsRelEscapingTheFixtureRoot: a remote
// name that walks above the fixture's "remotes" directory via "../"
// components must be rejected rather than nesting the new bare
// repository outside the fixture tree.
func TestAddBareRemoteRejectsNameEscapingTheFixtureRoot(t *testing.T) {
	ctx := t.Context()
	repo, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "addbareremote-escape"})
	if err != nil {
		t.Fatalf("NewRepo() error = %v", err)
	}

	if _, err := repo.AddBareRemote(ctx, "../../escape"); err == nil {
		t.Fatal("AddBareRemote(\"../../escape\"): error = nil, want the containment rejection")
	}
}

func newTestRepo(t *testing.T, opts gitfixture.RepoOptions) *gitfixture.Repo {
	t.Helper()
	if opts.Suite == "" {
		opts.Suite = t.Name()
	}
	repo, err := gitfixture.NewRepo(t.Context(), t.TempDir(), opts)
	if err != nil {
		t.Fatalf("NewRepo() error = %v", err)
	}
	return repo
}

// TestNewRepoNameControlsTheRepoDirectory covers gap 3 (configurable repo
// location): Name picks the directory under the fixture root.
func TestNewRepoNameControlsTheRepoDirectory(t *testing.T) {
	def := newTestRepo(t, gitfixture.RepoOptions{})
	if got := filepath.Base(def.Dir); got != "repo" {
		t.Errorf("default repo dir base = %q, want %q", got, "repo")
	}

	named := newTestRepo(t, gitfixture.RepoOptions{Name: "alpha"})
	if got, want := named.Dir, filepath.Join(named.Root(), "alpha"); got != want {
		t.Errorf("named repo Dir = %q, want %q", got, want)
	}
}

func TestNewRepoRejectsBadNames(t *testing.T) {
	for _, name := range []string{"..", ".", "a/b", "../escape", "home", "hooks", "remotes"} {
		t.Run(name, func(t *testing.T) {
			_, err := gitfixture.NewRepo(t.Context(), t.TempDir(), gitfixture.RepoOptions{Suite: "badname", Name: name})
			if err == nil {
				t.Fatalf("NewRepo(Name=%q) error = nil, want a rejection", name)
			}
		})
	}
}

func TestNewRepoRejectsAnExistingExplicitName(t *testing.T) {
	root := t.TempDir()
	opts := gitfixture.RepoOptions{Suite: "dup", Name: "alpha"}
	if _, err := gitfixture.NewRepo(t.Context(), root, opts); err != nil {
		t.Fatalf("first NewRepo() error = %v", err)
	}
	if _, err := gitfixture.NewRepo(t.Context(), root, opts); err == nil {
		t.Fatal("second NewRepo() of the same explicit Name: error = nil, want an already-exists rejection")
	}
}

// TestNewSiblingSharesTheFixtureRoot covers gap 3's multi-repo shape.
func TestNewSiblingSharesTheFixtureRoot(t *testing.T) {
	ctx := t.Context()
	alpha := newTestRepo(t, gitfixture.RepoOptions{Name: "alpha"})

	beta, err := alpha.NewSibling(ctx, "beta", gitfixture.RepoOptions{InitialBranch: "trunk"})
	if err != nil {
		t.Fatalf("NewSibling() error = %v", err)
	}
	if beta.Root() != alpha.Root() {
		t.Errorf("sibling Root() = %q, want %q", beta.Root(), alpha.Root())
	}
	if got, want := beta.Dir, filepath.Join(alpha.Root(), "beta"); got != want {
		t.Errorf("sibling Dir = %q, want %q", got, want)
	}
	if got := runGit(t, beta, "config", "--get", "user.name"); got != "gitfixture "+t.Name() {
		t.Errorf("sibling user.name = %q, want the parent's suite to be inherited", got)
	}
	if got := runGit(t, beta, "branch", "--show-current"); got != "trunk" {
		t.Errorf("sibling initial branch = %q, want %q", got, "trunk")
	}

	if _, err := alpha.NewSibling(ctx, "", gitfixture.RepoOptions{}); err == nil {
		t.Error("NewSibling(\"\") error = nil, want a name-required rejection")
	}
	if _, err := alpha.NewSibling(ctx, "../escape", gitfixture.RepoOptions{}); err == nil {
		t.Error("NewSibling(\"../escape\") error = nil, want a rejection")
	}
	if _, err := alpha.NewSibling(ctx, "beta", gitfixture.RepoOptions{}); err == nil {
		t.Error("NewSibling() over an existing sibling: error = nil, want an already-exists rejection")
	}
}

// TestInitialCommitCreatesHEAD covers gap 4.
func TestInitialCommitCreatesHEAD(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{InitialCommit: true})

	if got := runGit(t, repo, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commit count = %q, want 1", got)
	}
	if got := runGit(t, repo, "log", "-1", "--format=%s"); got != "initial commit" {
		t.Errorf("subject = %q, want %q", got, "initial commit")
	}
	if got := runGit(t, repo, "branch", "--show-current"); got != "main" {
		t.Errorf("branch = %q, want main", got)
	}
}

func TestNoInitialCommitByDefault(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{})
	if _, err := repo.Client.Run(t.Context(), "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		t.Error("HEAD resolves in a fresh fixture without InitialCommit, want an unborn branch")
	}
}

func TestNewRepoRejectsInvalidBareCombinations(t *testing.T) {
	cases := map[string]gitfixture.RepoOptions{
		"bare+initial commit":   {Suite: "x", Bare: true, InitialCommit: true},
		"bare+separate git dir": {Suite: "x", Bare: true, SeparateGitDir: "g.git"},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := gitfixture.NewRepo(t.Context(), t.TempDir(), opts); err == nil {
				t.Fatal("NewRepo() error = nil, want a rejection")
			}
		})
	}
}

// TestSeparateGitDirKeepsGitDirOutsideTheWorkTree covers gap 5.
func TestSeparateGitDirKeepsGitDirOutsideTheWorkTree(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{SeparateGitDir: "gitdirs/repo.git", InitialCommit: true})

	info, err := os.Stat(filepath.Join(repo.Dir, ".git"))
	if err != nil {
		t.Fatalf("stat .git: %v", err)
	}
	if info.IsDir() {
		t.Error(".git is a directory, want a gitdir: file")
	}
	want := filepath.Join(repo.Root(), "gitdirs", "repo.git")
	if got := runGit(t, repo, "rev-parse", "--absolute-git-dir"); got != want {
		t.Errorf("git dir = %q, want %q", got, want)
	}
	// Identity and hooks redirection were applied to the separate dir's config.
	if got := runGit(t, repo, "log", "-1", "--format=%ae"); got != t.Name()+"@gitfixture.invalid" {
		t.Errorf("author email = %q, want the fixture identity", got)
	}
}

func TestSeparateGitDirRejectsUnsafeLocations(t *testing.T) {
	for _, rel := range []string{"../outside.git", ".", "home/g.git", "hooks", "repo", "repo/inside.git"} {
		t.Run(rel, func(t *testing.T) {
			_, err := gitfixture.NewRepo(t.Context(), t.TempDir(), gitfixture.RepoOptions{Suite: "sgd", SeparateGitDir: rel})
			if err == nil {
				t.Fatalf("NewRepo(SeparateGitDir=%q) error = nil, want a rejection", rel)
			}
		})
	}
}

// TestCloneProducesAHermeticWorkingRepo covers gap 2.
func TestCloneProducesAHermeticWorkingRepo(t *testing.T) {
	ctx := t.Context()
	repo := newTestRepo(t, gitfixture.RepoOptions{InitialCommit: true})
	remote, err := repo.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if _, err := repo.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push: %v", err)
	}

	work, err := remote.Clone(ctx, "work")
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if got, want := work.Dir, filepath.Join(remote.Root(), "work"); got != want {
		t.Errorf("clone Dir = %q, want %q", got, want)
	}
	if got := runGit(t, work, "remote", "get-url", "origin"); got != remote.Dir {
		t.Errorf("clone origin = %q, want %q", got, remote.Dir)
	}
	if got := runGit(t, work, "rev-parse", "HEAD"); got != runGit(t, repo, "rev-parse", "HEAD") {
		t.Errorf("clone HEAD = %q, want the pushed commit", got)
	}

	// The clone commits under the fixture identity and can push back.
	sha, err := work.Commit(ctx, "from clone", map[string]string{"c.txt": "c\n"})
	if err != nil {
		t.Fatalf("clone Commit() error = %v", err)
	}
	if got := runGit(t, work, "log", "-1", "--format=%ae", sha); got != t.Name()+"-remote-origin@gitfixture.invalid" {
		t.Errorf("clone commit author email = %q, want the remote's fixture identity", got)
	}
	if _, err := work.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("clone push: %v", err)
	}
	if got := runGit(t, remote, "rev-parse", "main"); got != sha {
		t.Errorf("remote main = %q, want the clone's commit %q", got, sha)
	}
}

func TestCloneRejectsBadOrExistingNames(t *testing.T) {
	ctx := t.Context()
	repo := newTestRepo(t, gitfixture.RepoOptions{InitialCommit: true})
	if _, err := repo.Clone(ctx, "../escape"); err == nil {
		t.Error("Clone(\"../escape\") error = nil, want a rejection")
	}
	if _, err := repo.Clone(ctx, "repo"); err == nil {
		t.Error("Clone() over the source repo's own directory: error = nil, want an already-exists rejection")
	}
}

func TestCloneSurfacesAFailedClone(t *testing.T) {
	ctx := t.Context()
	repo := newTestRepo(t, gitfixture.RepoOptions{})
	if err := os.RemoveAll(repo.Dir); err != nil {
		t.Fatalf("RemoveAll(%s): %v", repo.Dir, err)
	}
	if _, err := repo.Clone(ctx, "work"); err == nil {
		t.Fatal("Clone() of a vanished source: error = nil, want git's failure")
	}
}

func TestRootPathRejectsEscapes(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{})

	got, err := repo.RootPath("wt/feature")
	if err != nil {
		t.Fatalf("RootPath() error = %v", err)
	}
	if want := filepath.Join(repo.Root(), "wt", "feature"); got != want {
		t.Errorf("RootPath() = %q, want %q", got, want)
	}
	if _, err := repo.RootPath("../escape"); err == nil {
		t.Error("RootPath(\"../escape\") error = nil, want the containment rejection")
	}
}

// TestRunCoversEveryOperationTheGitMigrationsNeed is the evidence for gap 1:
// Repo.Client.Run already executes arbitrary git hermetically, so no new
// runner is needed. Every verb the offender audit listed runs here with no
// exec.Command and no direct git binary call.
func TestRunCoversEveryOperationTheGitMigrationsNeed(t *testing.T) {
	ctx := t.Context()
	repo := newTestRepo(t, gitfixture.RepoOptions{InitialCommit: true})
	remote, err := repo.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}

	wt, err := repo.RootPath("wt")
	if err != nil {
		t.Fatalf("RootPath() error = %v", err)
	}
	steps := [][]string{
		{"config", "commit.gpgsign", "false"},
		{"switch", "-c", "feature"},
		{"branch", "other"},
		{"checkout", "main"},
		{"worktree", "add", wt, "-b", "wt-branch"},
		{"worktree", "lock", wt},
		{"worktree", "unlock", wt},
		{"merge", "--ff-only", "feature"},
		{"push", "origin", "main"},
		{"fetch", "origin"},
		{"rev-parse", "--verify", "origin/main"},
	}
	for _, args := range steps {
		if _, err := repo.Client.Run(ctx, args...); err != nil {
			t.Fatalf("Run(%v) error = %v", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
		t.Errorf("worktree not created at %s: %v", wt, err)
	}
	if got := runGit(t, remote, "rev-parse", "main"); got != runGit(t, repo, "rev-parse", "main") {
		t.Errorf("remote main = %q, want the pushed main", got)
	}
}

// runGit runs git through repo's own hermetic client and returns trimmed stdout.
func runGit(t *testing.T, repo *gitfixture.Repo, args ...string) string {
	t.Helper()
	out, err := repo.Client.Run(t.Context(), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimRight(string(out), "\r\n")
}

// TestOptionsReturnsAFreshHermeticOptionSet covers Repo.Options: the slice
// is non-empty, and mutating a returned slice never changes what a later
// call returns (each call builds its own).
func TestOptionsReturnsAFreshHermeticOptionSet(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{})

	first := repo.Options()
	if len(first) == 0 {
		t.Fatal("Options() returned an empty slice")
	}
	want := len(first)
	for i := range first {
		first[i] = nil // overwrite every element
	}
	_ = append(first, nil, nil) // and append to it

	second := repo.Options()
	if len(second) != want {
		t.Fatalf("len(Options()) after mutating an earlier result = %d, want %d", len(second), want)
	}
	for i, o := range second {
		if o == nil {
			t.Errorf("Options()[%d] is nil after mutating an earlier result", i)
		}
	}
}

// TestClientAtAnchorsAtALinkedWorktreeUnderTheRoot covers the happy path:
// a worktree placed under the fixture root yields a working client.
func TestClientAtAnchorsAtALinkedWorktreeUnderTheRoot(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{InitialCommit: true})
	wt, err := repo.RootPath("wt")
	if err != nil {
		t.Fatalf("RootPath() error = %v", err)
	}
	runGit(t, repo, "worktree", "add", wt, "-b", "wt-branch")

	c, err := repo.ClientAt(t.Context(), wt)
	if err != nil {
		t.Fatalf("ClientAt(%s) error = %v", wt, err)
	}
	out, err := c.Run(t.Context(), "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse via ClientAt client: %v", err)
	}
	if got := strings.TrimRight(string(out), "\n"); got != "wt-branch" {
		t.Errorf("ClientAt client HEAD = %q, want %q", got, "wt-branch")
	}
}

// TestClientAtRejectsDirsOutsideTheFixtureRoot covers the containment
// check: a separate temp dir, a symlink under the root that points outside
// it, a relative escape, and a dir that does not exist all fail, and the
// error names the fixture root.
func TestClientAtRejectsDirsOutsideTheFixtureRoot(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{})

	outside := t.TempDir()
	link := filepath.Join(repo.Root(), "escape-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink(%s, %s): %v", outside, link, err)
	}
	// A sibling directory whose name merely shares the root's string prefix.
	prefixSibling := repo.Root() + "-sibling"
	if err := os.MkdirAll(prefixSibling, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", prefixSibling, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(prefixSibling) })

	cases := map[string]string{
		"separate temp dir":    outside,
		"symlink out of root":  link,
		"relative escape":      filepath.Join(repo.Root(), "..", filepath.Base(outside)),
		"shared string prefix": prefixSibling,
		"missing dir":          filepath.Join(repo.Root(), "does-not-exist"),
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := repo.ClientAt(t.Context(), dir)
			if err == nil {
				t.Fatalf("ClientAt(%s) error = nil (client %v), want a rejection", dir, c)
			}
			if !strings.Contains(err.Error(), repo.Root()) {
				t.Errorf("ClientAt(%s) error = %q, want it to name the fixture root %s", dir, err, repo.Root())
			}
		})
	}
}

// TestClientAtAcceptsTheRootItselfButNotANonRepo covers the boundary: the
// root passes containment, and a contained non-repository directory still
// fails inside gitclient.New (the ceiling stops the walk-up).
func TestClientAtAcceptsTheRootItselfButNotANonRepo(t *testing.T) {
	repo := newTestRepo(t, gitfixture.RepoOptions{})

	if _, err := repo.ClientAt(t.Context(), repo.Root()); err == nil {
		t.Error("ClientAt(root) error = nil, want gitclient.New to reject a non-repository directory")
	} else if strings.Contains(err.Error(), "outside the fixture root") {
		t.Errorf("ClientAt(root) error = %q, want the containment check to PASS the root itself", err)
	}
}
