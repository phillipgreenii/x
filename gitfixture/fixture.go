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
// Running arbitrary git inside a fixture needs no extra API: every Repo
// carries a gitclient.Client, and Repo.Client.Run(ctx, args...) executes
// any git verb (worktree add/lock, switch -c, merge --ff-only, branch,
// config, rev-parse, checkout, push, fetch, ...) anchored at the repo and
// under the fixture's hermetic environment. Use Repo.Root / Repo.RootPath
// for contained paths beside the repository (worktree targets, ...).
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
	"strings"

	"github.com/phillipgreenii/x/gitclient"
)

// containedPath joins rel onto root and rejects any result that would
// escape root -- e.g. a rel of "../../etc/passwd" -- so WriteFile and
// AddBareRemote cannot be used to touch a path outside the fixture tree.
// No current caller passes such a rel/name, but the package's own
// "hermetic by construction" doc comment (see the package doc above)
// requires this to hold for any FUTURE caller too, not just today's.
func containedPath(root, rel string) (string, error) {
	joined := filepath.Join(root, rel)
	relToRoot, err := filepath.Rel(root, joined)
	if err != nil {
		return "", fmt.Errorf("gitfixture: resolving %q relative to %s: %w", rel, root, err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("gitfixture: %q escapes the fixture root %s", rel, root)
	}
	return joined, nil
}

// defaultRepoName is the directory NewRepo uses under the fixture root
// when RepoOptions.Name is empty.
const defaultRepoName = "repo"

// reservedNames are the top-level entries of a fixture root that hold
// fixture infrastructure, so a repository Name (or SeparateGitDir) can
// never collide with them.
var reservedNames = map[string]bool{"home": true, "hooks": true, "remotes": true}

// validateName checks that name is usable as the directory name of a
// repository directly under a fixture root: one plain path element (no
// separators, not "." or ".."), and not one of reservedNames.
func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/') {
		return fmt.Errorf("gitfixture: %q is not a plain directory name (no separators, not \".\" or \"..\")", name)
	}
	if reservedNames[name] {
		return fmt.Errorf("gitfixture: %q is reserved for fixture infrastructure", name)
	}
	return nil
}

// RepoOptions configures NewRepo.
type RepoOptions struct {
	// Suite is REQUIRED: it is folded into the fixture's forensic identity
	// (design §5 guarantee 3, D7) -- user.name = "gitfixture <Suite>",
	// user.email = "<Suite>@gitfixture.invalid" -- so any escape is
	// traceable to the suite that caused it.
	Suite string

	Bare          bool
	InitialBranch string // default "main"

	// Name is the repository's directory name directly under the fixture
	// root (default "repo"). It lets several repositories -- e.g.
	// "alpha" and "beta" -- live side by side under ONE fixture root,
	// sharing its HOME, hooks directory and ceiling: build the first with
	// NewRepo, the rest with (*Repo).NewSibling. It MUST be a single plain
	// path element and MUST NOT be "home", "hooks" or "remotes" (fixture
	// infrastructure). An explicitly named repository MUST NOT already
	// exist.
	Name string

	// InitialCommit, when true, makes NewRepo finish with one empty commit
	// ("initial commit") so the initial branch exists and HEAD resolves.
	// Not valid with Bare (a bare repository has no work tree to commit
	// from); push into a bare repository instead.
	InitialCommit bool

	// SeparateGitDir, when non-empty, keeps the repository's git directory
	// OUTSIDE its work tree (git init --separate-git-dir): the work tree's
	// ".git" becomes a "gitdir:" file pointing at the directory. The value
	// is a path RELATIVE TO THE FIXTURE ROOT and is containment-checked
	// like WriteFile's rel, so the git directory can never leave the
	// fixture; it MUST NOT be the root itself, inside the repository's own
	// work tree, or a reserved top-level name. Not valid with Bare.
	SeparateGitDir string
}

// Repo is one isolated fixture repository.
type Repo struct {
	Dir    string
	Client *gitclient.Client

	// root and suite are not part of the core design §5 contract (it lists
	// only Dir and Client); they let AddBareRemote, NewSibling and Clone
	// create further fixture repositories under the SAME fixture tree,
	// sharing the identity scheme, without re-deriving either from Dir.
	// Root and RootPath expose root read-only.
	root  string
	suite string
}

