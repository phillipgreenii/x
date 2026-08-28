// Package gitfixture creates isolated, real git repositories for tests --
// hermetic by CONSTRUCTION (pg2-8wnhc): it must not be possible to
// configure a fixture repo that can touch a real path.
//
// This is the CORE package: it is free of testing imports and depends only
// on the Go standard library plus gitclient, so a future thin CLI binary
// can wrap it for bats suites (pg2-gucfd) without linking testing into a
// shipped binary. The *testing.T adapter -- what _test.go files actually
// import -- is the separate gittest package.
//
// Design of record: the DESIGN field of epic bead pg2-svfbb, section 5
// (operator ruling 2026-08-27: the design lives in the bead, not committed
// to this repo).
package gitfixture

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/phillipgreenii/x/gitclient"
)

// RepoOptions configures NewRepo.
type RepoOptions struct {
	// Suite is REQUIRED: it is folded into the fixture's forensic identity
	// (design §5 guarantee 3, D7) -- user.name = "gitfixture <Suite>",
	// user.email = "<Suite>@gitfixture.invalid" -- so any escape is
	// traceable to the suite that caused it.
	Suite string

	Bare          bool
	InitialBranch string // default "main"
}

// Repo is one isolated fixture repository.
type Repo struct {
	Dir    string
	Client *gitclient.Client

	// root and suite are not part of the public contract (design §5 lists
	// only Dir and Client); they let AddBareRemote create a second fixture
	// repository nested under the SAME fixture tree, sharing the identity
	// scheme, without re-deriving either from Dir.
	root  string
	suite string
}

// NewRepo creates an isolated repository under root (which the caller
// owns, e.g. a temp dir). The layout under root is deterministic: the
// repository at <root>/repo, the fixture HOME at <root>/home, and the
// empty hooksPath directory at <root>/hooks. GIT_CEILING_DIRECTORIES is
// the symlink-RESOLVED root itself (design §5 guarantee 2) -- resolving
// symlinks before deriving the ceiling is REQUIRED, since on darwin
// t.TempDir() lives under /var, itself a symlink to /private/var.
//
// Identity is repo-local config (guarantee 3, D7): user.name =
// "gitfixture <Suite>", user.email = "<Suite>@gitfixture.invalid".
// core.hooksPath points at the empty hooks directory (guarantee 4).
//
// The child environment is built ONLY through gitclient's existing public
// Options (guarantee 5): WithHome, WithoutInherited("SSH_AUTH_SOCK"),
// WithCeiling, and WithEnv("GIT_CONFIG_NOSYSTEM", "1") -- there is no
// private hook into gitclient for this package to use instead.
func NewRepo(ctx context.Context, root string, opts RepoOptions) (*Repo, error) {
	if opts.Suite == "" {
		return nil, fmt.Errorf("gitfixture: RepoOptions.Suite is required")
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("gitfixture: creating root %s: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: resolving root %s: %w", root, err)
	}

	homeDir := filepath.Join(resolvedRoot, "home")
	hooksDir := filepath.Join(resolvedRoot, "hooks")
	repoDir := filepath.Join(resolvedRoot, "repo")
	for _, d := range []string{homeDir, hooksDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("gitfixture: creating %s: %w", d, err)
		}
	}

	gitOpts := []gitclient.Option{
		gitclient.WithHome(homeDir),
		gitclient.WithoutInherited("SSH_AUTH_SOCK"),
		gitclient.WithCeiling(resolvedRoot),
		gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
	}

	initOpts := gitclient.InitOptions{Bare: opts.Bare, InitialBranch: opts.InitialBranch}
	c, err := gitclient.Init(ctx, repoDir, initOpts, gitOpts...)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: initializing repo at %s: %w", repoDir, err)
	}

	resolvedRepoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: resolving repo dir %s: %w", repoDir, err)
	}

	if _, err := c.Run(ctx, "config", "user.name", "gitfixture "+opts.Suite); err != nil {
		return nil, fmt.Errorf("gitfixture: setting user.name: %w", err)
	}
	if _, err := c.Run(ctx, "config", "user.email", opts.Suite+"@gitfixture.invalid"); err != nil {
		return nil, fmt.Errorf("gitfixture: setting user.email: %w", err)
	}
	if _, err := c.Run(ctx, "config", "core.hooksPath", hooksDir); err != nil {
		return nil, fmt.Errorf("gitfixture: setting core.hooksPath: %w", err)
	}

	return &Repo{
		Dir:    resolvedRepoDir,
		Client: c,
		root:   resolvedRoot,
		suite:  opts.Suite,
	}, nil
}

// WriteFile writes content to a file at rel (relative to the repo's
// working tree), creating parent directories as needed. It does not stage
// or commit the file -- pair it with Client.Run("add", ...) directly, or
// use Commit for the common write-stage-commit shape.
func (r *Repo) WriteFile(rel, content string) error {
	path := filepath.Join(r.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gitfixture: creating parent directory for %s: %w", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("gitfixture: writing %s: %w", rel, err)
	}
	return nil
}

// Commit writes files (path -> content, WriteFile semantics), stages them,
// and commits with msg, returning the resulting commit SHA. An empty files
// map produces an --allow-empty commit rather than failing on "nothing to
// commit".
func (r *Repo) Commit(ctx context.Context, msg string, files map[string]string) (string, error) {
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels) // deterministic staging order

	for _, rel := range rels {
		if err := r.WriteFile(rel, files[rel]); err != nil {
			return "", err
		}
	}

	if len(rels) > 0 {
		addArgs := append([]string{"add", "--"}, rels...)
		if _, err := r.Client.Run(ctx, addArgs...); err != nil {
			return "", fmt.Errorf("gitfixture: staging %v: %w", rels, err)
		}
		if _, err := r.Client.Run(ctx, "commit", "-m", msg); err != nil {
			return "", fmt.Errorf("gitfixture: committing: %w", err)
		}
	} else {
		if _, err := r.Client.Run(ctx, "commit", "-m", msg, "--allow-empty"); err != nil {
			return "", fmt.Errorf("gitfixture: committing (empty): %w", err)
		}
	}

	out, err := r.Client.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitfixture: resolving HEAD: %w", err)
	}
	sha := string(out)
	for len(sha) > 0 && (sha[len(sha)-1] == '\n' || sha[len(sha)-1] == '\r') {
		sha = sha[:len(sha)-1]
	}
	return sha, nil
}

// AddBareRemote creates a second, local bare repository nested under this
// fixture's own root (so it inherits the same hermeticity guarantees --
// its own HOME override, ceiling, and GIT_CONFIG_NOSYSTEM=1, all newly
// derived rather than borrowed) and registers it as remote name on r. The
// returned Repo is that bare repository, usable as a "remote" for
// Fetch-style tests.
func (r *Repo) AddBareRemote(ctx context.Context, name string) (*Repo, error) {
	remoteRoot := filepath.Join(r.root, "remotes", name)
	remote, err := NewRepo(ctx, remoteRoot, RepoOptions{
		Suite: r.suite + "-remote-" + name,
		Bare:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("gitfixture: creating bare remote %s: %w", name, err)
	}
	if _, err := r.Client.Run(ctx, "remote", "add", name, remote.Dir); err != nil {
		return nil, fmt.Errorf("gitfixture: registering remote %s at %s: %w", name, remote.Dir, err)
	}
	return remote, nil
}
