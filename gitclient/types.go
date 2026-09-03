// Package gitclient defines role-segregated interfaces for interacting with
// a git repository (Locator, RefReader, StatusReader, HistoryReader,
// Fetcher, WorktreeManager, BranchManager, Cleaner) plus the pure
// construction seams behind them: per-verb argv builders, the
// environment-allowlist builder and functional Options, and the classify
// exit-code-to-error seam.
//
// It also ships Client (client.go, bead pg2-svfbb.2): the anchored,
// hermetic CLI-backed spawn mechanics -- constructors New/Init/Discover
// and the Run escape hatch, plus the context-cancellation contract every
// git-spawning call honors. Client's mutating role-interface method
// implementations (Fetcher, WorktreeManager, BranchManager, Cleaner) and
// their compile-time interface-satisfaction assertions live in mutate.go
// (bead pg2-svfbb.5); the read-side roles (Locator, RefReader,
// StatusReader, HistoryReader) and their assertions live in read.go
// (bead pg2-svfbb.4).
//
// Design of record: the DESIGN field of epic bead pg2-svfbb (operator
// ruling 2026-08-27: the design lives in the bead, not committed to this
// repo).
package gitclient

import "time"

// Signature identifies an author or committer at a point in time.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// Commit is one commit as reported by `git log`.
type Commit struct {
	SHA       string
	Subject   string
	Body      string
	Author    Signature
	Committer Signature // Committer.When is the %ct recency pg-go-mutate-tui reads
}

// FileChange is one line of `git diff --numstat -z`. For a rename/copy
// (git detects these by default, no flag required -- see numstatArgs/
// parseNumstat), Path is the NEW path and OrigPath is the ORIGINAL path;
// OrigPath is set only in that case, mirroring StatusEntry.OrigPath.
type FileChange struct {
	Path      string
	OrigPath  string // set only for renames/copies
	Additions int
	Deletions int
	Binary    bool
}

// StatusCode is one column of a `git status --porcelain` XY pair.
type StatusCode byte

const (
	StatusUnmodified  StatusCode = ' '
	StatusModified    StatusCode = 'M'
	StatusTypeChanged StatusCode = 'T'
	StatusAdded       StatusCode = 'A'
	StatusDeleted     StatusCode = 'D'
	StatusRenamed     StatusCode = 'R'
	StatusCopied      StatusCode = 'C'
	StatusUnmerged    StatusCode = 'U'
	StatusUntracked   StatusCode = '?' // both columns: "??"
)

// NOTE: no StatusIgnored ('!') -- ignored entries only appear under
// --ignored, which Status does not pass and no consumer needs; add both
// together when one does.

// StatusEntry is one entry of `git status --porcelain=v1 -z`.
// In -z mode a rename/copy record carries TWO NUL-separated paths in
// REVERSED order (new, then original) -- the parser must encode that
// layout.
type StatusEntry struct {
	Staged   StatusCode
	Unstaged StatusCode
	Path     string
	OrigPath string // set only for renames/copies
}

// LogOptions selects commits for HistoryReader.Commits. All fields optional.
type LogOptions struct {
	Base     string    // exclude commits reachable from Base (Base..Head); empty = full history
	Head     string    // defaults to "HEAD"
	Since    time.Time // zero = unbounded
	Until    time.Time // zero = unbounded
	Authors  []string  // --author filters, OR-ed
	Paths    []string  // limit to commits touching these paths
	NoMerges bool
	Limit    int // 0 = unlimited
}

// FetchOptions configures Fetcher.Fetch. All fields optional; the ZERO VALUE
// is the safe value: pruning is suppressed (--no-prune) unless AllowPrune is
// set, so a host-level fetch.prune config cannot silently delete the
// pr/<n> refs pg-pr depends on.
type FetchOptions struct {
	Remote     string // defaults to "origin"
	Refspec    string // optional, e.g. "+refs/pull/12/head:refs/remotes/origin/pr/12" (pg-pr's case needs the force prefix for re-fetches)
	AllowPrune bool   // false (default): pass --no-prune; true: omit it (git config governs)
}

// CreateWorktreeOptions carries the OPTIONAL parts of CreateWorktree;
// the required path and branch are positional parameters.
type CreateWorktreeOptions struct {
	StartPoint  string // optional commit-ish
	ResetBranch bool   // use -B instead of -b: create OR reset the branch (pr-pool redispatch)
}

// SyncOptions configures Syncer.Sync. Onto's presence selects between the
// two shapes pn's own rebase.go models today (design doc pg2-migib §3):
// empty (the default) runs `pull --rebase --autostash` against the
// branch's configured upstream; a non-empty Onto instead runs `rebase
// --autostash <onto>` against that explicit local ref, with no fetch/pull.
type SyncOptions struct {
	Onto string
}

// PushOptions configures Pusher.Push. The zero value runs a plain `push`
// (the branch already has a configured upstream). SetUpstream selects
// `push -u <remote> <branch>` instead, for a branch with no upstream yet
// -- Remote and Branch are then REQUIRED: resolving which remote to
// publish to is left to the caller (see the design note on Pusher in
// interfaces.go), not derived here.
type PushOptions struct {
	SetUpstream bool
	Remote      string // required when SetUpstream is true
	Branch      string // required when SetUpstream is true
	NoVerify    bool   // pass --no-verify, skipping the repo's pre-push hook
}

// CloneOptions configures the Clone constructor. All fields optional.
type CloneOptions struct {
	Branch string // optional --branch <branch>; empty clones the remote's default branch
}
