package gitfixture_test

// These are real-git smoke tests (untagged, run in the default `go test`
// tier), matching gitclient/client_test.go's own precedent of exercising
// its real-git constructors directly rather than only through the
// //go:build integration/contract suites -- gitfixture.NewRepo itself
// always calls gitclient.Init (a real git subprocess), so there is no
// tagged-vs-untagged distinction to preserve here that client_test.go
// doesn't already cross.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/x/gitfixture"
)

// TestNewRepoRequiresSuite covers NewRepo's required-field validation:
// RepoOptions.Suite is documented as REQUIRED (design §5 guarantee 3, D7),
// and this is the only test anywhere in this repo that calls
// gitfixture.NewRepo directly with an empty Suite -- gittest.New always
// defaults it to t.Name() before delegating, so this branch is otherwise
// unreached.
func TestNewRepoRequiresSuite(t *testing.T) {
	ctx := t.Context()
	_, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{})
	if err == nil {
		t.Fatal("NewRepo() with an empty Suite: error = nil, want the required-field rejection")
	}
}

// TestWriteFileFailsWhenAParentPathComponentIsAFile covers WriteFile's
// os.MkdirAll error branch: a path segment above rel already exists as a
// regular file, so MkdirAll cannot create the parent directory.
func TestWriteFileFailsWhenAParentPathComponentIsAFile(t *testing.T) {
	ctx := t.Context()
	repo, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "writefile-mkdirall-error"})
	if err != nil {
		t.Fatalf("NewRepo() error = %v", err)
	}

	if err := repo.WriteFile("blocker", "not a directory"); err != nil {
		t.Fatalf("WriteFile(blocker) error = %v", err)
	}
	if err := repo.WriteFile("blocker/nested.txt", "content"); err == nil {
		t.Fatal("WriteFile() under a file (not a directory): error = nil, want an error")
	}
}

// TestWriteFileFailsWhenTargetIsADirectory covers WriteFile's os.WriteFile
// error branch: rel names an existing directory, so writing a file there
// fails ("is a directory").
func TestWriteFileFailsWhenTargetIsADirectory(t *testing.T) {
	ctx := t.Context()
	repo, err := gitfixture.NewRepo(ctx, t.TempDir(), gitfixture.RepoOptions{Suite: "writefile-target-is-dir"})
	if err != nil {
		t.Fatalf("NewRepo() error = %v", err)
	}

	dirPath := filepath.Join(repo.Dir, "somedir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dirPath, err)
	}
	if err := repo.WriteFile("somedir", "content"); err == nil {
		t.Fatal("WriteFile() writing to a path that is a directory: error = nil, want an error")
	}
}
