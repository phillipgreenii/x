//go:build contract

package gitclient

// Contract test 3 (design §6, epic pg2-svfbb): allowlist completeness.
// Dumps the client's ACTUAL child environment through the client itself
// (a git alias that execs `env`, since the client only ever runs git --
// same trick as client_test.go's TestRunNeverSetsLCAllC and
// gittest/guarantee_test.go's lookupEnvLine) and asserts (a) every
// contract var is present with its expected value, and (b) nothing else
// appears beyond the documented git-self-injected tolerance list
// (GIT_PREFIX, GIT_EXEC_PATH, GIT_CONFIG_PARAMETERS, and git-core
// prepended to PATH -- all verified empirically against git 2.54.0, design
// §6 test 3). A planted ambient canary variable that must NOT appear is
// the control proving assertion (b) would actually catch a future
// accidental leak (e.g. buildEnv regressing to carry over, or blanket-copy,
// the parent environment).
//
// The git binary is pinned via WithGit rather than left to a default
// exec.LookPath, per design §6 test 3: darwin's /usr/bin/git shim injects
// SDKROOT/CPATH/... into its children, which would otherwise flake
// assertion (b) depending on which git happens to resolve first on a
// given machine's PATH. Pinning explicitly makes the intent unambiguous
// even where it resolves to the same binary a default lookup would find.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestAllowlistCompletenessAgainstTheClientsOwnChildEnv(t *testing.T) {
	ctx := t.Context()

	// Plant an ambient canary that is NOT part of the environment contract
	// at all. If buildEnv ever regressed to carrying over (or
	// blanket-copying) the parent environment instead of building it from
	// the allowlist, this canary would appear in the dump below and the
	// "nothing else" assertion would catch it -- the control proving this
	// test is not vacuously satisfied by construction.
	t.Setenv("GITCLIENT_CONTRACT_CANARY", "should-never-appear-in-a-child")
	// SSH_AUTH_SOCK is contract-carried (design §4.4) but not guaranteed
	// set in every test environment; set it explicitly so its presence is
	// genuinely exercised rather than silently skipped.
	const wantSSHAuthSock = "/tmp/gitclient-contract-test-fake-agent.sock"
	t.Setenv("SSH_AUTH_SOCK", wantSSHAuthSock)

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath(git): %v", err)
	}

	c, err := Init(ctx, t.TempDir(), InitOptions{},
		WithGit(gitPath),
		WithEnv("GITCLIENT_CONTRACT_EXTRA", "extra-value"),
	)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	out, err := c.Run(ctx, "-c", "alias.dumpenv=!env", "dumpenv")
	if err != nil {
		t.Fatalf("Run(dumpenv) error = %v", err)
	}
	got := parseEnvDump(string(out))

	// (a) every contract var is present with its expected value.
	wantPATHSuffix := os.Getenv("PATH")
	gotPATH, ok := got["PATH"]
	if !ok {
		t.Fatalf("child env has no PATH at all:\n%s", out)
	}
	if !strings.HasSuffix(gotPATH, wantPATHSuffix) {
		t.Errorf("child PATH = %q, want it to end with the inherited PATH %q (git-core is documented to prepend its own exec path)", gotPATH, wantPATHSuffix)
	}
	if got["HOME"] != os.Getenv("HOME") {
		t.Errorf("child HOME = %q, want the inherited %q", got["HOME"], os.Getenv("HOME"))
	}
	if got["SSH_AUTH_SOCK"] != wantSSHAuthSock {
		t.Errorf("child SSH_AUTH_SOCK = %q, want the inherited %q", got["SSH_AUTH_SOCK"], wantSSHAuthSock)
	}
	if got["GITCLIENT_CONTRACT_EXTRA"] != "extra-value" {
		t.Errorf("child GITCLIENT_CONTRACT_EXTRA = %q, want the WithEnv-added %q", got["GITCLIENT_CONTRACT_EXTRA"], "extra-value")
	}
	if v, present := got["GITCLIENT_CONTRACT_CANARY"]; present {
		t.Errorf("child env contains the planted ambient canary GITCLIENT_CONTRACT_CANARY=%q; the allowlist did not exclude an unlisted ambient var -- this is exactly the leak this test exists to catch", v)
	}

	// (b) nothing else appears beyond the allowlist plus the documented
	// git-self-injected tolerance list.
	allowed := map[string]bool{
		"PATH":                     true,
		"HOME":                     true,
		"SSH_AUTH_SOCK":            true,
		"GITCLIENT_CONTRACT_EXTRA": true,
		// git-self-injected tolerance list (design §6 test 3):
		"GIT_PREFIX":            true,
		"GIT_EXEC_PATH":         true,
		"GIT_CONFIG_PARAMETERS": true,
		// Platform-injected, NOT git-self-injected: verified empirically
		// (this bead) that __CF_USER_TEXT_ENCODING appears even when git
		// is spawned with a hand-built two-entry envp (PATH, HOME) with no
		// gitclient code involved at all -- it is injected by macOS/
		// CoreFoundation into the shell git spawns for a `!`-prefixed
		// alias (this test's `alias.dumpenv=!env` trick), not by
		// gitclient's own buildEnv. Absent on the Linux CI runners this
		// repo's own CI uses. Tolerated here, distinctly from the design's
		// git-self-injected list, so a real future leak on darwin is not
		// masked by widening that list itself.
		"__CF_USER_TEXT_ENCODING": true,
	}
	var unexpected []string
	for k := range got {
		if !allowed[k] {
			unexpected = append(unexpected, k)
		}
	}
	if len(unexpected) > 0 {
		t.Errorf("child env has unexpected vars beyond the allowlist + documented git tolerance list: %v\nfull dump:\n%s", unexpected, out)
	}
}

// parseEnvDump parses `env`'s newline-delimited KEY=VALUE output into a
// map, splitting each line on its FIRST '=' (a value may itself contain
// '=', as GIT_CONFIG_PARAMETERS does here).
func parseEnvDump(dump string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(dump, "\n") {
		if line == "" {
			continue
		}
		if idx := strings.IndexByte(line, '='); idx >= 0 {
			m[line[:idx]] = line[idx+1:]
		}
	}
	return m
}
