package gitclient

import (
	"os"
	"slices"
	"testing"
)

// applyOptions is the tiny helper the real constructors (pg2-svfbb.2) will
// use: build a zero config and apply every Option in order.
func applyOptions(opts ...Option) *config {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

func TestBuildEnvCarriesOverTheAllowlistedVarsOnly(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/home/tester")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-agent.sock")
	// A GIT_* var present in the parent process MUST NOT be carried over
	// -- this is the allowlist, not a denylist.
	t.Setenv("GIT_DIR", "/decoy/.git")

	cfg := applyOptions()
	env := buildEnv(cfg, false)

	want := []string{"PATH=/usr/bin:/bin", "HOME=/home/tester", "SSH_AUTH_SOCK=/tmp/ssh-agent.sock"}
	for _, w := range want {
		if !slices.Contains(env, w) {
			t.Errorf("buildEnv() = %v, missing %q", env, w)
		}
	}
	for _, e := range env {
		if len(e) >= 4 && e[:4] == "GIT_" {
			t.Errorf("buildEnv() = %v, contains a disallowed GIT_* var: %q", env, e)
		}
	}
}

func TestBuildEnvOmitsUnsetInheritedVars(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	// t.Setenv registers restoration of whatever HOME/SSH_AUTH_SOCK held
	// before this test; os.Unsetenv then actually clears them for its
	// duration -- t.Setenv itself has no "unset" mode since an empty
	// string is still a set value.
	t.Setenv("HOME", "placeholder")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "placeholder")
	if err := os.Unsetenv("SSH_AUTH_SOCK"); err != nil {
		t.Fatal(err)
	}

	cfg := applyOptions()
	env := buildEnv(cfg, false)

	for _, e := range env {
		if len(e) >= 5 && e[:5] == "HOME=" {
			t.Errorf("buildEnv() = %v, HOME should be absent when unset in the parent process", env)
		}
		if len(e) >= 14 && e[:14] == "SSH_AUTH_SOCK=" {
			t.Errorf("buildEnv() = %v, SSH_AUTH_SOCK should be absent when unset in the parent process", env)
		}
	}
}

func TestWithHomeOverridesInheritedHOME(t *testing.T) {
	t.Setenv("HOME", "/home/real")

	cfg := applyOptions(WithHome("/fixture/home"))
	env := buildEnv(cfg, false)

	if !slices.Contains(env, "HOME=/fixture/home") {
		t.Errorf("buildEnv() = %v, want HOME=/fixture/home", env)
	}
	if slices.Contains(env, "HOME=/home/real") {
		t.Errorf("buildEnv() = %v, real HOME leaked through", env)
	}
}

func TestWithoutInheritedDropsSSHAuthSock(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-agent.sock")

	cfg := applyOptions(WithoutInherited("SSH_AUTH_SOCK"))
	env := buildEnv(cfg, false)

	for _, e := range env {
		if len(e) >= 14 && e[:14] == "SSH_AUTH_SOCK=" {
			t.Fatalf("buildEnv() = %v, SSH_AUTH_SOCK should have been dropped", env)
		}
	}
}

func TestWithoutInheritedBeatsWithHomeForTheSameKey(t *testing.T) {
	cfg := applyOptions(WithHome("/fixture/home"), WithoutInherited("HOME"))
	env := buildEnv(cfg, false)

	for _, e := range env {
		if len(e) >= 5 && e[:5] == "HOME=" {
			t.Fatalf("buildEnv() = %v, HOME should have been dropped by WithoutInherited despite WithHome", env)
		}
	}
}

func TestWithEnvAddsAnExplicitEntry(t *testing.T) {
	cfg := applyOptions(WithEnv("GIT_CONFIG_NOSYSTEM", "1"))
	env := buildEnv(cfg, false)

	if !slices.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("buildEnv() = %v, want GIT_CONFIG_NOSYSTEM=1", env)
	}
}

func TestWithEnvOverridesInheritedForSameKey(t *testing.T) {
	t.Setenv("HOME", "/home/real")

	cfg := applyOptions(WithHome("/fixture/home"), WithEnv("HOME", "/explicit/home"))
	env := buildEnv(cfg, false)

	// exec.Cmd.Env documents last-one-wins for duplicate keys; WithEnv is
	// applied after the inherited-vars loop, so it must be the winner.
	last := ""
	for _, e := range env {
		if len(e) >= 5 && e[:5] == "HOME=" {
			last = e
		}
	}
	if last != "HOME=/explicit/home" {
		t.Errorf("last HOME entry = %q, want %q", last, "HOME=/explicit/home")
	}
}

func TestWithCeilingSetsGitCeilingDirectories(t *testing.T) {
	cfg := applyOptions(WithCeiling("/fixture/root"))
	if cfg.optErr != nil {
		t.Fatalf("unexpected optErr: %v", cfg.optErr)
	}
	env := buildEnv(cfg, false)
	if !slices.Contains(env, "GIT_CEILING_DIRECTORIES=/fixture/root") {
		t.Errorf("buildEnv() = %v, want GIT_CEILING_DIRECTORIES=/fixture/root", env)
	}
}

func TestWithCeilingMultipleDirsJoinedByPathListSeparator(t *testing.T) {
	cfg := applyOptions(WithCeiling("/a", "/b"))
	if cfg.optErr != nil {
		t.Fatalf("unexpected optErr: %v", cfg.optErr)
	}
	env := buildEnv(cfg, false)
	if !slices.Contains(env, "GIT_CEILING_DIRECTORIES=/a:/b") {
		t.Errorf("buildEnv() = %v, want GIT_CEILING_DIRECTORIES=/a:/b", env)
	}
}

func TestWithCeilingRejectsEmptyEntry(t *testing.T) {
	cfg := applyOptions(WithCeiling("/a", "", "/b"))
	if cfg.optErr == nil {
		t.Fatal("WithCeiling with an empty entry should record an error on cfg.optErr")
	}
	if len(cfg.ceiling) != 0 {
		t.Errorf("cfg.ceiling = %v, want none of the entries recorded when one is empty", cfg.ceiling)
	}
}

func TestWithGitSetsTheOverridePath(t *testing.T) {
	cfg := applyOptions(WithGit("/opt/git/bin/git"))
	if cfg.git != "/opt/git/bin/git" {
		t.Errorf("cfg.git = %q, want %q", cfg.git, "/opt/git/bin/git")
	}
}

func TestBuildEnvAppendsLCAllOnlyWhenParsed(t *testing.T) {
	cfg := applyOptions()

	if slices.Contains(buildEnv(cfg, false), "LC_ALL=C") {
		t.Error("buildEnv(cfg, false) contains LC_ALL=C, want it absent")
	}
	if !slices.Contains(buildEnv(cfg, true), "LC_ALL=C") {
		t.Error("buildEnv(cfg, true) does not contain LC_ALL=C, want it present")
	}
}
