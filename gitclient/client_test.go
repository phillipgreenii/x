package gitclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These are real-git smoke tests (t.TempDir + Init), not the exhaustive
// //go:build integration/contract suites design §6 describes -- those are
// later beads' scope (pg2-svfbb.4/.5/.6). Here they exist to prove the
// constructors, Run, and the context contract actually work against a
// real git binary.

func TestInitCreatesRepoAnchoredAtResolvedDir(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}

	c, err := Init(ctx, dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if c.dir != want {
		t.Errorf("Client.dir = %q, want %q (t.TempDir(), symlink-resolved)", c.dir, want)
	}
	if !filepath.IsAbs(c.dir) {
		t.Errorf("Client.dir = %q, want an absolute path", c.dir)
	}

	out, err := c.Run(ctx, "status", "--porcelain")
	if err != nil {
		t.Fatalf("Run(status) error = %v", err)
	}
	if len(out) != 0 {
		t.Errorf("Run(status) on a freshly initialized repo = %q, want empty", out)
	}
}

func TestInitDefaultsInitialBranchToMain(t *testing.T) {
	ctx := t.Context()
	c, err := Init(ctx, t.TempDir(), InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	out, err := c.Run(ctx, "branch", "--show-current")
	if err != nil {
		t.Fatalf("Run(branch --show-current) error = %v", err)
	}
	if got := strings.TrimRight(string(out), "\n"); got != "main" {
		t.Errorf("branch --show-current = %q, want %q", got, "main")
	}
}

func TestInitHonorsExplicitInitialBranch(t *testing.T) {
	ctx := t.Context()
	c, err := Init(ctx, t.TempDir(), InitOptions{InitialBranch: "trunk"})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	out, err := c.Run(ctx, "branch", "--show-current")
	if err != nil {
		t.Fatalf("Run(branch --show-current) error = %v", err)
	}
	if got := strings.TrimRight(string(out), "\n"); got != "trunk" {
		t.Errorf("branch --show-current = %q, want %q", got, "trunk")
	}
}

func TestInitBareCreatesBareRepository(t *testing.T) {
	ctx := t.Context()
	c, err := Init(ctx, t.TempDir(), InitOptions{Bare: true})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	out, err := c.Run(ctx, "rev-parse", "--is-bare-repository")
	if err != nil {
		t.Fatalf("Run(rev-parse --is-bare-repository) error = %v", err)
	}
	if got := strings.TrimRight(string(out), "\n"); got != "true" {
		t.Errorf("--is-bare-repository = %q, want %q", got, "true")
	}
}

func TestNewAnchorsAtAnExistingRepository(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	if _, err := Init(ctx, dir, InitOptions{}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	c, err := New(ctx, dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	if c.dir != want {
		t.Errorf("Client.dir = %q, want %q", c.dir, want)
	}
}

func TestNewAnchorsAtAnExistingBareRepository(t *testing.T) {
	// commonDirArgs (rev-parse --git-common-dir), not toplevelArgs
	// (--show-toplevel), is New's validation probe precisely so it
	// succeeds inside a bare repository, which has no work tree.
	ctx := t.Context()
	dir := t.TempDir()
	if _, err := Init(ctx, dir, InitOptions{Bare: true}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if _, err := New(ctx, dir); err != nil {
		t.Errorf("New() on a bare repository: error = %v, want nil", err)
	}
}

func TestNewOnNonRepositoryReturnsErrNotARepository(t *testing.T) {
	ctx := t.Context()
	_, err := New(ctx, t.TempDir())
	if !errors.Is(err, ErrNotARepository) {
		t.Errorf("New() error = %v, want errors.Is(_, ErrNotARepository)", err)
	}
}

func TestNewSurfacesARejectedOptionRatherThanMaskingIt(t *testing.T) {
	ctx := t.Context()
	_, err := New(ctx, t.TempDir(), WithCeiling(""))
	if err == nil {
		t.Fatal("New() error = nil, want the WithCeiling rejection")
	}
	if errors.Is(err, ErrNotARepository) {
		t.Errorf("New() error = %v, want the option error surfaced directly, not masked as ErrNotARepository", err)
	}
}

func TestDiscoverFromASubdirectoryAnchorsAtTheToplevel(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	repo, err := Init(ctx, dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	nested := filepath.Join(repo.dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", nested, err)
	}

	c, err := Discover(ctx, nested)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if c.dir != repo.dir {
		t.Errorf("Discover().dir = %q, want the toplevel %q", c.dir, repo.dir)
	}
}

func TestDiscoverOnNonRepositoryReturnsErrNotARepository(t *testing.T) {
	ctx := t.Context()
	_, err := Discover(ctx, t.TempDir())
	if !errors.Is(err, ErrNotARepository) {
		t.Errorf("Discover() error = %v, want errors.Is(_, ErrNotARepository)", err)
	}
}

func TestRunNeverSetsLCAllC(t *testing.T) {
	ctx := t.Context()
	c, err := Init(ctx, t.TempDir(), InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// git only runs its own alias.<name> config through a shell -- riding
	// one is how the child's own environment can be observed at all
	// (design §6 contract test 3 uses the same trick).
	out, err := c.Run(ctx, "-c", "alias.dumpenv=!env", "dumpenv")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(string(out), "LC_ALL=") {
		t.Errorf("Run()'s child env contains LC_ALL, want it scoped out of the Run escape hatch entirely:\n%s", out)
	}
}

func TestParsedInvocationsSetLCAllC(t *testing.T) {
	ctx := t.Context()
	c, err := Init(ctx, t.TempDir(), InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	out, err := c.run(ctx, verbArgs{Args: []string{"-c", "alias.dumpenv=!env", "dumpenv"}, Parsed: true})
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(string(out), "LC_ALL=C") {
		t.Errorf("a Parsed invocation's child env is missing LC_ALL=C:\n%s", out)
	}
}

func TestRunWrapsAnAlreadyCanceledContext(t *testing.T) {
	dir := t.TempDir()
	c, err := Init(t.Context(), dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Run(ctx, "status"); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() error = %v, want errors.Is(_, context.Canceled)", err)
	}
}

func TestRunWrapsAnAlreadyExpiredDeadline(t *testing.T) {
	dir := t.TempDir()
	c, err := Init(t.Context(), dir, InitOptions{})
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := c.Run(ctx, "status"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run() error = %v, want errors.Is(_, context.DeadlineExceeded)", err)
	}
}

// TestRunWrapsANonExitSpawnError covers run's final fallback branch: a
// runErr that is neither a context error (TestRunWrapsAnAlreadyCanceled
// Context/...ExpiredDeadline) nor an *exec.ExitError (the ordinary
// classify path every other test drives) -- a genuine spawn failure, here
// forced by pointing gitPath at a directory rather than an executable.
func TestRunWrapsANonExitSpawnError(t *testing.T) {
	c := &Client{dir: t.TempDir(), gitPath: t.TempDir()}
	_, err := c.run(t.Context(), verbArgs{Args: []string{"status"}, Parsed: false})
	if err == nil {
		t.Fatal("run() error = nil, want a spawn error (gitPath is a directory, not an executable)")
	}
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		t.Errorf("run() error = %v (*GitError), want a non-GitError spawn error", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("run() error = %v, want a spawn error, not a context error", err)
	}
}

// TestBuildClientConfigWhenGitNotOnPATHReturnsAnError covers
// buildClientConfig's exec.LookPath failure branch: with no WithGit
// override and no "git" resolvable on PATH, construction must fail rather
// than silently proceeding with an empty gitPath.
func TestBuildClientConfigWhenGitNotOnPATHReturnsAnError(t *testing.T) {
	t.Setenv("PATH", "")
	_, _, err := buildClientConfig(nil)
	if err == nil {
		t.Fatal("buildClientConfig(nil) error = nil, want an error resolving the git binary")
	}
	if !strings.Contains(err.Error(), "resolving git binary") {
		t.Errorf("buildClientConfig(nil) error = %v, want it to mention resolving the git binary", err)
	}
}

// TestNewOnANonExistentDirReturnsAWrappedResolveError covers New's own
// resolveAnchor error-wrapping branch -- distinct from
// TestNewOnNonRepositoryReturnsErrNotARepository, which uses an EXISTING
// (but non-repository) directory and so never reaches resolveAnchor's own
// failure mode: a dir that does not exist at all, so
// filepath.EvalSymlinks fails before git is ever invoked.
func TestNewOnANonExistentDirReturnsAWrappedResolveError(t *testing.T) {
	ctx := t.Context()
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	_, err := New(ctx, missing)
	if err == nil {
		t.Fatal("New() on a non-existent dir: error = nil, want an error")
	}
	if errors.Is(err, ErrNotARepository) {
		t.Errorf("New() on a non-existent dir: error = %v, want a resolveAnchor error, not ErrNotARepository", err)
	}
}

// TestInitSurfacesARejectedOption is Init's counterpart to
// TestNewSurfacesARejectedOptionRatherThanMaskingIt: buildClientConfig's
// error must surface directly rather than being swallowed.
func TestInitSurfacesARejectedOption(t *testing.T) {
	ctx := t.Context()
	_, err := Init(ctx, t.TempDir(), InitOptions{}, WithCeiling(""))
	if err == nil {
		t.Fatal("Init() error = nil, want the WithCeiling rejection")
	}
}

// TestInitFailsWhenAParentPathComponentIsAFile covers Init's os.MkdirAll
// error branch: dir's parent segment already exists as a regular file, so
// MkdirAll cannot create dir underneath it.
func TestInitFailsWhenAParentPathComponentIsAFile(t *testing.T) {
	ctx := t.Context()
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", blocker, err)
	}

	_, err := Init(ctx, filepath.Join(blocker, "repo"), InitOptions{})
	if err == nil {
		t.Fatal("Init() under a file (not a directory): error = nil, want an error")
	}
}

// TestInitRejectsAnInvalidInitialBranchName covers Init's c.run(git init)
// error branch: real git rejects an initial branch name containing a
// space ("fatal: invalid initial branch name"), verified behaviorally.
func TestInitRejectsAnInvalidInitialBranchName(t *testing.T) {
	ctx := t.Context()
	_, err := Init(ctx, t.TempDir(), InitOptions{InitialBranch: "bad branch name"})
	if err == nil {
		t.Fatal("Init() with an invalid initial branch name: error = nil, want git's rejection")
	}
}

// TestDiscoverSurfacesARejectedOption is Discover's counterpart to
// TestNewSurfacesARejectedOptionRatherThanMaskingIt.
func TestDiscoverSurfacesARejectedOption(t *testing.T) {
	ctx := t.Context()
	_, err := Discover(ctx, t.TempDir(), WithCeiling(""))
	if err == nil {
		t.Fatal("Discover() error = nil, want the WithCeiling rejection")
	}
	if errors.Is(err, ErrNotARepository) {
		t.Errorf("Discover() error = %v, want the option error surfaced directly, not masked as ErrNotARepository", err)
	}
}

// TestDiscoverOnANonExistentDirReturnsAWrappedResolveError is Discover's
// counterpart to TestNewOnANonExistentDirReturnsAWrappedResolveError.
func TestDiscoverOnANonExistentDirReturnsAWrappedResolveError(t *testing.T) {
	ctx := t.Context()
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	_, err := Discover(ctx, missing)
	if err == nil {
		t.Fatal("Discover() on a non-existent dir: error = nil, want an error")
	}
	if errors.Is(err, ErrNotARepository) {
		t.Errorf("Discover() on a non-existent dir: error = %v, want a resolveAnchor error, not ErrNotARepository", err)
	}
}

// TestDiscoverPropagatesAnAlreadyCanceledContext covers Discover's OWN
// context-error branch (probe.run's ctx.Canceled/DeadlineExceeded check),
// distinct from TestDiscoverOnNonRepositoryReturnsErrNotARepository's
// ordinary git-failure branch: with a real repository to discover from but
// an already-canceled context, Discover must propagate context.Canceled
// rather than masking it as ErrNotARepository.
func TestDiscoverPropagatesAnAlreadyCanceledContext(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(t.Context(), dir, InitOptions{}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Discover(ctx, dir)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Discover() error = %v, want errors.Is(_, context.Canceled)", err)
	}
}
