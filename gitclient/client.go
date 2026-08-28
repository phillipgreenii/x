package gitclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// defaultWaitDelay bounds how long run waits, once a context
// cancellation/deadline has killed the child git process, for that
// child's inherited I/O pipes to close (design §4.4's context contract).
// Without it, a grandchild that inherited the same stdout/stderr pipes
// (e.g. a hook, or a shell spawned via a git alias) could hold them open
// and stall the kill indefinitely.
const defaultWaitDelay = 5 * time.Second

// Client is the CLI-backed implementation of this package's role
// interfaces (Locator, RefReader, StatusReader, HistoryReader, Fetcher,
// WorktreeManager, BranchManager, Cleaner). The anchor directory and the
// child environment are fixed at construction; a Client holds no per-call
// state and is safe for concurrent use.
//
// The mutating role-interface method implementations (Fetcher,
// WorktreeManager, BranchManager, Cleaner) and their compile-time
// interface-satisfaction assertions live in mutate.go (bead pg2-svfbb.5);
// the read-side roles (Locator, RefReader, StatusReader, HistoryReader)
// and their assertions live in read.go (bead pg2-svfbb.4). This file
// offers the constructors, the Run escape hatch, and the
// spawn/context-contract plumbing they all share (bead pg2-svfbb.2).
type Client struct {
	dir     string // absolute, symlink-resolved anchor (design §4.4 D2)
	gitPath string // resolved git binary: WithGit override, else exec.LookPath at construction

	// envParsed/envUnparsed are the two child environments buildEnv can
	// produce for this client's options -- precomputed once here because
	// the environment is fixed at construction, not per call. envParsed
	// carries LC_ALL=C (invocations whose stdout this client parses);
	// envUnparsed does not (hook-running mutations and Run -- design
	// §4.4's SCOPED, not blanket rule).
	envParsed   []string
	envUnparsed []string
}

// InitOptions configures Init. All fields optional.
type InitOptions struct {
	Bare          bool
	InitialBranch string // default "main"
}

// buildClientConfig applies opts to a fresh config and resolves the git
// binary to invoke, surfacing any error an Option recorded (e.g.
// WithCeiling's empty-entry rejection) rather than silently dropping it --
// buildEnv itself does not check cfg.optErr (see its doc comment in
// env.go), so every constructor MUST go through this instead of calling
// buildEnv directly.
func buildClientConfig(opts []Option) (*config, string, error) {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.optErr != nil {
		return nil, "", cfg.optErr
	}
	gitPath := cfg.git
	if gitPath == "" {
		p, err := exec.LookPath("git")
		if err != nil {
			return nil, "", fmt.Errorf("gitclient: resolving git binary: %w", err)
		}
		gitPath = p
	}
	return cfg, gitPath, nil
}

// resolveAnchor makes dir absolute and resolves any symlinks in it, so a
// later process chdir cannot shift the anchor (design §4.4 D2) and so a
// symlinked root (e.g. darwin's /tmp -> /private/tmp) is anchored at its
// real path.
func resolveAnchor(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// newAnchoredClient constructs a Client anchored at anchor (which MUST
// already be absolute and symlink-resolved) with the two child
// environments buildEnv derives from cfg.
func newAnchoredClient(anchor, gitPath string, cfg *config) *Client {
	return &Client{
		dir:         anchor,
		gitPath:     gitPath,
		envParsed:   buildEnv(cfg, true),
		envUnparsed: buildEnv(cfg, false),
	}
}

// setupAnchoredClient runs the buildClientConfig -> resolveAnchor ->
// newAnchoredClient sequence New, Init, and Discover each need to turn
// (dir, opts) into a ready Client -- factored out so the three
// constructors share one place that gets it right rather than each
// repeating it verbatim. Discover calls it twice (once to build the
// probe client it runs --show-toplevel with, once more to anchor the
// returned Client at the discovered toplevel).
//
// prepare, if non-nil, runs after opts are validated but before dir is
// resolved -- Init's seam for its own os.MkdirAll, which MUST happen
// only once opts are known-good (so a rejected option never has the
// side effect of creating dir) and MUST happen before resolveAnchor
// (which requires dir to already exist).
func setupAnchoredClient(dir string, opts []Option, prepare func() error) (*Client, error) {
	cfg, gitPath, err := buildClientConfig(opts)
	if err != nil {
		return nil, err
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return nil, err
		}
	}
	anchor, err := resolveAnchor(dir)
	if err != nil {
		return nil, fmt.Errorf("gitclient: %s: %w", dir, err)
	}
	return newAnchoredClient(anchor, gitPath, cfg), nil
}

// New anchors a client at dir, which MUST be inside a git repository
// (ErrNotARepository otherwise -- callers use that as the cheap "is this
// a worktree?" probe, e.g. pr-pool's idempotent Ensure). Validation spawns
// git, so New takes a context like every other call. dir is resolved to
// an absolute path (symlinks resolved) at construction so a later process
// chdir cannot shift the anchor. New anchors exactly where told -- a
// client anchored at a linked-worktree path is distinct from one at the
// canonical clone, and one anchored inside a bare repository is valid
// too (the validation probe, `rev-parse --git-common-dir`, succeeds for
// bare repositories; unlike `--show-toplevel` it does not require a work
// tree).
func New(ctx context.Context, dir string, opts ...Option) (*Client, error) {
	c, err := setupAnchoredClient(dir, opts, nil)
	if err != nil {
		return nil, err
	}

	if _, err := c.run(ctx, commonDirArgs()); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrNotARepository, dir, err)
	}
	return c, nil
}

