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

// Fetcher updates remote-tracking state.
type Fetcher interface {
	Fetch(ctx context.Context, opts FetchOptions) error
}

// WorktreeManager manages linked worktrees of this client's repository.
type WorktreeManager interface {
	CreateWorktree(ctx context.Context, path, branch string, opts CreateWorktreeOptions) error // worktree add [-b|-B]
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
