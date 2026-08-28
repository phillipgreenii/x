//go:build contract

package gittest_test

// Guarantee tests: one per design §5 hermeticity guarantee (epic bead
// pg2-svfbb, design section 6 item 4). Each test plants an adversarial
// condition -- a hostile ambient config, a decoy repo positioned to catch
// an escaped discovery walk, or an executable hook canary at the location
// git would consult absent the fixture's override -- and asserts the
// hostile condition has NO effect, not merely that the happy path works.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

// TestGuarantee1HomeAndSystemConfigIsolation covers design §5 guarantee 1:
// HOME points at a fresh empty directory (no user/global config) and
// GIT_CONFIG_NOSYSTEM=1 is set (no system config). It exercises two
// independent hostile vectors -- an ambient decoy HOME, and a hostile
// "system" config threaded in via WithEnv("GIT_CONFIG_SYSTEM", ...), which
// is the only way a test can simulate a hostile /etc/gitconfig since it
// cannot write there -- and proves each vector is a REAL leak path (via a
// control assertion) before proving the fixture closes it.
func TestGuarantee1HomeAndSystemConfigIsolation(t *testing.T) {
	ctx := t.Context()

	// --- Vector A: decoy ambient HOME with a hostile .gitconfig. ---
	decoyHome := t.TempDir()
	mustWriteFile(t, filepath.Join(decoyHome, ".gitconfig"), "[alias]\n\tcanary = HOSTILE-HOME-LEAK\n")
	t.Setenv("HOME", decoyHome)

	// Control: prove the decoy actually leaks into a client that does NOT
	// override HOME, so the assertion below is not vacuously safe.
	plain, err := gitclient.Init(ctx, t.TempDir(), gitclient.InitOptions{})
	if err != nil {
		t.Fatalf("control Init (no HOME override) error = %v", err)
	}
	out, err := plain.Run(ctx, "config", "--get", "alias.canary")
	if err != nil || strings.TrimRight(string(out), "\n") != "HOSTILE-HOME-LEAK" {
		t.Fatalf("control: plain client without a HOME override did not observe the decoy alias (out=%q, err=%v); the decoy setup is not exercising anything", out, err)
	}

	// The guarantee: gittest.New (the real consumer entry point) must not
	// see the decoy, even with HOME poisoned in the ambient process env for
	// the whole call.
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "guarantee1-home"})
	if _, err := repo.Client.Run(ctx, "config", "--get", "alias.canary"); err == nil {
		t.Error("alias.canary resolved inside the fixture; the ambient decoy HOME's hostile .gitconfig leaked through despite the fixture's HOME override")
	}

	// Also confirm, structurally, that the REAL repo.Client (as
	// gitfixture.NewRepo actually built it, not just the hand-assembled c2
	// below) carries GIT_CONFIG_NOSYSTEM=1 in its own child env -- Vector B
	// below can only re-derive that option itself (RepoOptions has no
	// passthrough for extra Options), so without this dump the NewRepo
	// code path's own NOSYSTEM line would go unexercised by this test.
	envDump, err := repo.Client.Run(ctx, "-c", "alias.dumpenv=!env", "dumpenv")
	if err != nil {
		t.Fatalf("dumping repo.Client's child env: %v", err)
	}
	if v, ok := lookupEnvLine(string(envDump), "GIT_CONFIG_NOSYSTEM"); !ok || v != "1" {
		t.Errorf("repo.Client's child env has GIT_CONFIG_NOSYSTEM=%q (present=%v), want %q", v, ok, "1")
	}

	// --- Vector B: hostile GIT_CONFIG_SYSTEM, threaded via WithEnv exactly
	// as gitfixture.NewRepo composes its own options (WithHome,
	// WithoutInherited, WithCeiling, WithEnv(GIT_CONFIG_NOSYSTEM=1)), plus
	// the adversarial extra entry -- proving GIT_CONFIG_NOSYSTEM=1 wins
	// even when something manages to also set GIT_CONFIG_SYSTEM. ---
	hostileSystemConfig := filepath.Join(t.TempDir(), "hostile-system-gitconfig")
	mustWriteFile(t, hostileSystemConfig, "[alias]\n\tcanary2 = HOSTILE-SYSTEM-LEAK\n")

	root2 := t.TempDir()
	resolvedRoot2, err := filepath.EvalSymlinks(root2)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", root2, err)
	}
	home2 := filepath.Join(resolvedRoot2, "home")
	if err := os.MkdirAll(home2, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", home2, err)
	}

	// Control: the same hostile config, read directly by a client that
	// does NOT set GIT_CONFIG_NOSYSTEM, DOES see it -- proving the
	// GIT_CONFIG_SYSTEM injection is a real leak path too.
	plain2, err := gitclient.Init(ctx, t.TempDir(), gitclient.InitOptions{},
		gitclient.WithEnv("GIT_CONFIG_SYSTEM", hostileSystemConfig),
	)
	if err != nil {
		t.Fatalf("control Init (GIT_CONFIG_SYSTEM, no NOSYSTEM) error = %v", err)
	}
	out2, err := plain2.Run(ctx, "config", "--get", "alias.canary2")
	if err != nil || strings.TrimRight(string(out2), "\n") != "HOSTILE-SYSTEM-LEAK" {
		t.Fatalf("control: client with GIT_CONFIG_SYSTEM set (no NOSYSTEM) did not observe the hostile alias (out=%q, err=%v); the decoy setup is not exercising anything", out2, err)
	}

	c2, err := gitclient.Init(ctx, filepath.Join(resolvedRoot2, "repo"), gitclient.InitOptions{},
		gitclient.WithHome(home2),
		gitclient.WithoutInherited("SSH_AUTH_SOCK"),
		gitclient.WithCeiling(resolvedRoot2),
		gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		gitclient.WithEnv("GIT_CONFIG_SYSTEM", hostileSystemConfig), // the hostile injection
	)
	if err != nil {
		t.Fatalf("Init (fixture-equivalent options + hostile GIT_CONFIG_SYSTEM) error = %v", err)
	}
	if _, err := c2.Run(ctx, "config", "--get", "alias.canary2"); err == nil {
		t.Error("alias.canary2 resolved; GIT_CONFIG_NOSYSTEM=1 did not override the injected hostile GIT_CONFIG_SYSTEM")
	}
}

