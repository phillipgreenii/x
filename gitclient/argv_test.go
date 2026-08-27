package gitclient

import (
	"reflect"
	"slices"
	"testing"
)

func TestToplevelArgs(t *testing.T) {
	v := toplevelArgs()
	want := []string{"rev-parse", "--show-toplevel"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("toplevelArgs().Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("toplevelArgs().Parsed = false, want true")
	}
}

func TestCommonDirArgs(t *testing.T) {
	v := commonDirArgs()
	want := []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("commonDirArgs().Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("commonDirArgs().Parsed = false, want true")
	}
}

func TestCurrentBranchArgs(t *testing.T) {
	v := currentBranchArgs()
	want := []string{"branch", "--show-current"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("currentBranchArgs().Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("currentBranchArgs().Parsed = false, want true (branch --show-current output is parsed)")
	}
}

func TestRemoteURLArgs(t *testing.T) {
	v := remoteURLArgs("origin")
	want := []string{"config", "--get", "remote.origin.url"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("remoteURLArgs(\"origin\").Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("remoteURLArgs().Parsed = false, want true (config --get output is parsed)")
	}
}

func TestRefExistsArgs(t *testing.T) {
	v := refExistsArgs("main")
	want := []string{"rev-parse", "--verify", "--quiet", "main^{commit}"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("refExistsArgs(\"main\").Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("refExistsArgs().Parsed = false, want true")
	}
}

func TestHasUpstreamArgs(t *testing.T) {
	v := hasUpstreamArgs()
	want := []string{"rev-parse", "@{u}"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("hasUpstreamArgs().Args = %v, want %v", v.Args, want)
	}
	if !v.Parsed {
		t.Error("hasUpstreamArgs().Parsed = false, want true")
	}
}

func TestFetchArgsNoPruneIsTheSafeDefault(t *testing.T) {
	v := fetchArgs(FetchOptions{})
	want := []string{"fetch", "--no-prune", "origin"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("fetchArgs(FetchOptions{}).Args = %v, want %v", v.Args, want)
	}
	if v.Parsed {
		t.Error("fetchArgs().Parsed = true, want false (fetch is a mutation, not LC_ALL-scoped)")
	}
}

func TestFetchArgsAllowPruneOmitsNoPrune(t *testing.T) {
	v := fetchArgs(FetchOptions{AllowPrune: true})
	want := []string{"fetch", "origin"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("fetchArgs(AllowPrune: true).Args = %v, want %v", v.Args, want)
	}
}

func TestFetchArgsRemoteAndRefspec(t *testing.T) {
	v := fetchArgs(FetchOptions{
		Remote:  "upstream",
		Refspec: "+refs/pull/12/head:refs/remotes/origin/pr/12",
	})
	want := []string{"fetch", "--no-prune", "upstream", "+refs/pull/12/head:refs/remotes/origin/pr/12"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("fetchArgs().Args = %v, want %v", v.Args, want)
	}
}

func TestWorktreeAddArgsDefaultUsesLowercaseB(t *testing.T) {
	v := worktreeAddArgs("/path/to/wt", "feature", CreateWorktreeOptions{})
	want := []string{"worktree", "add", "-b", "feature", "/path/to/wt"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("worktreeAddArgs().Args = %v, want %v", v.Args, want)
	}
	if v.Parsed {
		t.Error("worktreeAddArgs().Parsed = true, want false (worktree add runs hooks; must not get LC_ALL=C)")
	}
}

func TestWorktreeAddArgsResetBranchUsesUppercaseB(t *testing.T) {
	v := worktreeAddArgs("/path/to/wt", "feature", CreateWorktreeOptions{ResetBranch: true})
	want := []string{"worktree", "add", "-B", "feature", "/path/to/wt"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("worktreeAddArgs(ResetBranch: true).Args = %v, want %v", v.Args, want)
	}
}

func TestWorktreeAddArgsWithStartPoint(t *testing.T) {
	v := worktreeAddArgs("/path/to/wt", "feature", CreateWorktreeOptions{StartPoint: "origin/main"})
	want := []string{"worktree", "add", "-b", "feature", "/path/to/wt", "origin/main"}
	if !reflect.DeepEqual(v.Args, want) {
		t.Errorf("worktreeAddArgs(StartPoint).Args = %v, want %v", v.Args, want)
	}
}

func TestWorktreeRemoveArgs(t *testing.T) {
	if v := worktreeRemoveArgs("/path", false); !reflect.DeepEqual(v.Args, []string{"worktree", "remove", "/path"}) {
		t.Errorf("worktreeRemoveArgs(false).Args = %v", v.Args)
	} else if v.Parsed {
		t.Error("worktreeRemoveArgs().Parsed = true, want false")
	}
	if v := worktreeRemoveArgs("/path", true); !reflect.DeepEqual(v.Args, []string{"worktree", "remove", "--force", "/path"}) {
		t.Errorf("worktreeRemoveArgs(true).Args = %v", v.Args)
	}
}

func TestWorktreePruneArgs(t *testing.T) {
	v := worktreePruneArgs()
	if !reflect.DeepEqual(v.Args, []string{"worktree", "prune"}) {
		t.Errorf("worktreePruneArgs().Args = %v", v.Args)
	}
	if v.Parsed {
		t.Error("worktreePruneArgs().Parsed = true, want false")
	}
}

func TestBranchDeleteArgs(t *testing.T) {
	if v := branchDeleteArgs("stale", false); !reflect.DeepEqual(v.Args, []string{"branch", "-d", "stale"}) {
		t.Errorf("branchDeleteArgs(false).Args = %v", v.Args)
	} else if v.Parsed {
		t.Error("branchDeleteArgs().Parsed = true, want false (a mutation, distinct from the parsed `branch --show-current`)")
	}
	if v := branchDeleteArgs("stale", true); !reflect.DeepEqual(v.Args, []string{"branch", "-D", "stale"}) {
		t.Errorf("branchDeleteArgs(true).Args = %v", v.Args)
	}
}

func TestResetHardArgs(t *testing.T) {
	v := resetHardArgs()
	if !reflect.DeepEqual(v.Args, []string{"reset", "--hard"}) {
		t.Errorf("resetHardArgs().Args = %v", v.Args)
	}
	if v.Parsed {
		t.Error("resetHardArgs().Parsed = true, want false")
	}
}

func TestCleanUntrackedArgs(t *testing.T) {
	v := cleanUntrackedArgs()
	if !reflect.DeepEqual(v.Args, []string{"clean", "-fd"}) {
		t.Errorf("cleanUntrackedArgs().Args = %v", v.Args)
	}
	if v.Parsed {
		t.Error("cleanUntrackedArgs().Parsed = true, want false")
	}
}

// TestLCAllScopingInEnvConstruction is the design's normative LC_ALL=C
// scoping list (§4.4), exercised end to end: for every verbArgs builder
// this bead implements, buildEnv(cfg, v.Parsed) must include LC_ALL=C
// exactly when the builder is one of the parsed commands (status, log,
// diff, rev-parse, rev-list, `branch --show-current`, ls-files,
// config --get), and must NOT include it for any worktree-add-family or
// other mutating command.
func TestLCAllScopingInEnvConstruction(t *testing.T) {
	cfg := &config{}

	parsed := map[string]verbArgs{
		"status (StatusReader.Status)":                   statusArgs(),
		"log (HistoryReader.Commits)":                    logArgs(LogOptions{}),
		"diff (HistoryReader.ChangedFiles)":              numstatArgs("main"),
		"rev-parse --show-toplevel (Locator.Toplevel)":   toplevelArgs(),
		"rev-parse --git-common-dir (Locator.CommonDir)": commonDirArgs(),
		"rev-parse --verify (RefReader.RefExists)":       refExistsArgs("main"),
		"rev-parse @{u} (RefReader.HasUpstream)":         hasUpstreamArgs(),
		"rev-list --count (RefReader.CommitsAhead)":      commitsAheadArgs("main", "feature"),
		"branch --show-current (Locator.CurrentBranch)":  currentBranchArgs(),
		"ls-files (StatusReader.IsTracked)":              isTrackedArgs("some/path"),
		"config --get (Locator.RemoteURL)":               remoteURLArgs("origin"),
	}
	for name, v := range parsed {
		if !v.Parsed {
			t.Errorf("%s: verbArgs.Parsed = false, want true", name)
			continue
		}
		env := buildEnv(cfg, v.Parsed)
		if !slices.Contains(env, "LC_ALL=C") {
			t.Errorf("%s: buildEnv output %v does not contain LC_ALL=C", name, env)
		}
	}

	notParsed := map[string]verbArgs{
		"fetch (Fetcher.Fetch)":                                  fetchArgs(FetchOptions{}),
		"worktree add -b (WorktreeManager.CreateWorktree)":       worktreeAddArgs("/wt", "feature", CreateWorktreeOptions{}),
		"worktree add -B (WorktreeManager.CreateWorktree reset)": worktreeAddArgs("/wt", "feature", CreateWorktreeOptions{ResetBranch: true}),
		"worktree remove (WorktreeManager.RemoveWorktree)":       worktreeRemoveArgs("/wt", true),
		"worktree prune (WorktreeManager.PruneWorktrees)":        worktreePruneArgs(),
		"branch -d (BranchManager.DeleteBranch)":                 branchDeleteArgs("stale", false),
		"reset --hard (Cleaner.ResetHard)":                       resetHardArgs(),
		"clean -fd (Cleaner.CleanUntracked)":                     cleanUntrackedArgs(),
	}
	for name, v := range notParsed {
		if v.Parsed {
			t.Errorf("%s: verbArgs.Parsed = true, want false", name)
			continue
		}
		env := buildEnv(cfg, v.Parsed)
		if slices.Contains(env, "LC_ALL=C") {
			t.Errorf("%s: buildEnv output %v must not contain LC_ALL=C (hook-running mutation)", name, env)
		}
	}
}
