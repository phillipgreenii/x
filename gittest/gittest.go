// Package gittest is the *testing.T adapter over gitfixture: what
// _test.go files import to get an isolated, real git repository. This is
// the ONLY package in the fixture split that may import "testing" --
// gitfixture itself (the core) is testing-free by design (design §5,
// epic bead pg2-svfbb).
package gittest

import (
	"testing"

	"github.com/phillipgreenii/x/gitclient"
	"github.com/phillipgreenii/x/gitfixture"
)

// New builds an isolated fixture repository for t: the fixture root is
// t.TempDir() (removed automatically at test end -- nothing in the
// fixture ever writes outside that root, so no additional cleanup is
// registered), the context is t.Context(), and any error from
// gitfixture.NewRepo fails the test immediately via t.Fatal.
//
// If opts.Suite is empty it defaults to t.Name(), so the forensic identity
// (D7) always names the (sub)test that owns the fixture.
func New(t *testing.T, opts gitfixture.RepoOptions) *gitfixture.Repo {
	t.Helper()

	if opts.Suite == "" {
		opts.Suite = t.Name()
	}

	repo, err := gitfixture.NewRepo(t.Context(), t.TempDir(), opts)
	if err != nil {
		t.Fatalf("gittest.New: %v", err)
	}
	return repo
}

// NewSibling creates a further fixture repository named name beside
// parent, under the same fixture root (see gitfixture.Repo.NewSibling and
// RepoOptions.Name), failing the test immediately on error. If opts.Suite
// is empty it is inherited from parent.
func NewSibling(t *testing.T, parent *gitfixture.Repo, name string, opts gitfixture.RepoOptions) *gitfixture.Repo {
	t.Helper()

	repo, err := parent.NewSibling(t.Context(), name, opts)
	if err != nil {
		t.Fatalf("gittest.NewSibling(%q): %v", name, err)
	}
	return repo
}

// Clone clones src (typically a bare remote from AddBareRemote) into a new
// working repository named name (see gitfixture.Repo.Clone), failing the
// test immediately on error.
func Clone(t *testing.T, src *gitfixture.Repo, name string) *gitfixture.Repo {
	t.Helper()

	repo, err := src.Clone(t.Context(), name)
	if err != nil {
		t.Fatalf("gittest.Clone(%q): %v", name, err)
	}
	return repo
}

// ClientAt returns a hermetic client for dir under repo's fixture (see
// gitfixture.Repo.ClientAt): dir MUST resolve inside the fixture root, e.g.
// a linked worktree created under repo.RootPath. It fails the test
// immediately on error, like New, NewSibling and Clone.
func ClientAt(t *testing.T, repo *gitfixture.Repo, dir string) *gitclient.Client {
	t.Helper()

	c, err := repo.ClientAt(t.Context(), dir)
	if err != nil {
		t.Fatalf("gittest.ClientAt(%q): %v", dir, err)
	}
	return c
}
