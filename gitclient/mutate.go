package gitclient

import "context"

// Compile-time assertions that Client satisfies the four mutating role
// interfaces this file implements. The read-side roles' assertions
// (Locator, RefReader, StatusReader, HistoryReader) land with their own
// method implementations in bead pg2-svfbb.4.
var (
	_ Fetcher         = (*Client)(nil)
	_ WorktreeManager = (*Client)(nil)
	_ BranchManager   = (*Client)(nil)
	_ Cleaner         = (*Client)(nil)
)

// Fetch runs `git fetch` (fetchArgs): opts.Remote (default "origin"),
// optionally opts.Refspec, with opts.AllowPrune governing --no-prune (design
// §4.1/§4.4 -- the zero value is the safe default, so a host-level
// fetch.prune config cannot silently delete refs this client did not ask to
// prune).
func (c *Client) Fetch(ctx context.Context, opts FetchOptions) error {
	_, err := c.run(ctx, fetchArgs(opts))
	return err
}

// CreateWorktree runs `git worktree add` (worktreeAddArgs): -b (create
// only, the default) or -B (create-or-reset, when opts.ResetBranch is set --
// pr-pool's redispatch case) at opts.StartPoint if given.
func (c *Client) CreateWorktree(ctx context.Context, path, branch string, opts CreateWorktreeOptions) error {
	_, err := c.run(ctx, worktreeAddArgs(path, branch, opts))
	return err
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
