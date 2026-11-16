package gitfixture_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
)

func buildTemplate(t *testing.T) (*gitfixture.Template, string) {
	t.Helper()
	root := t.TempDir()
	tpl, err := gitfixture.NewTemplate(t.Context(), root, gitfixture.RepoOptions{Suite: "tpl"}, func(ctx context.Context, r *gitfixture.Repo) error {
		if _, err := r.Commit(ctx, "first", map[string]string{"a.txt": "a\n", "sub/b.sh": "#!/bin/sh\n"}); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(r.Dir, "sub/b.sh"), 0o755); err != nil {
			return err
		}
		if _, err := r.Client.Run(ctx, "add", "sub/b.sh"); err != nil {
			return err
		}
		if _, err := r.Client.Run(ctx, "commit", "--amend", "--no-edit"); err != nil {
			return err
		}
		_, err := r.Client.Run(ctx, "branch", "feature")
		return err
	})
	if err != nil {
		t.Fatalf("NewTemplate() error = %v", err)
	}
	return tpl, root
}

func gitOut(t *testing.T, r *gitfixture.Repo, args ...string) string {
	t.Helper()
	out, err := r.Client.Run(t.Context(), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestTemplateCopyWorksFromADifferentAbsolutePath proves a copy is a
// working, hermetic repository under ITS OWN root: history and branches
// carry over, core.hooksPath points at the copy's hooks directory (not the
// template's), commits work, and the copies are independent.
func TestTemplateCopyWorksFromADifferentAbsolutePath(t *testing.T) {
	tpl, tplRoot := buildTemplate(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	a, err := tpl.NewRepo(t.Context(), rootA, "copy-a")
	if err != nil {
		t.Fatalf("NewRepo(A) error = %v", err)
	}
	b, err := tpl.NewRepo(t.Context(), rootB, "")
	if err != nil {
		t.Fatalf("NewRepo(B) error = %v", err)
	}

	for _, c := range []struct {
		r    *gitfixture.Repo
		root string
	}{{a, rootA}, {b, rootB}} {
		resolved, _ := filepath.EvalSymlinks(c.root)
		if c.r.Root() != resolved || !strings.HasPrefix(c.r.Dir, resolved+string(filepath.Separator)) {
			t.Fatalf("copy Root()=%s Dir=%s, want under %s", c.r.Root(), c.r.Dir, resolved)
		}
		if strings.HasPrefix(c.r.Dir, tplRoot) {
			t.Fatalf("copy %s is under the template root %s", c.r.Dir, tplRoot)
		}
		if got, want := gitOut(t, c.r, "config", "core.hooksPath"), filepath.Join(resolved, "hooks"); got != want {
			t.Errorf("core.hooksPath = %q, want %q", got, want)
		}
		if got := gitOut(t, c.r, "log", "--format=%s"); got != "first" {
			t.Errorf("log = %q, want %q", got, "first")
		}
		if got := gitOut(t, c.r, "rev-parse", "--verify", "feature"); got == "" {
			t.Error("branch feature missing from copy")
		}
		if got := gitOut(t, c.r, "status", "--porcelain"); got != "" {
			t.Errorf("copy not clean: %q", got)
		}
		fi, err := os.Stat(filepath.Join(c.r.Dir, "sub/b.sh"))
		if err != nil || fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("exec bit lost on sub/b.sh: %v %v", fi, err)
		}
	}
	if got := gitOut(t, a, "config", "user.name"); got != "gitfixture copy-a" {
		t.Errorf("A user.name = %q", got)
	}
	if got := gitOut(t, b, "config", "user.name"); got != "gitfixture tpl" {
		t.Errorf("B user.name = %q, want the template suite", got)
	}

	// Independence: a commit in A is visible neither in B nor the template.
	if _, err := a.Commit(t.Context(), "second", map[string]string{"c.txt": "c\n"}); err != nil {
		t.Fatalf("Commit in A: %v", err)
	}
	if got := gitOut(t, b, "log", "--format=%s"); got != "first" {
		t.Errorf("B log after commit in A = %q, want only first", got)
	}
	// The template repo must still be intact and usable for another copy.
	if _, err := tpl.NewRepo(t.Context(), t.TempDir(), ""); err != nil {
		t.Errorf("third copy: %v", err)
	}
}

// TestTemplateCopyRefusesExistingRepo covers the ensureNew guard.
func TestTemplateCopyRefusesExistingRepo(t *testing.T) {
	tpl, _ := buildTemplate(t)
	root := t.TempDir()
	if _, err := tpl.NewRepo(t.Context(), root, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.NewRepo(t.Context(), root, ""); err == nil {
		t.Fatal("second copy into the same root: error = nil, want already-exists")
	}
}

// TestNewTemplateRejectsAbsolutePathState covers the relocation guards.
func TestNewTemplateRejectsAbsolutePathState(t *testing.T) {
	ctx := t.Context()
	if _, err := gitfixture.NewTemplate(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "s", SeparateGitDir: "g"}, nil); err == nil {
		t.Error("SeparateGitDir: error = nil, want rejection")
	}
	_, err := gitfixture.NewTemplate(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "s", InitialCommit: true}, func(ctx context.Context, r *gitfixture.Repo) error {
		_, err := r.AddBareRemote(ctx, "origin")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "remotes") {
		t.Errorf("remote: error = %v, want remotes rejection", err)
	}
	_, err = gitfixture.NewTemplate(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "s", InitialCommit: true}, func(ctx context.Context, r *gitfixture.Repo) error {
		wt, _ := r.RootPath("wt")
		_, err := r.Client.Run(ctx, "worktree", "add", "-b", "w", wt)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "worktrees") {
		t.Errorf("worktree: error = %v, want worktrees rejection", err)
	}
}

// BenchmarkFreshVersusTemplate compares building a repo with two commits
// from scratch against copying a template that holds them.
func BenchmarkFreshVersusTemplate(b *testing.B) {
	build := func(ctx context.Context, r *gitfixture.Repo) error {
		for _, m := range []string{"one", "two"} {
			if _, err := r.Commit(ctx, m, map[string]string{m + ".txt": m}); err != nil {
				return err
			}
		}
		return nil
	}
	ctx := b.Context()
	b.Run("fresh", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			r, err := gitfixture.NewRepo(ctx, b.TempDir(), gitfixture.RepoOptions{Suite: "bench", InitialCommit: true})
			if err != nil {
				b.Fatal(err)
			}
			if err := build(ctx, r); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("template", func(b *testing.B) {
		tpl, err := gitfixture.NewTemplate(ctx, b.TempDir(), gitfixture.RepoOptions{Suite: "bench", InitialCommit: true}, build)
		if err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := tpl.NewRepo(ctx, b.TempDir(), ""); err != nil {
				b.Fatal(err)
			}
		}
	})
}
