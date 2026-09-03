package gitclient

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

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

// classifyRunErr turns cmd.Wait()'s returned error into the exact
// ctx-error/*GitError result client.go's run has always produced (its own
// doc comment carries the full context-contract rationale) -- extracted
// so run's buffered spawn and Handle's streaming spawn (newHandle) apply
// IDENTICAL classification instead of two copies that could drift apart.
//
// runErr == nil returns nil immediately. Otherwise ctx is consulted
// independently of what runErr happens to be: once a child has actually
// been signaled, os/exec's Cmd.Wait prefers the process's own exit status
// over the context's error, so ctx.Err() must be checked explicitly on
// every failure path rather than inferred from runErr's shape (run's own
// doc comment explains this in full). Only once ctx reports no error is
// runErr classified as an *exec.ExitError (via classify) or, failing
// that, wrapped as a generic spawn error.
func classifyRunErr(ctx context.Context, args []string, stderr []byte, runErr error) error {
	if runErr == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("gitclient: git %s: %w: %w", strings.Join(args, " "), ctxErr, runErr)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return classify(args, exitErr.ExitCode(), stderr)
	}
	return fmt.Errorf("gitclient: git %s: %w", strings.Join(args, " "), runErr)
}
