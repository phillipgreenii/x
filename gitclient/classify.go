package gitclient

import "strings"

// classify turns a failed git invocation into an error. It is pure -- no
// subprocess, no I/O -- so unit tests can exercise every mapping without
// spawning git (an *exec.ExitError cannot be fabricated for that purpose).
//
// exitCode == 0 is not a failure and classify returns nil; every other exit
// code is wrapped as a *GitError carrying a defensive copy of args, the
// exit code, and stderr (trailing newlines trimmed) so callers can inspect
// it via errors.As.
//
// classify deliberately keys ONLY on exitCode, never on stderr text: some
// invocations are LC_ALL=C-scoped and some deliberately are not (design
// §4.4), so text matching would be locale-fragile, and error classification
// MUST key on exit codes, never on localized stderr text.
//
// ErrDetachedHEAD and ErrNoRemote are NOT classify mappings -- they arise
// from call-specific exit-code checks inside CurrentBranch/RemoteURL (empty
// stdout on a successful `branch --show-current`, and `config --get`'s
// exit 1, respectively). ErrNotARepository likewise arises inside the
// constructors' own validation (bead pg2-svfbb.2), not here: see the doc
// comment on that sentinel in errors.go.
func classify(args []string, exitCode int, stderr []byte) error {
	if exitCode == 0 {
		return nil
	}
	return &GitError{
		Args:     append([]string(nil), args...),
		ExitCode: exitCode,
		Stderr:   strings.TrimRight(string(stderr), "\n"),
	}
}
