package spool

import (
	"os"
	"syscall"
)

// File is the part of an open file the Sink needs.
type File interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// FS is the lower seam under FileSink: the file-system operations it uses,
// so tests can inject faults and assert their order. Swap contract: an
// implementation MUST behave like the os package for these calls (Rename
// atomic within one directory, Lstat not following symlinks, OpenFile
// honouring O_EXCL and O_NOFOLLOW).
type FS interface {
	MkdirAll(path string, perm os.FileMode) error
	OpenFile(name string, flag int, perm os.FileMode) (File, error)
	Rename(oldpath, newpath string) error
	Lstat(name string) (os.FileInfo, error)
	// Owner returns the uid owning fi, and false when it cannot be told.
	Owner(fi os.FileInfo) (uid int, ok bool)
	Remove(name string) error
	// Uid is the current process's uid.
	Uid() int
}

// OSFS is the real FS.
type OSFS struct{}

func (OSFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (OSFS) Rename(oldpath, newpath string) error         { return os.Rename(oldpath, newpath) }
func (OSFS) Lstat(name string) (os.FileInfo, error)       { return os.Lstat(name) }
func (OSFS) Remove(name string) error                     { return os.Remove(name) }
func (OSFS) Uid() int                                     { return os.Getuid() }

// OpenFile implements FS.
func (OSFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Owner implements FS.
func (OSFS) Owner(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