// TestGuarantee2CeilingPreventsEscapeFromSymlinkedRoot covers design §5
// guarantee 2: GIT_CEILING_DIRECTORIES is the symlink-resolved fixture
// root, so a discovery walk from a non-repo directory inside the fixture
// (here, the fixture's own home directory) cannot proceed upward out of
// the fixture tree. It runs specifically against a fixture root reached
// only through a symlink (the darwin /var -> /private/var shape) with a
// REAL decoy repository positioned as the fixture's immediate ancestor, so
// an escape is not merely theoretical: without the ceiling, discovery
// really does walk out and anchor at the decoy.
func TestGuarantee2CeilingPreventsEscapeFromSymlinkedRoot(t *testing.T) {
	ctx := t.Context()

	ancestor := t.TempDir()
	if _, err := gitclient.Init(ctx, ancestor, gitclient.InitOptions{}); err != nil {
		t.Fatalf("Init(ancestor decoy repo) error = %v", err)
	}
	ancestorResolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", ancestor, err)
	}

	realRoot := filepath.Join(ancestor, "real-fixture-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", realRoot, err)
	}
	symlinkRoot := filepath.Join(ancestor, "fixture-root-link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatalf("Symlink(%s, %s): %v", realRoot, symlinkRoot, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(symlinkRoot)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", symlinkRoot, err)
	}

	repo, err := gitfixture.NewRepo(ctx, symlinkRoot, gitfixture.RepoOptions{Suite: "guarantee2"})
	if err != nil {
		t.Fatalf("NewRepo(symlinked root) error = %v", err)
	}
	homeDir := filepath.Join(resolvedRoot, "home")

	// Control: WITHOUT a ceiling, discovery from the fixture's own home
	// directory walks up past the (non-repo) fixture root and anchors at
	// the ancestor decoy repo -- proving the decoy is a real escape target.
	escaped, err := gitclient.Discover(ctx, homeDir)
	if err != nil {
		t.Fatalf("control Discover (no ceiling) error = %v, want it to escape to the ancestor decoy", err)
	}
	topOut, err := escaped.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("control: rev-parse --show-toplevel on the escaped client: %v", err)
	}
	gotTop, err := filepath.EvalSymlinks(strings.TrimRight(string(topOut), "\n"))
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", topOut, err)
	}
	if gotTop != ancestorResolved {
		t.Fatalf("control: Discover (no ceiling) from %s anchored at %s, want the ancestor decoy %s -- the escape-target setup is not real", homeDir, gotTop, ancestorResolved)
	}

	// The guarantee, tied to what gitfixture.NewRepo ACTUALLY constructed:
	// dump repo.Client's real child environment (the same alias trick
	// client_test.go uses) and read GIT_CEILING_DIRECTORIES back out of it,
	// rather than re-deriving the expected value independently -- a bug
	// that dropped or mis-derived the ceiling inside NewRepo itself must
	// fail this assertion, not just re-confirm gitclient's own WithCeiling
	// plumbing.
	envDump, err := repo.Client.Run(ctx, "-c", "alias.dumpenv=!env", "dumpenv")
	if err != nil {
		t.Fatalf("dumping repo.Client's child env: %v", err)
	}
	ceiling, ok := lookupEnvLine(string(envDump), "GIT_CEILING_DIRECTORIES")
	if !ok {
		t.Fatalf("repo.Client's child env has no GIT_CEILING_DIRECTORIES at all:\n%s", envDump)
	}
	if ceiling != resolvedRoot {
		t.Fatalf("repo.Client's GIT_CEILING_DIRECTORIES = %q, want the symlink-resolved root %q", ceiling, resolvedRoot)
	}

	// Now prove that THIS value (read from the fixture's own real client,
	// not independently re-derived) actually stops the walk-up: discovery
	// from the same non-repo home directory, using the fixture's own
	// ceiling, must fail outright rather than walk out to the ancestor.
	if _, err := gitclient.Discover(ctx, homeDir, gitclient.WithCeiling(ceiling)); !errors.Is(err, gitclient.ErrNotARepository) {
		t.Errorf("Discover(home, WithCeiling(%q)) error = %v, want errors.Is(_, ErrNotARepository)", ceiling, err)
	}
}