// hermeticOptions returns the gitclient Options every repository under
// resolvedRoot is built with (design §5 guarantee 5): a fixture HOME, no
// inherited SSH_AUTH_SOCK, the root as the discovery ceiling, and no
// system config. ONE definition, so NewRepo, NewSibling and Clone cannot
// drift apart.
func hermeticOptions(resolvedRoot string) []gitclient.Option {
	return []gitclient.Option{
		gitclient.WithHome(filepath.Join(resolvedRoot, "home")),
		gitclient.WithoutInherited("SSH_AUTH_SOCK"),
		gitclient.WithCeiling(resolvedRoot),
		gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
	}
}

// configureRepo applies the fixture's repo-local config to c: the forensic
// identity (guarantee 3, D7) and core.hooksPath at the empty hooks
// directory (guarantee 4).
func configureRepo(ctx context.Context, c *gitclient.Client, resolvedRoot, suite string) error {
	if _, err := c.Run(ctx, "config", "user.name", "gitfixture "+suite); err != nil {
		return fmt.Errorf("gitfixture: setting user.name: %w", err)
	}
	if _, err := c.Run(ctx, "config", "user.email", suite+"@gitfixture.invalid"); err != nil {
		return fmt.Errorf("gitfixture: setting user.email: %w", err)
	}
	if _, err := c.Run(ctx, "config", "core.hooksPath", filepath.Join(resolvedRoot, "hooks")); err != nil {
		return fmt.Errorf("gitfixture: setting core.hooksPath: %w", err)
	}
	return nil
}

// ensureNew rejects a path that already exists, so an explicitly named
// repository can never silently re-initialize or clone over another one.
func ensureNew(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("gitfixture: %s already exists", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("gitfixture: checking %s: %w", path, err)
	}
	return nil
}

// separateGitDirPath resolves opts.SeparateGitDir to an absolute path
// under resolvedRoot, rejecting anything that would put the git directory
// at the root itself, inside the repository's own work tree, or on top of
// fixture infrastructure.
func separateGitDirPath(resolvedRoot, repoDir, rel string) (string, error) {
	p, err := containedPath(resolvedRoot, rel)
	if err != nil {
		return "", err
	}
	relToRoot, err := filepath.Rel(resolvedRoot, p)
	if err != nil {
		return "", fmt.Errorf("gitfixture: resolving SeparateGitDir %q: %w", rel, err)
	}
	top := strings.SplitN(relToRoot, string(filepath.Separator), 2)[0]
	switch {
	case relToRoot == ".":
		return "", fmt.Errorf("gitfixture: SeparateGitDir %q is the fixture root itself", rel)
	case reservedNames[top]:
		return "", fmt.Errorf("gitfixture: SeparateGitDir %q is under reserved fixture directory %q", rel, top)
	case p == repoDir || strings.HasPrefix(p, repoDir+string(filepath.Separator)):
		return "", fmt.Errorf("gitfixture: SeparateGitDir %q is inside the repository's own work tree", rel)
	}
	return p, nil
}

