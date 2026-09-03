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
// newAnchoredClient sequence New, Init, Discover, and Clone each need to
// turn (dir, opts) into a ready Client -- factored out so the four
// constructors share one place that gets it right rather than each
// repeating it verbatim. Discover calls it twice (once to build the
// probe client it runs --show-toplevel with, once more to anchor the
// returned Client at the discovered toplevel).
//
// prepare, if non-nil, runs after opts are validated but before dir is
// resolved -- Init's seam for its own os.MkdirAll (which MUST happen only
// once opts are known-good, so a rejected option never has the side
// effect of creating dir, and MUST happen before resolveAnchor, which
// requires dir to already exist) and Clone's seam for actually starting
// the `git clone` invocation. prepare receives the already-resolved cfg
// and gitPath so Clone's prepare can spawn that invocation with the exact
// same environment/binary the anchored Client will use for every later
// call -- Init's own prepare simply ignores both.
func setupAnchoredClient(dir string, opts []Option, prepare func(cfg *config, gitPath string) error) (*Client, error) {
	cfg, gitPath, err := buildClientConfig(opts)
	if err != nil {
		return nil, err
	}
	if prepare != nil {
		if err := prepare(cfg, gitPath); err != nil {
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
	c, err := setupAnchoredClient(dir, opts, func(*config, string) error {
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

// envFor returns the child environment for one invocation: envParsed when
// parsed is true, envUnparsed otherwise -- buildEnv's own LC_ALL=C
// SCOPED-not-blanket rule (design §4.4), applied uniformly by both run
// and startHandle.
func (c *Client) envFor(parsed bool) []string {
	if parsed {
		return c.envParsed
	}
	return c.envUnparsed
}

// newCmd builds the *exec.Cmd every gitclient invocation shares: anchored
// at dir with env as its child environment, plus the context contract
// every git-spawning call honors.
//
// Context contract: every ctx cancellation or deadline is wrapped
// explicitly by the caller (via classifyRunErr) so errors.Is(err,
// context.Canceled) / errors.Is(err, context.DeadlineExceeded) hold. This
// is NOT automatic from exec.CommandContext: once the child has actually
// been signaled, os/exec's Cmd.Wait prefers the process's own (non-zero,
// signal-killed) exit status over the context's error -- ctx.Err() is
// only surfaced by Wait on the narrow race where the process exits
// successfully despite being canceled. So ctx.Err() must be consulted
// independently on every failure path, not inferred from the error Wait
// happened to return. Cmd.WaitDelay is set so a killed child that
// inherited open I/O pipes (e.g. a hook or alias-spawned grandchild)
// cannot stall that kill.
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
//
// Parameterized by dir/gitPath/env (rather than reading a *Client's own
// fields) so Clone's prepare hook -- which spawns `git clone` BEFORE a
// Client exists to anchor at the not-yet-created target directory -- can
// share this exact mechanism instead of re-deriving it, and so Handle's
// streaming spawn (startHandle) and run's buffered spawn build identical
// *exec.Cmd values from one place.
func newCmd(ctx context.Context, gitPath, dir string, env []string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = defaultWaitDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return cmd
}

// run executes one git invocation described by va, anchored at c.dir with
// the environment matching va.Parsed, blocking until it completes and
// returning its captured stdout. classifyRunErr applies the shared
// context/exit-code classification (see newCmd's doc comment for the
// full context contract this depends on).
func (c *Client) run(ctx context.Context, va verbArgs) ([]byte, error) {
	cmd := newCmd(ctx, c.gitPath, c.dir, c.envFor(va.Parsed), va.Args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	return stdout.Bytes(), classifyRunErr(ctx, va.Args, stderr.Bytes(), runErr)
}

// startHandle spawns va anchored at c.dir with c's environment matching
// va.Parsed, returning a Handle immediately once the process has started
// (cmd.Start()) rather than waiting for it to finish -- the streaming
// counterpart to run's buffered spawn-and-wait, shared by every mutating
// role method that returns a *Handle (Fetch, CreateWorktree, Sync,
// Commit, Push).
func (c *Client) startHandle(ctx context.Context, va verbArgs) (*Handle, error) {
	cmd := newCmd(ctx, c.gitPath, c.dir, c.envFor(va.Parsed), va.Args)
	return newHandle(ctx, cmd, va.Args)
}

// Clone runs `git clone` (cloneArgs) into dir and returns a Client
// anchored there plus the Handle for the clone invocation itself.
//
// Mechanics: like Init, Clone goes through setupAnchoredClient's prepare
// seam -- but where Init's prepare only creates an empty directory,
// Clone's prepare additionally STARTS the clone invocation itself (via
// newCmd + newHandle, the same mechanism startHandle uses), using the
// already-resolved gitPath/env setupAnchoredClient's own buildClientConfig
// call hands it, BEFORE dir has been symlink-resolved into an anchor.
// Concretely:
//
//  1. dir is made absolute (NOT YET symlink-resolved -- EvalSymlinks
//     would fail on a path that does not exist yet) and its EMPTY self is
//     created via os.MkdirAll, exactly Init's own seam. `git clone` then
//     targets an already-existing empty directory rather than racing its
//     own directory creation against a concurrent resolveAnchor call.
//  2. The clone invocation is started and returned to the caller as a
//     live *Handle WITHOUT waiting for it to finish: Clone does not block
//     for the whole clone, so a caller gets the same
//     AttachStream/Wait-driven streaming shape every other mutating role
//     method's Handle offers.
//  3. prepare returns nil as soon as the process has STARTED (cmd.Start()
//     succeeded); setupAnchoredClient then resolves and anchors dir. This
//     is safe even though the clone is likely still running in the
//     background, because step 1 already guarantees dir exists on disk
//     (empty, or partially populated by the still-running clone) --
//     resolveAnchor only needs the path to exist, not for the clone to be
//     complete.
//
// A caller that needs the returned Client to be genuinely usable (e.g. to
// run a further command inside the freshly cloned repository) MUST
// Wait() on the returned Handle first and check its error -- exactly like
// every other Handle-returning method's contract.
//
// copts configures the returned Client's own options (WithHome,
// WithCeiling, WithGit, ...) as usual; because buildClientConfig also
// resolves the gitPath/env the clone invocation itself runs with, a
// WithGit override or WithEnv addition applies to the clone command too,
// not just to the anchored Client's later calls.
func Clone(ctx context.Context, url, dir string, opts CloneOptions, copts ...Option) (*Client, *Handle, error) {
	var handle *Handle

	c, err := setupAnchoredClient(dir, copts, func(cfg *config, gitPath string) error {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("gitclient: resolving %s: %w", dir, err)
		}
		if err := os.MkdirAll(absDir, 0o755); err != nil {
			return fmt.Errorf("gitclient: creating %s: %w", absDir, err)
		}

		// Parsed: false -- like every other mutation, the clone
		// invocation must not leak LC_ALL=C into a child that can run
		// hooks (design §4.4).
		ca := cloneArgs(url, absDir, opts.Branch)
		cmd := newCmd(ctx, gitPath, absDir, buildEnv(cfg, false), ca.Args)
		h, err := newHandle(ctx, cmd, ca.Args)
		if err != nil {
			return err
		}
		handle = h
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return c, handle, nil
}
