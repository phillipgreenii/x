package gitclient

import "context"

// Each method's trailing comment names the plumbing it wraps. The set is
// derived from a workspace-wide call-site audit (epic pg2-67h4y design
// field) plus a review pass over pr-pool's internal/worktree and executor
// wiring: every production call site lands on a role, and no method exists
// without a current consumer -- notably there is NO standalone
// CreateBranch, since every branch creation in production Go today is
// `worktree add -b`/`-B`, which CreateWorktree models.

// Locator answers where a repository is and how it is identified.
type Locator interface {
	Toplevel(ctx context.Context) (string, error)                 // rev-parse --show-toplevel
	CommonDir(ctx context.Context) (string, error)                // rev-parse --path-format=absolute --git-common-dir
	CurrentBranch(ctx context.Context) (string, error)            // branch --show-current; ErrDetachedHEAD when detached; works on unborn HEAD
	RemoteURL(ctx context.Context, remote string) (string, error) // config --get remote.<remote>.url (raw; NO insteadOf expansion -- see doc note below); ErrNoRemote when unconfigured
}

// Doc note on RemoteURL: the raw-config read is deliberate, not an
// oversight -- two of three migrating consumers (pa-monitor repo.go, pg-pr
// worktree/git.go) read `config --get remote.origin.url` today, and
// pa-monitor derives stable workspace.repo labels from it. Swapping to
// `remote get-url` would additionally expand insteadOf rewrites and change
// those labels -- a silent behavior change a migration bead must absorb
// deliberately, not something to "fix" here.

// RefReader answers questions about refs and reachability.
type RefReader interface {
	RefExists(ctx context.Context, ref string) (bool, error)         // rev-parse --verify --quiet <ref>^{commit}
	HasUpstream(ctx context.Context) (bool, error)                   // rev-parse @{u}
	CommitsAhead(ctx context.Context, base, tip string) (int, error) // rev-list --count base..tip
}

// StatusReader reports working-tree and index state.
type StatusReader interface {
	Status(ctx context.Context) ([]StatusEntry, error)        // status --porcelain=v1 -z; clean iff empty
	IsTracked(ctx context.Context, path string) (bool, error) // ls-files --error-unmatch <path>
}

// HistoryReader reads commit history and diff summaries.
type HistoryReader interface {
	Commits(ctx context.Context, opts LogOptions) ([]Commit, error)
	ChangedFiles(ctx context.Context, base string) ([]FileChange, error) // diff --numstat base...HEAD (merge-base semantics); base required
}

// Fetcher updates remote-tracking state. Streaming (design pg2-migib §3):
// a fetch against a slow remote is exactly the kind of call pn's UX wants
// to watch live, so this returns a *Handle rather than blocking. The nine
// already-migrated leaf-app consumers keep calling it buffered -- `h, err
// := Fetch(...); if err == nil { err = h.Wait() }` -- Handle's own doc
// comment covers why that is safe even though this bead does not migrate
// those call sites itself.
type Fetcher interface {
	Fetch(ctx context.Context, opts FetchOptions) (*Handle, error)
}

// WorktreeManager manages linked worktrees of this client's repository.
// CreateWorktree streams for the same reason Fetch does; RemoveWorktree
// and PruneWorktrees are UNCHANGED (no streaming need -- neither runs
// long enough for live progress to matter to any current or anticipated
// caller).
type WorktreeManager interface {
	CreateWorktree(ctx context.Context, path, branch string, opts CreateWorktreeOptions) (*Handle, error) // worktree add [-b|-B]
	RemoveWorktree(ctx context.Context, path string, force bool) error
	PruneWorktrees(ctx context.Context) error
}

// BranchManager mutates local branches.
type BranchManager interface {
	DeleteBranch(ctx context.Context, branch string, force bool) error // branch -d | -D
}

// Cleaner restores a working tree to HEAD.
type Cleaner interface {
	ResetHard(ctx context.Context) error      // reset --hard
	CleanUntracked(ctx context.Context) error // clean -fd
}

// Syncer brings a branch up to date with its upstream -- pn's own
// rebase.go today runs `fetch` + `pull --rebase --autostash` (no Onto) or
// `rebase --autostash <onto>` (Onto set); SyncOptions.Onto selects between
// the two. Streaming: both shapes can run long (a real rebase, a slow
// remote) and pn's UX watches progress live -- born streaming-shaped, no
// buffered twin needed (bead pg2-f1cq7).
//
// Sync does not classify a conflict as a distinct sentinel (e.g. a future
// ErrRebaseConflict): `git rebase`/`pull --rebase` exit non-zero for a
// conflict the same way they do for several unrelated failure modes, and
// telling them apart needs a second role composition (a post-failure
// StatusReader probe for unmerged entries) that no current caller of this
// bead exercises. A conflict therefore surfaces through Wait() as an
// ordinary *GitError, inspectable via errors.As like any other mutation's
// failure -- a deliberately narrower scope than a future consumer might
// eventually need, not an oversight (design doc pg2-migib §3/§8 discusses
// the fuller machinery this intentionally defers).
type Syncer interface {
	Sync(ctx context.Context, opts SyncOptions) (*Handle, error)
}

// Committer stages and commits changes in the working tree (pn's
// propagate.go: `checkout -- <path>`, `add`, `commit -m`). RestorePath and
// Add have no streaming need -- neither runs a hook synchronously in the
// way Commit's `commit -m` does (commit-msg/post-commit), so only Commit
// returns a *Handle.
//
// Committer deliberately carries NO hook-bypass mechanism (no
// PREK_ALLOW_NO_CONFIG-shaped option, here or anywhere else in this
// package) -- that is a pn-side root-cause fix tracked separately, not
// something this client's hermetic-by-construction contract should grow
// a knob for.
type Committer interface {
	RestorePath(ctx context.Context, path string) error          // checkout -- <path>
	Add(ctx context.Context, paths ...string) error              // add -- <paths...>
	Commit(ctx context.Context, message string) (*Handle, error) // commit -m <message>
}

// Pusher publishes local commits to a remote (pn's push.go: `push` or
// `push -u <remote> <branch>`, optionally `--no-verify`). Streaming: a
// push can be slow (large history, a slow remote) and pn's UX watches it
// live.
//
// Remote resolution is deliberately NOT this role's job: push.go's
// resolvePushRemote is a 7-step config-reading fallback chain with no
// direct Locator equivalent, and the bead that scoped this interface left
// open whether to fold that chain into gitclient or keep it in pn. This
// implementation keeps it in pn (the simpler of the two options) --
// PushOptions.Remote/Branch are the ALREADY-RESOLVED values a caller
// supplies; Push itself only runs the `push` invocation.
type Pusher interface {
	Push(ctx context.Context, opts PushOptions) (*Handle, error)
}

// BranchLister enumerates local branches -- the read-side role behind
// pn's own workspace/status.go localBranches gap (design doc pg2-migib
// §2: no BranchManager method covers `branch --format=...` today). No
// streaming concept applies; it is a single parsed read, exactly like
// Locator/RefReader/StatusReader.
type BranchLister interface {
	ListBranches(ctx context.Context) ([]string, error) // branch --format=%(refname:short)
}

// RemoteManager configures a repository's remotes. AddRemote is Clone's
// own companion for a multi-remote CloneOptions configuration -- pn's
// clone.go registers every non-origin remote this way immediately after
// the initial clone. No streaming need (a config-only mutation).
type RemoteManager interface {
	AddRemote(ctx context.Context, name, url string) error // remote add -- <name> <url>
}
