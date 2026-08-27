package gitclient

import (
	"strconv"
	"time"
)

// verbArgs pairs one git invocation's argv with whether its output is
// parsed by this client -- the property that governs LC_ALL=C scoping
// (buildEnv). It is a pure value with no behavior; the forthcoming Client
// (pg2-svfbb.2) spawns Args and builds the child env for that invocation
// via buildEnv(cfg, Parsed).
//
// Parsed is true only for the commands the environment contract (design
// §4.4) names: status, log, diff, rev-parse, rev-list,
// `branch --show-current`, ls-files, config --get. It is false for every
// hook-running mutation (worktree add/remove/prune, branch -d/-D, fetch,
// reset --hard, clean -fd) -- LC_ALL=C MUST NOT leak into a child that
// might run hooks or into the Run escape hatch.
type verbArgs struct {
	Args   []string
	Parsed bool
}

// --- Locator ---

// toplevelArgs builds `rev-parse --show-toplevel` (Locator.Toplevel).
func toplevelArgs() verbArgs {
	return verbArgs{Args: []string{"rev-parse", "--show-toplevel"}, Parsed: true}
}

// commonDirArgs builds `rev-parse --path-format=absolute --git-common-dir`
// (Locator.CommonDir).
func commonDirArgs() verbArgs {
	return verbArgs{Args: []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, Parsed: true}
}

// currentBranchArgs builds `branch --show-current` (Locator.CurrentBranch).
func currentBranchArgs() verbArgs {
	return verbArgs{Args: []string{"branch", "--show-current"}, Parsed: true}
}

// remoteURLArgs builds `config --get remote.<remote>.url`
// (Locator.RemoteURL).
func remoteURLArgs(remote string) verbArgs {
	return verbArgs{Args: []string{"config", "--get", "remote." + remote + ".url"}, Parsed: true}
}

// --- RefReader ---

// refExistsArgs builds `rev-parse --verify --quiet <ref>^{commit}`
// (RefReader.RefExists).
func refExistsArgs(ref string) verbArgs {
	return verbArgs{Args: []string{"rev-parse", "--verify", "--quiet", ref + "^{commit}"}, Parsed: true}
}

// hasUpstreamArgs builds `rev-parse @{u}` (RefReader.HasUpstream).
func hasUpstreamArgs() verbArgs {
	return verbArgs{Args: []string{"rev-parse", "@{u}"}, Parsed: true}
}

// commitsAheadArgs builds `rev-list --count <base>..<tip>`
// (RefReader.CommitsAhead).
func commitsAheadArgs(base, tip string) verbArgs {
	return verbArgs{Args: []string{"rev-list", "--count", base + ".." + tip}, Parsed: true}
}

// --- StatusReader ---

// statusArgs builds `status --porcelain=v1 -z` (StatusReader.Status).
func statusArgs() verbArgs {
	return verbArgs{Args: []string{"status", "--porcelain=v1", "-z"}, Parsed: true}
}

// isTrackedArgs builds `ls-files --error-unmatch <path>`
// (StatusReader.IsTracked).
func isTrackedArgs(path string) verbArgs {
	return verbArgs{Args: []string{"ls-files", "--error-unmatch", path}, Parsed: true}
}

// --- HistoryReader ---

// logArgs builds a NUL-delimited `log -z --format=<logFormat>` invocation
// (HistoryReader.Commits) plus opts' filters. -z makes git NUL-terminate
// each commit's formatted expansion instead of newline-terminating it, so
// combined with logFormat's own NUL field separators the whole stdout is
// one flat NUL-delimited stream (see parseCommits).
func logArgs(opts LogOptions) verbArgs {
	args := []string{"log", "-z", "--format=" + logFormat}
	if opts.NoMerges {
		args = append(args, "--no-merges")
	}
	if !opts.Since.IsZero() {
		args = append(args, "--since="+opts.Since.Format(time.RFC3339))
	}
	if !opts.Until.IsZero() {
		args = append(args, "--until="+opts.Until.Format(time.RFC3339))
	}
	for _, author := range opts.Authors {
		args = append(args, "--author="+author)
	}
	if opts.Limit > 0 {
		args = append(args, "-n", strconv.Itoa(opts.Limit))
	}
	head := opts.Head
	if head == "" {
		head = "HEAD"
	}
	if opts.Base != "" {
		args = append(args, opts.Base+".."+head)
	} else {
		args = append(args, head)
	}
	if len(opts.Paths) > 0 {
		args = append(args, "--")
		args = append(args, opts.Paths...)
	}
	return verbArgs{Args: args, Parsed: true}
}

// numstatArgs builds `diff --numstat <base>...HEAD`
// (HistoryReader.ChangedFiles) -- merge-base ("...") semantics per the
// interface doc comment.
func numstatArgs(base string) verbArgs {
	return verbArgs{Args: []string{"diff", "--numstat", base + "...HEAD"}, Parsed: true}
}

// --- Fetcher ---

// fetchArgs builds `fetch [--no-prune] <remote> [<refspec>]`
// (Fetcher.Fetch). AllowPrune false (the zero value, and the safe default)
// passes --no-prune; true omits it and leaves fetch.prune governing.
func fetchArgs(opts FetchOptions) verbArgs {
	remote := opts.Remote
	if remote == "" {
		remote = "origin"
	}
	args := []string{"fetch"}
	if !opts.AllowPrune {
		args = append(args, "--no-prune")
	}
	args = append(args, remote)
	if opts.Refspec != "" {
		args = append(args, opts.Refspec)
	}
	return verbArgs{Args: args, Parsed: false}
}

// --- WorktreeManager ---

// worktreeAddArgs builds `worktree add [-b|-B] <branch> <path>
// [<start-point>]` (WorktreeManager.CreateWorktree). ResetBranch selects -B
// (create the branch, or reset it if it already exists -- pr-pool's
// redispatch case) over the default -b (create only, fails if the branch
// already exists).
func worktreeAddArgs(path, branch string, opts CreateWorktreeOptions) verbArgs {
	flag := "-b"
	if opts.ResetBranch {
		flag = "-B"
	}
	args := []string{"worktree", "add", flag, branch, path}
	if opts.StartPoint != "" {
		args = append(args, opts.StartPoint)
	}
	return verbArgs{Args: args, Parsed: false}
}

// worktreeRemoveArgs builds `worktree remove [--force] <path>`
// (WorktreeManager.RemoveWorktree).
func worktreeRemoveArgs(path string, force bool) verbArgs {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, path)
	return verbArgs{Args: args, Parsed: false}
}

// worktreePruneArgs builds `worktree prune`
// (WorktreeManager.PruneWorktrees).
func worktreePruneArgs() verbArgs {
	return verbArgs{Args: []string{"worktree", "prune"}, Parsed: false}
}

// --- BranchManager ---

// branchDeleteArgs builds `branch [-d|-D] <branch>`
// (BranchManager.DeleteBranch).
func branchDeleteArgs(branch string, force bool) verbArgs {
	flag := "-d"
	if force {
		flag = "-D"
	}
	return verbArgs{Args: []string{"branch", flag, branch}, Parsed: false}
}

// --- Cleaner ---

// resetHardArgs builds `reset --hard` (Cleaner.ResetHard).
func resetHardArgs() verbArgs {
	return verbArgs{Args: []string{"reset", "--hard"}, Parsed: false}
}

// cleanUntrackedArgs builds `clean -fd` (Cleaner.CleanUntracked).
func cleanUntrackedArgs() verbArgs {
	return verbArgs{Args: []string{"clean", "-fd"}, Parsed: false}
}
