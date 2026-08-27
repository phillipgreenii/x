package gitclient

import "errors"

// config accumulates the effect of Option values passed to a constructor.
// It carries no behavior of its own -- buildEnv (env.go) reads it to
// assemble one invocation's child environment. The constructors that will
// apply it (New, Init, Discover) are pg2-svfbb.2's scope, not this
// package's yet.
type config struct {
	extra      map[string]string // WithEnv additions
	extraOrder []string          // insertion order, for deterministic env output
	home       string
	homeSet    bool
	without    map[string]bool // WithoutInherited drops
	ceiling    []string        // WithCeiling dirs, in call order
	git        string          // WithGit override
	optErr     error           // first error recorded by an Option (e.g. WithCeiling's empty-entry rejection)
}

// Option configures a Client constructor's child environment and git
// binary. A later WithEnv for the same key wins over an earlier one (and
// over the inherited/HOME default), matching exec.Cmd.Env's own
// last-one-wins duplicate-key semantics -- see buildEnv.
type Option func(*config)

// WithEnv adds one explicit entry to the child environment, overriding any
// inherited or default value for the same key.
func WithEnv(key, value string) Option {
	return func(c *config) {
		if c.extra == nil {
			c.extra = make(map[string]string)
		}
		if _, exists := c.extra[key]; !exists {
			c.extraOrder = append(c.extraOrder, key)
		}
		c.extra[key] = value
	}
}

// WithHome overrides HOME in the child environment -- how fixtures point
// at a fresh, isolated home directory.
func WithHome(dir string) Option {
	return func(c *config) {
		c.home = dir
		c.homeSet = true
	}
}

// WithoutInherited drops one or more of the default-carried variables
// (HOME, SSH_AUTH_SOCK) from the child environment entirely -- how
// gitfixture closes the production carve-outs. It takes priority over both
// the inherited value and any WithHome override for the same key.
func WithoutInherited(keys ...string) Option {
	return func(c *config) {
		if c.without == nil {
			c.without = make(map[string]bool)
		}
		for _, k := range keys {
			c.without[k] = true
		}
	}
}

// WithCeiling sets GIT_CEILING_DIRECTORIES. Every entry MUST be non-empty:
// per git(1), an empty entry disables symlink resolution for the entries
// that follow it, which would silently defeat the ceiling on a symlinked
// root (e.g. darwin's /var -> /private/var). A call with any empty entry
// is rejected wholesale -- none of its entries are recorded -- and the
// rejection is remembered on cfg so a later constructor MUST surface it
// rather than silently drop it.
func WithCeiling(dirs ...string) Option {
	return func(c *config) {
		for _, d := range dirs {
			if d == "" {
				if c.optErr == nil {
					c.optErr = errors.New("gitclient: WithCeiling: empty entry is rejected (it disables symlink resolution for subsequent entries)")
				}
				return
			}
		}
		c.ceiling = append(c.ceiling, dirs...)
	}
}

// WithGit overrides the git binary to invoke (default: resolved via
// exec.LookPath at construction). Contract test 3 (design §6) pins this so
// a platform shim (darwin's /usr/bin/git injecting SDKROOT/CPATH/...)
// cannot flake the allowlist-completeness assertion.
func WithGit(path string) Option {
	return func(c *config) { c.git = path }
}