// Init creates a new repository at dir (git init [--bare]
// [--initial-branch]) and returns a client anchored there. It exists so
// fixtures (x/gittest) and any future repo-creating consumer share the
// ONE hermetic environment implementation rather than hand-rolling a
// second.
func Init(ctx context.Context, dir string, init InitOptions, opts ...Option) (*Client, error) {
	c, err := setupAnchoredClient(dir, opts, func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("gitclient: creating %s: %w", dir, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	branch := init.InitialBranch
	if branch == "" {
		branch = "main"
	}
	args := []string{"init", "--initial-branch=" + branch}
	if init.Bare {
		args = append(args, "--bare")
	}
	// `init` is a mutation, not one of the parsed verbs the environment
	// contract names (design §4.4) -- Parsed: false, so LC_ALL=C is not
	// set.
	if _, err := c.run(ctx, verbArgs{Args: args, Parsed: false}); err != nil {
		return nil, err
	}
	return c, nil
}

// Discover walks up from dir to the repository toplevel and anchors there
// (the gitfacet "where am I" case). The walk itself is git's own
// discovery: `rev-parse --show-toplevel` run with dir as the working
// directory performs it, so Discover need not reimplement it.
func Discover(ctx context.Context, dir string, opts ...Option) (*Client, error) {
	probe, err := setupAnchoredClient(dir, opts, nil)
	if err != nil {
		return nil, err
	}

	out, err := probe.run(ctx, toplevelArgs())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrNotARepository, dir, err)
	}

	toplevel := strings.TrimRight(string(out), "\n")
	return setupAnchoredClient(toplevel, opts, nil)
}

// Run executes git with the client's anchor and environment -- the escape
// hatch for verbs not yet modeled. It never sets LC_ALL=C: Run's stdout is
// not parsed by this client, and a blanket LC_ALL=C would otherwise leak
// into any grandchildren git spawns (hooks, aliases) -- design §4.4's
// SCOPED, not blanket rule. Deliberately on the concrete type only: code
// that needs faking depends on a role interface instead.
func (c *Client) Run(ctx context.Context, args ...string) ([]byte, error) {
	return c.run(ctx, verbArgs{Args: args, Parsed: false})
}

// run executes one git invocation described by va, anchored at c.dir with
// the environment matching va.Parsed, and returns its captured stdout.
//
// Context contract: every ctx cancellation or deadline is wrapped
// explicitly here so errors.Is(err, context.Canceled) /
// errors.Is(err, context.DeadlineExceeded) hold. This is NOT automatic
// from exec.CommandContext: once the child has actually been signaled,
// os/exec's Cmd.Wait prefers the process's own (non-zero, signal-killed)
// exit status over the context's error -- ctx.Err() is only surfaced by
// Wait on the narrow race where the process exits successfully despite
// being canceled. So ctx.Err() must be consulted independently on every
// failure path, not inferred from the error Wait happened to return.
// Cmd.WaitDelay is set so a killed child that inherited open I/O pipes
// (e.g. a hook or alias-spawned grandchild) cannot stall that kill.
//
// Otherwise -- no context error -- a non-zero exit is turned into an
// error via classify.
//
// Process-group kill on cancellation: a hook git runs synchronously (e.g.
// post-checkout) is a GRANDCHILD of this call, not a direct child, and it
// can itself spawn further descendants (a shell running a long-lived
// command). exec.CommandContext's default cancellation only kills the
// direct git child; a grandchild that has already forked off its own
// process tree is unaffected and keeps running orphaned. cmd.SysProcAttr
// below puts the child in its OWN new process group (Setpgid), which
// every descendant it forks inherits, and cmd.Cancel overrides the
// default single-process kill to signal that whole group (the negative
// pid convention -- kill(2)/man 2 kill) instead, so a killed invocation
// never leaves a hook's descendants running. WaitDelay still bounds how
// long Wait() waits for the (now entirely dead) group's inherited I/O
// pipes to close.
func (c *Client) run(ctx context.Context, va verbArgs) ([]byte, error) {
	env := c.envUnparsed
	if va.Parsed {
		env = c.envParsed
	}

	cmd := exec.CommandContext(ctx, c.gitPath, va.Args...)
	cmd.Dir = c.dir
	cmd.Env = env
	cmd.WaitDelay = defaultWaitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr == nil {
		return stdout.Bytes(), nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout.Bytes(), fmt.Errorf("gitclient: git %s: %w: %w", strings.Join(va.Args, " "), ctxErr, runErr)
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return stdout.Bytes(), classify(va.Args, exitErr.ExitCode(), stderr.Bytes())
	}
	return stdout.Bytes(), fmt.Errorf("gitclient: git %s: %w", strings.Join(va.Args, " "), runErr)
}