// NewRepo creates an isolated repository under root (which the caller
// owns, e.g. a temp dir). The layout under root is deterministic: the
// repository at <root>/<Name> (default <root>/repo), the fixture HOME at
// <root>/home, and the empty hooksPath directory at <root>/hooks.
// GIT_CEILING_DIRECTORIES is the symlink-RESOLVED root itself (design §5
// guarantee 2) -- resolving symlinks before deriving the ceiling is
// REQUIRED, since on darwin t.TempDir() lives under /var, itself a
// symlink to /private/var.
//
// Identity is repo-local config (guarantee 3, D7): user.name =
// "gitfixture <Suite>", user.email = "<Suite>@gitfixture.invalid".
// core.hooksPath points at the empty hooks directory (guarantee 4).
//
// The child environment is built ONLY through gitclient's existing public
// Options (guarantee 5), via hermeticOptions: WithHome,
// WithoutInherited("SSH_AUTH_SOCK"), WithCeiling, and
// WithEnv("GIT_CONFIG_NOSYSTEM", "1") -- there is no private hook into
// gitclient for this package to use instead.
func NewRepo(ctx context.Context, root string, opts RepoOptions) (*Repo, error) {
	if opts.Suite == "" {
		return nil, fmt.Errorf("gitfixture: RepoOptions.Suite is required")
	}
	name := opts.Name
	if name == "" {
		name = defaultRepoName
	} else if err := validateName(name); err != nil {
		return nil, err
	}
	if opts.Bare && opts.InitialCommit {
		return nil, fmt.Errorf("gitfixture: RepoOptions.InitialCommit is not valid with Bare (a bare repository has no work tree to commit from)")
	}
	if opts.Bare && opts.SeparateGitDir != "" {
		return nil, fmt.Errorf("gitfixture: RepoOptions.SeparateGitDir is not valid with Bare")
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("gitfixture: creating root %s: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: resolving root %s: %w", root, err)
	}

	repoDir := filepath.Join(resolvedRoot, name)
	if opts.Name != "" {
		if err := ensureNew(repoDir); err != nil {
			return nil, err
		}
	}
	initOpts := gitclient.InitOptions{Bare: opts.Bare, InitialBranch: opts.InitialBranch}
	if opts.SeparateGitDir != "" {
		gitDir, err := separateGitDirPath(resolvedRoot, repoDir, opts.SeparateGitDir)
		if err != nil {
			return nil, err
		}
		// git creates the git dir itself but not its parents.
		if err := os.MkdirAll(filepath.Dir(gitDir), 0o755); err != nil {
			return nil, fmt.Errorf("gitfixture: creating parent of SeparateGitDir %s: %w", gitDir, err)
		}
		initOpts.SeparateGitDir = gitDir
	}

	for _, d := range []string{filepath.Join(resolvedRoot, "home"), filepath.Join(resolvedRoot, "hooks")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("gitfixture: creating %s: %w", d, err)
		}
	}

	c, err := gitclient.Init(ctx, repoDir, initOpts, hermeticOptions(resolvedRoot)...)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: initializing repo at %s: %w", repoDir, err)
	}

	resolvedRepoDir, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: resolving repo dir %s: %w", repoDir, err)
	}

	if err := configureRepo(ctx, c, resolvedRoot, opts.Suite); err != nil {
		return nil, err
	}

	r := &Repo{
		Dir:    resolvedRepoDir,
		Client: c,
		root:   resolvedRoot,
		suite:  opts.Suite,
	}
	if opts.InitialCommit {
		if _, err := r.Commit(ctx, "initial commit", nil); err != nil {
			return nil, fmt.Errorf("gitfixture: creating the initial commit: %w", err)
		}
	}
	return r, nil
}

// Root returns the symlink-resolved fixture root this repository lives
// under (the directory NewRepo was given). Use it -- or RootPath -- to
// place anything a test needs beside the repository, such as a linked
// worktree target, without reaching for a path outside the fixture.
func (r *Repo) Root() string { return r.root }

// RootPath joins rel onto the fixture root, rejecting any rel that would
// escape it (the same containment rule WriteFile applies to the work
// tree). It only computes the path; it creates nothing.
func (r *Repo) RootPath(rel string) (string, error) {
	return containedPath(r.root, rel)
}

// Options returns a fresh slice holding exactly the hermetic gitclient
// Options this repository's own Client was built with (design §5 guarantee
// 5): the fixture HOME, no inherited SSH_AUTH_SOCK, the symlink-resolved
// fixture root as the discovery ceiling, and no system config. Feed it to
// an Opener seam, or to gitclient.New / Discover / Clone, inside code under
// test that opens its own client at another path (a linked worktree, a
// directory beside the repository) so that client is hermetic too instead
// of re-deriving the option set by hand. Every call builds a new slice, so
// appending to or overwriting a returned slice never affects a later call.
func (r *Repo) Options() []gitclient.Option {
	return hermeticOptions(r.root)
}

// ClientAt returns a client anchored at dir under this repository's
// hermetic option set (gitclient.New(ctx, dir, r.Options()...)). dir MUST
// resolve, after symlink resolution, to the fixture root or a path inside
// it: a dir that does not exist, or that resolves outside the root (a
// separate temp dir, a symlink under the root pointing out of it), is
// rejected with an error naming the root, keeping the package promise that
// a fixture cannot be configured to touch a real path. Use it for a linked
// worktree created under RootPath, or any other directory beside the
// repository, that Client (anchored at Dir) does not cover.
func (r *Repo) ClientAt(ctx context.Context, dir string) (*gitclient.Client, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: resolving %s to check it is inside the fixture root %s: %w", dir, r.root, err)
	}
	if resolved != r.root && !strings.HasPrefix(resolved, r.root+string(filepath.Separator)) {
		return nil, fmt.Errorf("gitfixture: %s (resolves to %s) is outside the fixture root %s", dir, resolved, r.root)
	}
	return gitclient.New(ctx, dir, r.Options()...)
}

