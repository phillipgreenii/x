package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Sweep limits. A run file is deleted when older than SweepMaxAge; then, while
// a signal directory holds more than SweepMaxBytes or SweepMaxFiles, the
// oldest are deleted. Nothing modified within SweepMinAge (or in the future)
// is ever deleted, and the sweep itself runs at most once per SweepInterval.
const (
	SweepMaxAge   = 24 * time.Hour
	SweepMaxBytes = 200 << 20
	SweepMaxFiles = 5000
	SweepMinAge   = 10 * time.Minute
	SweepInterval = 10 * time.Minute
	// SweepMarker is the throttle marker file inside the spool directory.
	SweepMarker = ".sweep"
)

// Values of the pn.spool.swept_files "reason" attribute. The "spool"
// attribute is Role.String().
const (
	ReasonAge = "age"
	ReasonCap = "cap"
)

// SweepFS is the FS seam plus the directory listing and marker touch the
// sweep needs. OSFS implements it.
type SweepFS interface {
	FS
	// ReadDir returns the entry names of dir.
	ReadDir(dir string) ([]string, error)
	// Touch sets name's mtime to t, creating it with perm when missing. It
	// MUST NOT follow a symlink at name.
	Touch(name string, perm os.FileMode, t time.Time) error
}

// ReadDir implements SweepFS.
func (OSFS) ReadDir(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(ents))
	for i, e := range ents {
		names[i] = e.Name()
	}
	return names, nil
}

// Touch implements SweepFS.
func (OSFS) Touch(name string, perm os.FileMode, t time.Time) error {
	fi, err := os.Lstat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	case err != nil:
		return err
	case !fi.Mode().IsRegular():
		return fmt.Errorf("spool: %s is not a regular file", name)
	}
	return os.Chtimes(name, t, t)
}

// SweepConfig configures Sweep.
type SweepConfig struct {
	// Dir is the spool directory holding the traces and metrics directories.
	Dir  string
	Role Role
	// Extensions are the file extensions (with leading dot) of the encoders
	// writing into the spool; only run files with one of them are swept.
	Extensions []string
	// FS defaults to OSFS; Now defaults to time.Now.
	FS  SweepFS
	Now func() time.Time
}

// SweepResult carries the counts the caller records once the meter provider
// exists: pn.spool.swept_files{spool=Role.String(), reason=age|cap}.
type SweepResult struct {
	// Ran is false when the throttle marker skipped the sweep.
	Ran bool
	// Age and Cap count successful removals only (a file the collector
	// removed first is neither).
	Age, Cap int
	// StaleTmp counts removed abandoned scratch files. They never reached
	// the collector, so they are not data loss and have no counter.
	StaleTmp int
}

// Sweep applies the retention rules to <Dir>/traces and <Dir>/metrics. It
// never fails the caller: every error leaves the file in place and uncounted.
// Only the two exact signal directories are scanned, with Lstat and no
// symlink following; names that do not match the run-file pattern are left
// alone.
func Sweep(cfg SweepConfig) SweepResult {
	fsys := cfg.FS
	if fsys == nil {
		fsys = OSFS{}
	}
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	t := now()
	var res SweepResult

	marker := filepath.Join(cfg.Dir, SweepMarker)
	// A future marker mtime gives a negative age, which counts as "just ran".
	if fi, err := fsys.Lstat(marker); err == nil && t.Sub(fi.ModTime()) <= SweepInterval {
		return res
	}
	res.Ran = true

	re := runFileRegexp(cfg.Extensions)
	for _, sig := range []string{SignalTraces, SignalMetrics} {
		sweepDir(fsys, filepath.Join(cfg.Dir, sig), re, t, &res)
	}
	// Touch after the sweep; a missing spool directory is not created here.
	if fi, err := fsys.Lstat(cfg.Dir); err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
		_ = fsys.Touch(marker, cfg.Role.FileMode(), t)
	}
	return res
}

// runFileRegexp matches <service>-<utc-ts>-<pid>-<rand>-<seq><ext> as written
// by the exporters (service is sanitized there and contains no '-').
func runFileRegexp(exts []string) *regexp.Regexp {
	alts := make([]string, 0, len(exts))
	for _, e := range exts {
		alts = append(alts, regexp.QuoteMeta(e))
	}
	if len(alts) == 0 {
		return regexp.MustCompile(`\A\z.\A`) // matches nothing
	}
	return regexp.MustCompile(`\A[A-Za-z0-9._]+-\d{8}T\d{6}Z-\d+-[0-9a-f]{8}-\d+(?:` + strings.Join(alts, "|") + `)\z`)
}

type runFile struct {
	path  string
	size  int64
	mtime time.Time
}

func sweepDir(fsys SweepFS, dir string, re *regexp.Regexp, now time.Time, res *SweepResult) {
	if fi, err := fsys.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	names, err := fsys.ReadDir(dir)
	if err != nil {
		return
	}
	var files []runFile
	for _, n := range names {
		isTmp := strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".tmp") && re.MatchString(n[1:len(n)-len(".tmp")])
		if !isTmp && !re.MatchString(n) {
			continue
		}
		p := filepath.Join(dir, n)
		fi, err := fsys.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if isTmp {
			if now.Sub(fi.ModTime()) > SweepMinAge && removed(fsys, p) {
				res.StaleTmp++
			}
			continue
		}
		files = append(files, runFile{p, fi.Size(), fi.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mtime.Equal(files[j].mtime) {
			return files[i].mtime.Before(files[j].mtime)
		}
		return files[i].path < files[j].path
	})

	// Age pass. Files are oldest first, so the first one not older than
	// SweepMaxAge ends it; deletable also requires being older than SweepMinAge.
	var total int64
	count := 0
	kept := files[:0:0]
	for _, f := range files {
		if now.Sub(f.mtime) > SweepMaxAge {
			gone, counted := remove(fsys, f.path)
			if counted {
				res.Age++
			}
			if gone {
				continue
			}
		}
		kept = append(kept, f)
		total += f.size
		count++
	}
	// Cap pass, oldest first, never touching a recent (or future) file.
	for _, f := range kept {
		if total <= SweepMaxBytes && count <= SweepMaxFiles {
			break
		}
		if now.Sub(f.mtime) <= SweepMinAge {
			break // sorted by mtime: everything after is at least as recent
		}
		gone, counted := remove(fsys, f.path)
		if counted {
			res.Cap++
		}
		if gone {
			total -= f.size
			count--
		}
	}
}

// remove deletes p. gone: the file no longer exists; counted: this call
// removed it (ENOENT is gone but not counted).
func remove(fsys SweepFS, p string) (gone, counted bool) {
	err := fsys.Remove(p)
	switch {
	case err == nil:
		return true, true
	case errors.Is(err, os.ErrNotExist):
		return true, false
	}
	return false, false
}

func removed(fsys SweepFS, p string) bool {
	_, counted := remove(fsys, p)
	return counted
}
