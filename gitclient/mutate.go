package gitclient

import (
	"context"
	"fmt"
)

// Compile-time assertions that Client satisfies the mutating role
// interfaces this file implements. The read-side roles' assertions
// (Locator, RefReader, StatusReader, HistoryReader, BranchLister) live
// with their own method implementations in read.go (bead pg2-svfbb.4 /
// pg2-f1cq7).
var (
	_ Fetcher         = (*Client)(nil)
	_ WorktreeManager = (*Client)(nil)
	_ BranchManager   = (*Client)(nil)
	_ Cleaner         = (*Client)(nil)
	_ Syncer          = (*Client)(nil)
	_ Committer       = (*Client)(nil)
	_ Pusher          = (*Client)(nil)
	_ RemoteManager   = (*Client)(nil)
)

// Fetch runs `git fetch` (fetchArgs): opts.Remote (default "origin"),
// optionally opts.Refspec, with opts.AllowPrune governing --no-prune (design
// §4.1/§4.4 -- the zero value is the safe default, so a host-level
// fetch.prune config cannot silently delete refs this client did not ask to
// prune). Streams via startHandle (bead pg2-f1cq7): the returned Handle's
// process is already running by the time this call returns.
func (c *Client) Fetch(ctx context.Context, opts FetchOptions) (*Handle, error) {
	return c.startHandle(ctx, fetchArgs(opts))
}

// CreateWorktree runs `git worktree add` (worktreeAddArgs): -b (create
// only, the default) or -B (create-or-reset, when opts.ResetBranch is set --
// pr-pool's redispatch case) at opts.StartPoint if given. Streams via
// startHandle (bead pg2-f1cq7).
func (c *Client) CreateWorktree(ctx context.Context, path, branch string, opts CreateWorktreeOptions) (*Handle, error) {
	return c.startHandle(ctx, worktreeAddArgs(path, branch, opts))
}

// RemoveWorktree runs `git worktree remove [--force]` (worktreeRemoveArgs).
// Without force, git itself refuses to remove a worktree that is dirty
// (uncommitted changes); that failure is not swallowed here -- like any
// other non-zero exit it flows through run/classify, so callers observe it
// as a *GitError via errors.As.
func (c *Client) RemoveWorktree(ctx context.Context, path string, force bool) error {
	_, err := c.run(ctx, worktreeRemoveArgs(path, force))
	return err
}

// PruneWorktrees runs `git worktree prune` (worktreePruneArgs).
func (c *Client) PruneWorktrees(ctx context.Context) error {
	_, err := c.run(ctx, worktreePruneArgs())
	return err
}

// DeleteBranch runs `git branch -d|-D` (branchDeleteArgs). Without force,
// git itself refuses to delete a branch that is not fully merged into its
// upstream/HEAD; that failure flows through unmodified, as a *GitError.
func (c *Client) DeleteBranch(ctx context.Context, branch string, force bool) error {
	_, err := c.run(ctx, branchDeleteArgs(branch, force))
	return err
}

// ResetHard runs `git reset --hard` (resetHardArgs).
func (c *Client) ResetHard(ctx context.Context) error {
	_, err := c.run(ctx, resetHardArgs())
	return err
}

// CleanUntracked runs `git clean -fd` (cleanUntrackedArgs).
func (c *Client) CleanUntracked(ctx context.Context) error {
	_, err := c.run(ctx, cleanUntrackedArgs())
	return err
}

// Sync runs either `rebase --autostash <onto>` (opts.Onto set) or `pull
// --rebase --autostash` (opts.Onto empty) -- syncArgs. Streams via
// startHandle; see Syncer's doc comment (interfaces.go) for why a
// conflict surfaces as an ordinary *GitError rather than a dedicated
// sentinel.
func (c *Client) Sync(ctx context.Context, opts SyncOptions) (*Handle, error) {
	return c.startHandle(ctx, syncArgs(opts))
}

// RestorePath runs `checkout -- <path>` (restorePathArgs).
func (c *Client) RestorePath(ctx context.Context, path string) error {
	_, err := c.run(ctx, restorePathArgs(path))
	return err
}

// Add runs `add -- <paths...>` (addArgs).
func (c *Client) Add(ctx context.Context, paths ...string) error {
	_, err := c.run(ctx, addArgs(paths...))
	return err
}

// Commit runs `commit -m <message>` (commitArgs). Streams via
// startHandle: commit-msg/post-commit hooks run synchronously as part of
// this invocation, and pn's UX watches their output live.
func (c *Client) Commit(ctx context.Context, message string) (*Handle, error) {
	return c.startHandle(ctx, commitArgs(message))
}

// Push runs `push [--no-verify]` or, when opts.SetUpstream is set, `push
// [--no-verify] -u <remote> <branch>` (pushArgs). opts.Remote and
// opts.Branch are REQUIRED when SetUpstream is true (PushOptions' own doc
// comment) -- rejected here with a clear error rather than handed to git
// as empty positional arguments, which would fail confusingly.
func (c *Client) Push(ctx context.Context, opts PushOptions) (*Handle, error) {
	if opts.SetUpstream && (opts.Remote == "" || opts.Branch == "") {
		return nil, fmt.Errorf("gitclient: PushOptions.SetUpstream requires both Remote and Branch")
	}
	return c.startHandle(ctx, pushArgs(opts))
}

// AddRemote runs `remote add -- <name> <url>` (addRemoteArgs) -- Clone's
// companion for a multi-remote CloneOptions configuration.
func (c *Client) AddRemote(ctx context.Context, name, url string) error {
	_, err := c.run(ctx, addRemoteArgs(name, url))
	return err
}
