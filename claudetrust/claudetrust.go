// Package claudetrust ensures working directories are pre-trusted in Claude
// Code's .claude.json so an automated `claude` launch does not stall on the
// interactive folder-trust prompt. That prompt blocks the REPL from starting,
// so trust is guaranteed before launch rather than detected after.
//
// Seeded from the ccpool trust package in nix-agent-support
// (packages/ccpool/internal/trust).
//
// The file is Claude-managed: the write is merge-only (it sets
// .projects[cwd].hasTrustDialogAccepted and keeps every other key), happens
// only when a key is missing, and is atomic (temp file plus rename). Claude
// Code rewrites the file on its own schedule, so the read-merge-rename has an
// unavoidable lost-update window; a lost write degrades to the trust-prompt
// hang, which is why the file is rewritten only when something is missing.
package claudetrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// resolve returns cwd with every symlink resolved, or cwd itself when it does
// not resolve. Claude keys .projects by the resolved path (on macOS /tmp is
// /private/tmp), so trust is read and written under that form.
func resolve(cwd string) string {
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		return r
	}
	return cwd
}

const trustKey = "hasTrustDialogAccepted"

// Ensure sets .projects[cwd].hasTrustDialogAccepted = true in the JSON at
// path for every cwd, preserving all other content and creating the file when
// absent. Each cwd is resolved through symlinks first (kept as given when it
// does not resolve), and a symlinked path is written through, not replaced. It does
// not rewrite the file when every cwd is already trusted.
func Ensure(path string, cwds ...string) error {
	cwds = append([]string(nil), cwds...)
	root, err := load(path)
	if err != nil {
		return err
	}
	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	changed := false
	for i, cwd := range cwds {
		cwd = resolve(cwd)
		cwds[i] = cwd
		proj, _ := projects[cwd].(map[string]any)
		if proj == nil {
			proj = map[string]any{}
		}
		if proj[trustKey] == true {
			continue
		}
		proj[trustKey] = true
		projects[cwd] = proj
		changed = true
	}
	if !changed {
		return nil
	}
	root["projects"] = projects

	// Write, read back, and retry once: a concurrent Claude write can drop ours.
	for attempt := 0; attempt < 2; attempt++ {
		if err := atomicWriteJSON(path, root); err != nil {
			return err
		}
		if allTrusted(path, cwds) {
			return nil
		}
	}
	return fmt.Errorf("claudetrust: trust write to %s did not stick (concurrent Claude write?)", path)
}

// IsTrusted reports whether cwd (resolved through symlinks) is marked hasTrustDialogAccepted in the JSON
// at path. A missing file is not an error: it reports false.
func IsTrusted(path, cwd string) (bool, error) {
	root, err := load(path)
	if err != nil {
		return false, err
	}
	cwd = resolve(cwd)
	projects, _ := root["projects"].(map[string]any)
	proj, _ := projects[cwd].(map[string]any)
	return proj[trustKey] == true, nil
}

func allTrusted(path string, cwds []string) bool {
	for _, cwd := range cwds {
		if ok, err := IsTrusted(path, cwd); err != nil || !ok {
			return false
		}
	}
	return true
}

// load reads path as a JSON object; a missing file is an empty object.
func load(path string) (map[string]any, error) {
	root := map[string]any{}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return root, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claudetrust: read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("claudetrust: parse %s: %w", path, err)
	}
	return root, nil
}

func atomicWriteJSON(path string, v any) error {
	// Write the file a symlink points at, so the link survives the rename.
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.tmp-*")
	if err != nil {
		return fmt.Errorf("claudetrust: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
