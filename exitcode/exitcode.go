// Package exitcode defines the coarse process exit codes shared by every
// workspace CLI and daemon, so that a caller (a script, an agent, a
// supervisor) can branch on the same meaning from any tool.
//
// The codes are deliberately coarse and app-neutral: "the daemon is down" has
// a slot here, "the recorder is behind" does not. An app MAY define its own
// codes in the range AppMin..AppMax; it MUST NOT reuse a shared value for a
// different meaning, and MUST NOT assign 1 a branchable meaning.
//
// Reserved by the shell or by other tools, never assigned here or by an app:
//   - 64..78: the sysexits.h range.
//   - 124: timeout(1) reports a timed-out command with it.
//   - 126, 127: the shell's "found but not executable" and "not found".
//   - 128+n: the shell's report of death by signal n (130 = SIGINT, 137 =
//     SIGKILL, 143 = SIGTERM). A process MUST NOT exit with 128+n unless it
//     is relaying the status of a child killed by signal n.
//   - 255: out-of-range or "exit status unknown" in many runtimes.
//
// Values are stable: renumbering one breaks every caller that branches on it,
// so the package's test pins each number.
package exitcode

// Shared exit codes. Each documents when to use it and when not to.
const (
	// OK means the command did what was asked. Use it for success only,
	// including a successful query that found nothing when "nothing" is a
	// valid answer (a filter that matches zero rows). Do not use it when the
	// result is partial (Degraded) or the thing asked for does not exist
	// (NotFound).
	OK = 0

	// Error is the generic, unexpected failure: an internal error, a bug, an
	// unclassified failure. It carries no branchable meaning; a caller may only
	// conclude "something went wrong". Use it when no other code fits. Do not
	// use it for a failure the caller could act on differently (a missing
	// daemon, a bad flag, a timeout): that has a code below. The workspace rule
	// (code-file-standards.md, Exit Codes) reserves 1 for this.
	Error = 1

	// Usage means the command line itself is wrong: an unknown flag or verb, a
	// missing or malformed argument, a flag combination that is not allowed.
	// The request was never attempted. Use it for what the argument parser
	// rejects. Do not use it for a well-formed request whose content is
	// invalid for the backend (a bad ref name: Error or NotFound) or for a bad
	// config file (Config). `--help` is not a usage error and exits OK.
	Usage = 2

	// Unavailable means a dependency the command needs could not be reached or
	// was not serving: the daemon is down, the socket refused the connection, a
	// backend is unreachable or still starting, or every source of a fan-out
	// failed. Use it when retrying later, or starting the dependency, could
	// succeed. Do not use it for a dependency that answered "no" (that is NotFound,
	// Denied or Conflict), one that answered too slowly (Timeout), or one that is merely busy (Busy). A fan-out where
	// only some sources failed is Degraded, not Unavailable.
	Unavailable = 3

	// NotFound means the thing the caller named does not exist: an unknown id,
	// path, session, bead, record. Use it when the lookup itself worked and the
	// answer is "no such thing". Do not use it when the dependency could not be
	// asked (Unavailable) or when an empty result is a valid answer (OK).
	NotFound = 4

	// Degraded means the command ran and produced a result, but the result is
	// partial or the system is impaired: some sources of a fan-out failed, a
	// status report found a degraded component, a bounded read was cut short in
	// a way the caller must act on. The output is still meaningful and SHOULD be
	// printed. Use it for "worked, but not fully". Do not use it when nothing
	// usable was produced (Unavailable or Error), or for a harmless note such
	// as a truncated listing the output already flags (OK).
	Degraded = 5

	// Timeout means the command gave up because a time budget ran out: its own
	// deadline, a wait that expired, a dependency that did not answer in time.
	// The outcome is unknown, so retrying MAY be safe depending on the verb. Use
	// it only for a deadline the command itself enforced. Do not use it for a
	// dependency that is down (Unavailable), and never 124, which belongs to
	// timeout(1).
	Timeout = 6

	// Config means a configuration input is invalid or missing, so the command
	// cannot start or cannot proceed: an unparsable config file, an unknown or
	// out-of-range key, a missing required setting, an unusable configured
	// path. Use it when the operator must edit configuration. Do not use it for
	// a wrong command-line argument (Usage), for a failing dependency
	// (Unavailable), or for a credential the dependency rejected (Denied).
	Config = 7

	// Denied means the caller is not allowed: credentials are missing,
	// expired or rejected (unauthenticated), or authenticated but not
	// permitted (a permission or ownership refusal, an unwritable state
	// directory the OS denied). Use it when the fix is a login, a token or a
	// permission. Do not use it for a bad credential setting in a config file
	// (Config) or a backend that is down (Unavailable).
	Denied = 8

	// Busy means the command declined to run now because the resource is
	// occupied or the work was deliberately deferred: a lock is held, a pool is
	// exhausted, a concurrency limit is full, a policy declined the request
	// (for example a drain or capacity gate). Nothing was done, the request was
	// valid, and trying again later is expected to work. Use it for "not now".
	// Do not use it for a dependency that is down (Unavailable) or a state
	// change that makes the request wrong (Conflict).
	Busy = 9

	// ShutdownOverrun is a daemon's hard exit when graceful shutdown outlasted
	// its deadline and the watchdog killed the process (see package lifecycle).
	// A supervisor treats it as a failure and may restart. Use it only from a
	// shutdown watchdog. Do not use it for any other failure.
	ShutdownOverrun = 10

	// Conflict means the request was valid but the state it assumed has changed
	// or does not hold: a head that moved, a version or schema mismatch, a
	// cursor that expired, a precondition (compare-and-set) that failed. The
	// caller SHOULD re-read and decide again. Use it for "the world is not as
	// you expected". Do not use it for "occupied right now" (Busy) or "does not
	// exist" (NotFound).
	Conflict = 11
)

// The application-specific range. An app MAY define its own codes in
// AppMin..AppMax for outcomes no shared code covers, MUST document each in its
// help output and README, and MUST NOT define a code here that duplicates the
// meaning of a shared one. The values below AppMin that are not assigned above
// (12..19) are reserved for future shared codes.
const (
	// AppMin is the first app-specific code.
	AppMin = 20
	// AppMax is the last app-specific code.
	AppMax = 63
)

// IsAppSpecific reports whether code lies in the app-specific range.
func IsAppSpecific(code int) bool { return code >= AppMin && code <= AppMax }

// IsSignal reports whether code is in the shell's 128+n band, which is never
// assigned: it means a child was killed by a signal.
func IsSignal(code int) bool { return code > 128 && code < 255 }
