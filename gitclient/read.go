package gitclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// trimTrailingNewline drops the single trailing newline git appends to
// the one-line stdout of rev-parse/branch/config invocations -- the
// idiom Toplevel, CommonDir, CurrentBranch, and RemoteURL each repeat.
func trimTrailingNewline(s string) string {
	return strings.TrimRight(s, "\n")
}

// Compile-time assertions that Client satisfies the four read-side role
// interfaces this file implements. The mutating roles' assertions
// (Fetcher, WorktreeManager, BranchManager, Cleaner) live in mutate.go
// (bead pg2-svfbb.5).
var (
	_ Locator       = (*Client)(nil)
	_ RefReader     = (*Client)(nil)
	_ StatusReader  = (*Client)(nil)
	_ HistoryReader = (*Client)(nil)
)

// --- Locator ---

// Toplevel runs `rev-parse --show-toplevel` (toplevelArgs).
func (c *Client) Toplevel(ctx context.Context) (string, error) {
	out, err := c.run(ctx, toplevelArgs())
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}

// CommonDir runs `rev-parse --path-format=absolute --git-common-dir`
// (commonDirArgs).
func (c *Client) CommonDir(ctx context.Context) (string, error) {
	out, err := c.run(ctx, commonDirArgs())
	if err != nil {
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}

// CurrentBranch runs `branch --show-current` (currentBranchArgs). Empty
// stdout on a SUCCESSFUL invocation (exit 0) means HEAD does not point at
// a branch -- ErrDetachedHEAD -- verified behaviorally against real git
// 2.54.0: an unborn HEAD (a freshly `git init`ed repository with zero
// commits) still prints the initial branch name (e.g. "main"), so an
// empty unborn HEAD is NOT mistaken for the detached case; only a
// genuinely detached checkout (`git checkout <sha>`) produces empty
// stdout.
func (c *Client) CurrentBranch(ctx context.Context) (string, error) {
	out, err := c.run(ctx, currentBranchArgs())
	if err != nil {
		return "", err
	}
	branch := trimTrailingNewline(string(out))
	if branch == "" {
		return "", ErrDetachedHEAD
	}
	return branch, nil
}

// RemoteURL runs `config --get remote.<remote>.url` (remoteURLArgs) -- a
// RAW config read, deliberately not `remote get-url`, which would
// additionally expand insteadOf rewrites (see the doc note on
// Locator.RemoteURL in interfaces.go). Verified behaviorally against real
// git: `config --get` exits 1 with empty stdout when the key is not
// configured at all; that specific exit code is mapped to ErrNoRemote
// here rather than in classify (see classify.go's doc comment -- this is
// one of the two sentinels classify deliberately does not know about).
// Any other non-zero exit is a genuine error and propagates unmodified as
// a *GitError.
func (c *Client) RemoteURL(ctx context.Context, remote string) (string, error) {
	out, err := c.run(ctx, remoteURLArgs(remote))
	if err != nil {
		if isExitCode(err, 1) {
			return "", ErrNoRemote
		}
		return "", err
	}
	return trimTrailingNewline(string(out)), nil
}

// --- RefReader ---

// RefExists runs `rev-parse --verify --quiet <ref>^{commit}`
// (refExistsArgs). Verified behaviorally against real git: exit 0 means
// the ref resolves to a commit; exit 1 (with --quiet suppressing git's
// own error message on the ordinary "doesn't exist" case -- though a
// same-exit-code "wrong object type" mismatch can still print to stderr
// despite --quiet) means it does not; any other exit code is a genuine
// error and propagates as a *GitError.
func (c *Client) RefExists(ctx context.Context, ref string) (bool, error) {
	_, err := c.run(ctx, refExistsArgs(ref))
	if err == nil {
		return true, nil
	}
	if isExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

// HasUpstream runs `rev-parse @{u}` (hasUpstreamArgs).
//
// This is a DELIBERATE, DOCUMENTED design tradeoff, not an unexplained
// inconsistency with RefExists/IsTracked (each of which keys on one
// specific, genuinely distinct exit code): re-verified behaviorally
// against real git 2.54.0 (pg2-i0q71) across every failure mode this
// specific invocation can hit --
//
//   - no upstream configured at all ("fatal: no upstream configured for
//     branch '<name>'")
//   - an unborn HEAD ("fatal: no such branch: '<name>'")
//   - a detached HEAD ("fatal: HEAD does not point to a branch")
//   - an upstream naming a remote that was never configured, i.e. "not
//     stored as a remote-tracking branch" ("fatal: upstream branch
//     '<ref>' not stored as a remote-tracking branch")
//   - a dangling upstream config -- branch.<name>.merge pointing at a
//     ref with no corresponding remote-tracking ref, e.g. because the
//     remote branch was deleted after tracking was set up ("fatal:
//     ambiguous argument '@{u}': unknown revision or path not in the
//     working tree.")
//
// -- every one of these exits 128 with a different "fatal: ..." message.
// Unlike `rev-parse --verify --quiet <ref>^{commit}` (RefExists), there
// is no `--quiet`/`--verify` form of upstream ("@{u}") expansion: any
// resolution failure is always git's generic fatal-error exit, so exit
// code alone cannot separate "no upstream" from a hypothetical future
// fatal condition (e.g. ref-store corruption) hitting the same
// expansion. Keying on stderr text instead would just trade one
// fragility for another: classify.go's own doc comment already commits
// this package to never matching localized/version-specific stderr text,
// and the four DISTINCT messages above show there is no single stable
// substring that would even cover just the "no upstream" cases without
// also matching messages from unrelated causes. So, deliberately: any
// ordinary git failure (a *GitError, any exit code) here is reported as
// false ("no upstream"); only a non-git failure -- context
// cancellation/deadline, or a spawn error, per run's own contract --
// propagates as an error.
func (c *Client) HasUpstream(ctx context.Context) (bool, error) {
	_, err := c.run(ctx, hasUpstreamArgs())
	if err == nil {
		return true, nil
	}
	var gitErr *GitError
	if errors.As(err, &gitErr) {
		return false, nil
	}
	return false, err
}

// CommitsAhead runs `rev-list --count <base>..<tip>` (commitsAheadArgs)
// and parses the integer count from stdout (parseCount).
func (c *Client) CommitsAhead(ctx context.Context, base, tip string) (int, error) {
	out, err := c.run(ctx, commitsAheadArgs(base, tip))
	if err != nil {
		return 0, err
	}
	return parseCount(out)
}

// --- StatusReader ---

// Status runs `status --porcelain=v1 -z` (statusArgs) and parses the
// NUL-delimited output (parseStatus), including the reversed-order
// rename/copy record (new path, then original path).
func (c *Client) Status(ctx context.Context) ([]StatusEntry, error) {
	out, err := c.run(ctx, statusArgs())
	if err != nil {
		return nil, err
	}
	return parseStatus(out)
}

// IsTracked runs `ls-files --error-unmatch <path>` (isTrackedArgs).
// Verified behaviorally against real git: exit 0 means the path is
// tracked; exit 1 ("pathspec ... did not match any file(s) known to
// git") means it is not; any other exit code (e.g. 128 for a path
// outside the repository) is a genuine error and propagates as a
// *GitError.
func (c *Client) IsTracked(ctx context.Context, path string) (bool, error) {
	_, err := c.run(ctx, isTrackedArgs(path))
	if err == nil {
		return true, nil
	}
	if isExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

// --- HistoryReader ---

// Commits runs `log -z --format=<logFormat>` (logArgs) built from opts
// and parses the NUL-delimited output (parseCommits). opts.Limit MUST be
// >= 0: only 0 is the documented "unlimited" sentinel (LogOptions' own
// doc comment), and logArgs' `if opts.Limit > 0` guard means a negative
// value would otherwise fall through silently treated as unlimited
// rather than rejected -- so it is rejected here instead.
func (c *Client) Commits(ctx context.Context, opts LogOptions) ([]Commit, error) {
	if opts.Limit < 0 {
		return nil, fmt.Errorf("gitclient: LogOptions.Limit must be >= 0 (0 means unlimited), got %d", opts.Limit)
	}
	out, err := c.run(ctx, logArgs(opts))
	if err != nil {
		return nil, err
	}
	return parseCommits(out)
}

// ChangedFiles runs `diff --numstat <base>...HEAD` (numstatArgs --
// merge-base ("...") semantics per the interface doc comment) and parses
// the tab-delimited output (parseNumstat).
func (c *Client) ChangedFiles(ctx context.Context, base string) ([]FileChange, error) {
	out, err := c.run(ctx, numstatArgs(base))
	if err != nil {
		return nil, err
	}
	return parseNumstat(out)
}