// NewSibling creates a further repository named name directly under THIS
// repository's fixture root, sharing its HOME, hooks directory, ceiling
// and (unless opts.Suite is set) Suite -- e.g. a workspace holding an
// "alpha" and a "beta" repository. opts.Name is overridden by name; see
// RepoOptions.Name for the naming rules.
func (r *Repo) NewSibling(ctx context.Context, name string, opts RepoOptions) (*Repo, error) {
	if name == "" {
		return nil, fmt.Errorf("gitfixture: NewSibling requires a name")
	}
	opts.Name = name
	if opts.Suite == "" {
		opts.Suite = r.suite
	}
	return NewRepo(ctx, r.root, opts)
}

// Clone clones this repository (a normal or bare one -- typically a bare
// remote from AddBareRemote) into a new working repository named name
// directly under this repository's fixture root, with the same hermetic
// environment, identity and hooks redirection as every other fixture
// repository. The clone's "origin" is this repository's Dir. name MUST be
// a plain directory name (see RepoOptions.Name) and MUST NOT already
// exist. Note a bare remote created by AddBareRemote is its own fixture
// root, so a clone of it lands under that nested root.
func (r *Repo) Clone(ctx context.Context, name string) (*Repo, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	dir := filepath.Join(r.root, name)
	if err := ensureNew(dir); err != nil {
		return nil, err
	}

	c, h, err := gitclient.Clone(ctx, r.Dir, dir, gitclient.CloneOptions{}, hermeticOptions(r.root)...)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: cloning %s into %s: %w", r.Dir, dir, err)
	}
	if err := h.Wait(); err != nil {
		return nil, fmt.Errorf("gitfixture: cloning %s into %s: %w", r.Dir, dir, err)
	}
	if err := configureRepo(ctx, c, r.root, r.suite); err != nil {
		return nil, err
	}

	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: resolving clone dir %s: %w", dir, err)
	}
	return &Repo{Dir: resolvedDir, Client: c, root: r.root, suite: r.suite}, nil
}

// WriteFile writes content to a file at rel (relative to the repo's
// working tree), creating parent directories as needed. It does not stage
// or commit the file -- pair it with Client.Run("add", ...) directly, or
// use Commit for the common write-stage-commit shape.
func (r *Repo) WriteFile(rel, content string) error {
	path, err := containedPath(r.Dir, rel)
	if err != nil {
		return err
	}
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
		// git's "nothing to commit, working tree clean" refusal --
		// e.g. committing content byte-identical to what's already
		// there -- prints to STDOUT, not stderr, so *GitError's own
		// Stderr field is empty and unhelpful for this specific
		// failure; folding stdout into the wrapped error too gives
		// fixture authors the actual reason instead of an opaque,
		// blank-looking one.
		if out, err := r.Client.Run(ctx, "commit", "-m", msg); err != nil {
			return "", fmt.Errorf("gitfixture: committing: %w (git stdout: %s)", err, strings.TrimSpace(string(out)))
		}
	} else {
		if out, err := r.Client.Run(ctx, "commit", "-m", msg, "--allow-empty"); err != nil {
			return "", fmt.Errorf("gitfixture: committing (empty): %w (git stdout: %s)", err, strings.TrimSpace(string(out)))
		}
	}

	out, err := r.Client.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("gitfixture: resolving HEAD: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// AddBareRemote creates a second, local bare repository nested under this
// fixture's own root (so it inherits the same hermeticity guarantees --
// its own HOME override, ceiling, and GIT_CONFIG_NOSYSTEM=1, all newly
// derived rather than borrowed) and registers it as remote name on r. The
// returned Repo is that bare repository, usable as a "remote" for
// Fetch-style tests.
func (r *Repo) AddBareRemote(ctx context.Context, name string) (*Repo, error) {
	remoteRoot, err := containedPath(filepath.Join(r.root, "remotes"), name)
	if err != nil {
		return nil, err
	}
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
