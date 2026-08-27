package gitclient

import (
	"os"
	"strings"
)

// allowedInheritedVars are the parent-process environment variables carried
// into every git child by default under the allowlist contract (design
// §4.4): everything else -- in particular the entire GIT_* family
// (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_COMMON_DIR, ...) -- is
// excluded unless added explicitly via WithEnv. This is an ALLOWLIST, not a
// denylist: a new git env var is excluded by default.
var allowedInheritedVars = []string{"PATH", "HOME", "SSH_AUTH_SOCK"}

// buildEnv assembles the child process environment for one git invocation
// under the allowlist contract: PATH, HOME, and SSH_AUTH_SOCK are carried
// over from the real process environment unless dropped via
// WithoutInherited or (for HOME) overridden via WithHome, cfg's explicit
// GIT_CEILING_DIRECTORIES (from WithCeiling) and WithEnv entries are
// appended after the carried-over vars -- so they win on a duplicate key,
// per exec.Cmd.Env's own documented last-one-wins semantics -- and LC_ALL=C
// is appended only when parsed is true (the invocation's stdout is parsed
// by this client; see design §4.4's SCOPED, not blanket rule).
//
// buildEnv does not surface cfg.optErr (a rejected WithCeiling call); the
// caller (the future Client constructors, pg2-svfbb.2) MUST check that
// separately before trusting the environment this returns.
func buildEnv(cfg *config, parsed bool) []string {
	env := make([]string, 0, len(allowedInheritedVars)+len(cfg.extraOrder)+2)
	for _, key := range allowedInheritedVars {
		if cfg.without[key] {
			continue
		}
		if key == "HOME" && cfg.homeSet {
			env = append(env, "HOME="+cfg.home)
			continue
		}
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	if len(cfg.ceiling) > 0 {
		env = append(env, "GIT_CEILING_DIRECTORIES="+strings.Join(cfg.ceiling, string(os.PathListSeparator)))
	}
	for _, key := range cfg.extraOrder {
		env = append(env, key+"="+cfg.extra[key])
	}
	if parsed {
		env = append(env, "LC_ALL=C")
	}
	return env
}
