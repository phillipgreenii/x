// Package gittest is the *testing.T adapter over gitfixture: what
// _test.go files import to get an isolated, real git repository. This is
// the ONLY package in the fixture split that may import "testing" --
// gitfixture itself (the core) is testing-free by design (design §5,
// epic bead pg2-svfbb).
package gittest

import (
	"testing"

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
