package gitfixture

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Template is a prebuilt fixture repository that can be copied cheaply
// into any number of fresh fixture roots, so a test package pays the
// git-process cost of building a repository (init, config, commits) ONCE --
// typically in TestMain -- instead of once per test.
//
// Build one with NewTemplate; create a repository from it with
// (*Template).NewRepo (or gittest.FromTemplate in a test). A Template is
// read-only after NewTemplate returns, so it is safe to share across
// parallel tests.
//
// What a copy carries and what it does not:
//   - The repository directory is copied byte for byte (objects, refs,
//     config, work tree, mode bits and symlinks), so history, branches and
//     committed files are identical to the template's.
//   - The one setting the fixture bakes as an ABSOLUTE path,
//     core.hooksPath, is rewritten to the new root's hooks directory, and
//     the forensic identity (user.name / user.email) is re-stamped when the
//     copy asks for a different Suite. A copy is therefore hermetic under
//     ITS OWN root, never the template's.
//   - Anything else that records an absolute path would go stale in a copy,
//     so NewTemplate rejects it: remotes (do not call AddBareRemote or
//     Clone on the template; add them to the copy), linked worktrees, and
//     SeparateGitDir.
type Template struct {
	name  string // repository directory name under the template root
	dir   string // resolved absolute path of the template repository
	suite string // Suite the template was built with
	bare  bool
}

// NewTemplate builds the template repository under root (caller-owned,
// e.g. a directory made in TestMain and removed after m.Run) exactly as
// NewRepo(ctx, root, opts) would, then calls populate -- when non-nil --
// with the Repo so it can commit files and create branches. It then checks
// the result holds no absolute path a copy could not relocate (see
// Template). opts.SeparateGitDir is not supported.
func NewTemplate(ctx context.Context, root string, opts RepoOptions, populate func(ctx context.Context, r *Repo) error) (*Template, error) {
	if opts.SeparateGitDir != "" {
		return nil, fmt.Errorf("gitfixture: RepoOptions.SeparateGitDir is not supported for a Template (its gitdir file holds an absolute path)")
	}
	r, err := NewRepo(ctx, root, opts)
	if err != nil {
		return nil, err
	}
	if populate != nil {
		if err := populate(ctx, r); err != nil {
			return nil, fmt.Errorf("gitfixture: populating the template: %w", err)
		}
	}
	if out, err := r.Client.Run(ctx, "config", "--get-regexp", `^remote\.`); err == nil && strings.TrimSpace(string(out)) != "" {
		return nil, fmt.Errorf("gitfixture: a Template must not have remotes (their URLs are absolute paths); add them to the copy instead")
	}
	if _, err := os.Lstat(filepath.Join(gitCommonDirOf(r), "worktrees")); err == nil {
		return nil, fmt.Errorf("gitfixture: a Template must not have linked worktrees (they record absolute paths); add them to the copy instead")
	}
	name := opts.Name
	if name == "" {
		name = defaultRepoName
	}
	return &Template{name: name, dir: r.Dir, suite: r.suite, bare: opts.Bare}, nil
}

// gitCommonDirOf returns the git directory of r: Dir/.git for a normal
// repository, Dir itself for a bare one.
func gitCommonDirOf(r *Repo) string {
	if fi, err := os.Stat(filepath.Join(r.Dir, ".git")); err == nil && fi.IsDir() {
		return filepath.Join(r.Dir, ".git")
	}
	return r.Dir
}

// NewRepo copies the template into a fresh fixture rooted at root (which
// the caller owns and which MAY be at any absolute path, different from
// the template's), laying it out exactly as the package-level NewRepo
// does: the repository at <root>/<name>, HOME at <root>/home and the empty
// hooks directory at <root>/hooks. suite names the forensic identity of
// the copy; empty keeps the template's. Costs a directory copy plus at
// most three git config calls, versus a full init, config and commits.
func (t *Template) NewRepo(ctx context.Context, root, suite string) (*Repo, error) {
	if suite == "" {
		suite = t.suite
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("gitfixture: creating root %s: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: resolving root %s: %w", root, err)
	}
	repoDir := filepath.Join(resolvedRoot, t.name)
	if err := ensureNew(repoDir); err != nil {
		return nil, err
	}
	for _, d := range []string{filepath.Join(resolvedRoot, "home"), filepath.Join(resolvedRoot, "hooks")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("gitfixture: creating %s: %w", d, err)
		}
	}
	if err := copyTree(t.dir, repoDir); err != nil {
		return nil, fmt.Errorf("gitfixture: copying template %s to %s: %w", t.dir, repoDir, err)
	}
	c, err := newClient(ctx, repoDir, resolvedRoot)
	if err != nil {
		return nil, fmt.Errorf("gitfixture: opening the copy at %s: %w", repoDir, err)
	}
	// Relocate core.hooksPath; re-stamp identity only if it differs.
	if _, err := c.Run(ctx, "config", "core.hooksPath", filepath.Join(resolvedRoot, "hooks")); err != nil {
		return nil, fmt.Errorf("gitfixture: setting core.hooksPath: %w", err)
	}
	if suite != t.suite {
		if _, err := c.Run(ctx, "config", "user.name", "gitfixture "+suite); err != nil {
			return nil, fmt.Errorf("gitfixture: setting user.name: %w", err)
		}
		if _, err := c.Run(ctx, "config", "user.email", suite+"@gitfixture.invalid"); err != nil {
			return nil, fmt.Errorf("gitfixture: setting user.email: %w", err)
		}
	}
	return &Repo{Dir: repoDir, Client: c, root: resolvedRoot, suite: suite}, nil
}

// copyTree copies the directory tree src to dst (which must not exist),
// preserving mode bits and recreating symlinks verbatim.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.Mkdir(target, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		default:
			return fmt.Errorf("unsupported file type %s at %s", info.Mode().Type(), p)
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	_, err = io.Copy(out, in)
	return err
}
