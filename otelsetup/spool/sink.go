package spool

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Sink is the destination seam: it publishes one finished file. Swap
// contract: Publish MUST make the file visible under signal/name all at once
// (a reader never sees a partial file), MUST NOT retain data, and returns an
// error wrapping ErrTransient only for a failure that a later Publish may
// survive.
type Sink interface {
	Publish(signal, name string, data []byte) error
}

// ErrTransient marks a Publish failure that does not mean the destination is
// broken (for example the sweep removed the scratch file before the rename).
var ErrTransient = errors.New("spool: transient publish error")

// ErrUnsafeDir marks a spool directory that is a symlink, not a directory or
// owned by the wrong uid.
var ErrUnsafeDir = errors.New("spool: unsafe spool directory")

// FileSink publishes files into <Dir>/<signal>/ atomically: it writes
// .<name>.tmp (O_EXCL|O_NOFOLLOW), syncs it, then renames it to <name> in the
// same directory. The temporary name never matches a *.<ext> glob.
type FileSink struct {
	dir  string
	role Role
	fs   FS
}

// NewFileSink returns a Sink writing under dir. A nil fs selects OSFS.
func NewFileSink(dir string, role Role, fs FS) *FileSink {
	if fs == nil {
		fs = OSFS{}
	}
	return &FileSink{dir: dir, role: role, fs: fs}
}

// Publish implements Sink.
func (s *FileSink) Publish(signal, name string, data []byte) error {
	if signal != SignalTraces && signal != SignalMetrics {
		return fmt.Errorf("spool: unknown signal %q", signal)
	}
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("spool: invalid file name %q", name)
	}
	if len(data) == 0 {
		return errors.New("spool: refusing to publish an empty file")
	}
	sigDir := filepath.Join(s.dir, signal)
	for _, d := range []string{s.dir, sigDir} {
		if err := s.ensureDir(d); err != nil {
			return err
		}
	}
	tmp := filepath.Join(sigDir, "."+name+".tmp")
	final := filepath.Join(sigDir, name)

	const flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL | syscall.O_NOFOLLOW
	f, err := s.fs.OpenFile(tmp, flags, s.role.FileMode())
	if err != nil {
		return fmt.Errorf("spool: create %s: %w", tmp, err)
	}
	if err := writeSync(f, data); err != nil {
		_ = f.Close()
		_ = s.fs.Remove(tmp)
		return fmt.Errorf("spool: write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = s.fs.Remove(tmp)
		return fmt.Errorf("spool: close %s: %w", tmp, err)
	}
	if err := s.fs.Rename(tmp, final); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The sweep removed the scratch file: one lost flush, not a broken spool.
			return fmt.Errorf("%w: rename %s: %v", ErrTransient, tmp, err)
		}
		_ = s.fs.Remove(tmp)
		return fmt.Errorf("spool: rename %s: %w", tmp, err)
	}
	return nil
}

func writeSync(f File, data []byte) error {
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return f.Sync()
}

// ensureDir creates dir if missing, then insists it is a real directory (not a
// symlink) owned by the role's expected uid.
func (s *FileSink) ensureDir(dir string) error {
	fi, err := s.fs.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.fs.MkdirAll(dir, s.role.DirMode()); err != nil {
			return fmt.Errorf("spool: create directory %s: %w", dir, err)
		}
		fi, err = s.fs.Lstat(dir)
	}
	if err != nil {
		return fmt.Errorf("spool: stat directory %s: %w", dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a plain directory", ErrUnsafeDir, dir)
	}
	want := s.role.ExpectedUID(s.fs.Uid())
	uid, ok := s.fs.Owner(fi)
	if !ok || uid != want {
		return fmt.Errorf("%w: %s is not owned by uid %d", ErrUnsafeDir, dir, want)
	}
	return nil
}
