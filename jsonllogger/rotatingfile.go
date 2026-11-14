package jsonllogger

import (
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile is an append-only log file that survives an external rename or
// removal (logrotate, a janitor, a test cleaning up) and caps its own size.
//
// Before each write it checks that path still names the open file; if the file
// was moved or deleted it reopens (creating) path. When a write would take the
// file past MaxBytes the file is renamed to path+".1" (replacing any older
// backup) and a fresh file is started. MaxBytes <= 0 disables the size cap.
// It is safe for concurrent use and satisfies io.WriteCloser.
type RotatingFile struct {
	path     string
	maxBytes int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenRotatingFile opens (creating it and its directory) path for appending.
func OpenRotatingFile(path string, maxBytes int64) (*RotatingFile, error) {
	r := &RotatingFile{path: path, maxBytes: maxBytes}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write appends p, reopening or rotating first when needed.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if disk, err := os.Stat(r.path); err != nil || !isSame(disk, r.f) {
		// Moved or removed underneath us: follow the path, not the old inode.
		_ = r.f.Close()
		r.f = nil
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		_ = r.f.Close()
		r.f = nil
		if err := os.Rename(r.path, r.path+".1"); err != nil {
			return 0, err
		}
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func isSame(disk os.FileInfo, f *os.File) bool {
	open, err := f.Stat()
	return err == nil && os.SameFile(disk, open)
}

// Close closes the underlying file; a later Write reopens it.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
