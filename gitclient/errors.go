package gitclient

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotARepository is returned when a Client is anchored at (or
	// discovered from) a path that is not inside a git repository. It
	// arises from the constructors' own validation (bead pg2-svfbb.2),
	// not from classify: the generic exit code that signals this (128) is
	// shared by many unrelated fatal conditions, so only the caller that
	// knows exactly which probe it ran can attribute it correctly.
	ErrNotARepository = errors.New("gitclient: not a git repository")

	// ErrDetachedHEAD is returned by Locator.CurrentBranch when HEAD does
	// not point at a branch. It derives from EMPTY STDOUT on a
	// *successful* `branch --show-current` (exit 0), so it never flows
	// through classify either.
	ErrDetachedHEAD = errors.New("gitclient: detached HEAD")

	// ErrNoRemote is returned by Locator.RemoteURL when the named remote
	// has no configured URL (`config --get`'s exit 1). pa-monitor
	// branches on exactly this to reach its local: fallback.
	ErrNoRemote = errors.New("gitclient: remote not configured")
)

// GitError wraps any git invocation that exited non-zero; callers reach it
// with errors.As(err, &ge) where ge is *GitError.
type GitError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

// Error formats as "git <args>: exit N: <stderr>".
func (e *GitError) Error() string {
	return fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, e.Stderr)
}

// isExitCode reports whether err is a *GitError with EXACTLY this exit
// code -- the idiom RefExists, IsTracked, and RemoteURL each key on to
// tell "the ordinary, documented negative case" apart from a genuine
// failure that happens to share the same *GitError type. Deliberately
// NOT used by HasUpstream, whose own doc comment explains why it must
// fold in ANY exit code rather than one specific one.
func isExitCode(err error, code int) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.ExitCode == code
}
