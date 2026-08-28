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
