package gittest_test

// These are real-git smoke tests (untagged, run in the default `go test`
// tier) proving the basic construction/builder shapes work against a real
// git binary. The exhaustive //go:build contract guarantee tests live in
// guarantee_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"
)

func TestNewCreatesAnAnchoredRepo(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "smoke"})

	if repo.Dir == "" {
		t.Fatal("repo.Dir is empty")
	}
	if !filepath.IsAbs(repo.Dir) {
		t.Errorf("repo.Dir = %q, want an absolute path", repo.Dir)
	}
	if repo.Client == nil {
		t.Fatal("repo.Client is nil")
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, ".git")); err != nil {
		t.Errorf("repo.Dir/.git: %v", err)
	}
}

func TestNewDefaultsSuiteToTestName(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{})

	out, err := repo.Client.Run(t.Context(), "config", "--get", "user.name")
	if err != nil {
		t.Fatalf("config --get user.name: %v", err)
	}
	want := "gitfixture " + t.Name()
	if got := strings.TrimRight(string(out), "\n"); got != want {
		t.Errorf("user.name = %q, want %q", got, want)
	}
}

func TestWriteFileAndCommitRoundTrip(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "roundtrip"})
	ctx := t.Context()

	sha, err := repo.Commit(ctx, "seed", map[string]string{
		"a.txt":     "hello\n",
		"dir/b.txt": "world\n",
	})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if sha == "" {
		t.Fatal("Commit() returned an empty sha")
	}

	out, err := repo.Client.Run(ctx, "show", "--stat", "--format=", sha)
	if err != nil {
		t.Fatalf("show --stat: %v", err)
	}
	if !strings.Contains(string(out), "a.txt") || !strings.Contains(string(out), "b.txt") {
		t.Errorf("show --stat = %q, want it to mention both committed files", out)
	}

	status, err := repo.Client.Run(ctx, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status --porcelain: %v", err)
	}
	if len(status) != 0 {
		t.Errorf("status --porcelain after Commit = %q, want a clean tree", status)
	}
}

func TestCommitWithNoFilesProducesAnEmptyCommit(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "empty-commit"})
	ctx := t.Context()

	sha, err := repo.Commit(ctx, "empty", nil)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if sha == "" {
		t.Fatal("Commit() returned an empty sha")
	}
}

func TestAddBareRemoteRegistersAFetchableRemote(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{Suite: "remote-owner"})
	ctx := t.Context()

	if _, err := repo.Commit(ctx, "seed", map[string]string{"a.txt": "hello\n"}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	remote, err := repo.AddBareRemote(ctx, "origin")
	if err != nil {
		t.Fatalf("AddBareRemote() error = %v", err)
	}
	if remote.Dir == "" || remote.Client == nil {
		t.Fatalf("AddBareRemote() returned an incomplete Repo: %+v", remote)
	}

	if _, err := repo.Client.Run(ctx, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push to the bare remote: %v", err)
	}

	out, err := remote.Client.Run(ctx, "rev-parse", "--is-bare-repository")
	if err != nil {
		t.Fatalf("rev-parse --is-bare-repository on the remote: %v", err)
	}
	if got := strings.TrimRight(string(out), "\n"); got != "true" {
		t.Errorf("remote --is-bare-repository = %q, want %q", got, "true")
	}
}