// lookupEnvLine finds "key=value" among the NUL/newline-separated lines of
// dump (from `git -c alias.dumpenv=!env dumpenv`) and returns value, ok.
func lookupEnvLine(dump, key string) (string, bool) {
	prefix := key + "="
	for _, line := range strings.Split(dump, "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return v, true
		}
	}
	return "", false
}

// TestGuarantee3CommitIdentityMatchesFixtureScheme covers design §5
// guarantee 3: a commit's author and committer both read back as
// "gitfixture <Suite> <<Suite>@gitfixture.invalid>" (D7).
func TestGuarantee3CommitIdentityMatchesFixtureScheme(t *testing.T) {
	ctx := t.Context()
	const suite = "guarantee3-suite"
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: suite})

	sha, err := repo.Commit(ctx, "seed commit", map[string]string{"a.txt": "hello\n"})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	out, err := repo.Client.Run(ctx, "log", "-1", "--format=%an%x00%ae%x00%cn%x00%ce", sha)
	if err != nil {
		t.Fatalf("log -1 error = %v", err)
	}
	fields := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(fields) != 4 {
		t.Fatalf("log -1 output = %q, want 4 NUL-separated fields", out)
	}
	authorName, authorEmail, committerName, committerEmail := fields[0], fields[1], fields[2], fields[3]

	wantName := "gitfixture " + suite
	wantEmail := suite + "@gitfixture.invalid"
	if authorName != wantName || authorEmail != wantEmail {
		t.Errorf("author = %q <%s>, want %q <%s>", authorName, authorEmail, wantName, wantEmail)
	}
	if committerName != wantName || committerEmail != wantEmail {
		t.Errorf("committer = %q <%s>, want %q <%s>", committerName, committerEmail, wantName, wantEmail)
	}
}

// TestGuarantee4HooksNeverRun covers design §5 guarantee 4: hooks cannot
// run because core.hooksPath points at an empty directory. The canaries
// are planted at the DEFAULT hooks location (<repo>/.git/hooks) -- the
// path git would consult if core.hooksPath were somehow not honored -- so
// a regression in the redirection itself would be caught, not just "no
// hooks happen to run".
func TestGuarantee4HooksNeverRun(t *testing.T) {
	ctx := t.Context()
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "guarantee4"})

	markerDir := t.TempDir()
	preCommitMarker := filepath.Join(markerDir, "pre-commit-ran")
	postCheckoutMarker := filepath.Join(markerDir, "post-checkout-ran")

	defaultHooksDir := filepath.Join(repo.Dir, ".git", "hooks")
	if err := os.MkdirAll(defaultHooksDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", defaultHooksDir, err)
	}
	// exit 1: if this ever runs, the commit below must fail loudly rather
	// than silently succeeding despite the canary firing.
	plantCanary(t, filepath.Join(defaultHooksDir, "pre-commit"), preCommitMarker, 1)
	plantCanary(t, filepath.Join(defaultHooksDir, "post-checkout"), postCheckoutMarker, 0)

	if _, err := repo.Commit(ctx, "trigger pre-commit canary", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v -- the DEFAULT .git/hooks/pre-commit canary appears to have run despite core.hooksPath", err)
	}
	if _, statErr := os.Stat(preCommitMarker); !os.IsNotExist(statErr) {
		t.Errorf("pre-commit canary marker exists (stat err = %v); the DEFAULT .git/hooks/pre-commit ran despite core.hooksPath", statErr)
	}

	wtPath := filepath.Join(t.TempDir(), "wt")
	if _, err := repo.Client.Run(ctx, "worktree", "add", wtPath, "-b", "guarantee4-wt"); err != nil {
		t.Fatalf("worktree add error = %v", err)
	}
	if _, statErr := os.Stat(postCheckoutMarker); !os.IsNotExist(statErr) {
		t.Errorf("post-checkout canary marker exists (stat err = %v); the DEFAULT .git/hooks/post-checkout ran despite core.hooksPath", statErr)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// plantCanary writes an executable hook script at path that touches
// markerPath (a detectable side effect) and exits with exitCode.
func plantCanary(t *testing.T, path, markerPath string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\ntouch " + shellQuote(markerPath) + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing canary %s: %v", path, err)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
