package spool

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// fakeFS is an in-memory FS recording operations in order, with injectable
// faults.
type fakeFS struct {
	mu    sync.Mutex
	ops   []string
	files map[string][]byte
	dirs  map[string]fakeDir
	uid   int

	openErr   error // returned by OpenFile of a .tmp
	writeErr  error
	shortN    int // when >0, Write reports this many bytes and no error
	syncErr   error
	renameErr error
	perms     map[string]os.FileMode
	renames   int
}

type fakeDir struct {
	mode  os.FileMode
	owner int
	link  bool
}

func newFakeFS(uid int) *fakeFS {
	return &fakeFS{files: map[string][]byte{}, dirs: map[string]fakeDir{}, perms: map[string]os.FileMode{}, uid: uid}
}

func (f *fakeFS) log(s string) { f.ops = append(f.ops, s) }

type fakeInfo struct {
	name string
	d    fakeDir
}

func (i fakeInfo) Name() string { return i.name }
func (i fakeInfo) Size() int64  { return 0 }
func (i fakeInfo) Mode() fs.FileMode {
	if i.d.link {
		return os.ModeSymlink | 0o777
	}
	return os.ModeDir | i.d.mode
}
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return !i.d.link }
func (i fakeInfo) Sys() any           { return nil }

func (f *fakeFS) MkdirAll(p string, perm os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("mkdir " + p)
	f.perms[p] = perm
	f.dirs[p] = fakeDir{mode: perm, owner: f.uid}
	return nil
}

func (f *fakeFS) Lstat(p string) (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.dirs[p]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
	}
	return fakeInfo{p, d}, nil
}

func (f *fakeFS) Owner(fi os.FileInfo) (int, bool) { return fi.(fakeInfo).d.owner, true }
func (f *fakeFS) Uid() int                         { return f.uid }

func (f *fakeFS) OpenFile(p string, flag int, perm os.FileMode) (File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("open " + p)
	if f.openErr != nil {
		return nil, f.openErr
	}
	if flag&os.O_EXCL == 0 || flag&syscall.O_NOFOLLOW == 0 {
		return nil, errors.New("fakeFS: missing O_EXCL|O_NOFOLLOW")
	}
	if _, ok := f.files[p]; ok {
		return nil, fs.ErrExist
	}
	f.files[p] = nil
	f.perms[p] = perm
	return &fakeFile{fs: f, path: p}, nil
}

func (f *fakeFS) Rename(a, b string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("rename " + a + " " + b)
	f.renames++
	if f.renameErr != nil {
		return f.renameErr
	}
	f.files[b] = f.files[a]
	f.perms[b] = f.perms[a]
	delete(f.files, a)
	return nil
}

func (f *fakeFS) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log("remove " + p)
	delete(f.files, p)
	return nil
}

// published returns the final (non-tmp) files.
func (f *fakeFS) published() map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]byte{}
	for p, b := range f.files {
		if !strings.Contains(p[strings.LastIndex(p, "/")+1:], ".tmp") && !strings.HasPrefix(p[strings.LastIndex(p, "/")+1:], ".") {
			out[p] = b
		}
	}
	return out
}

func (f *fakeFS) opsList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

type fakeFile struct {
	fs   *fakeFS
	path string
}

func (w *fakeFile) Write(p []byte) (int, error) {
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	w.fs.log("write " + w.path)
	if w.fs.writeErr != nil {
		return 0, w.fs.writeErr
	}
	if w.fs.shortN > 0 {
		w.fs.files[w.path] = append(w.fs.files[w.path], p[:w.fs.shortN]...)
		return w.fs.shortN, nil
	}
	w.fs.files[w.path] = append(w.fs.files[w.path], p...)
	return len(p), nil
}

func (w *fakeFile) Sync() error {
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	w.fs.log("sync " + w.path)
	return w.fs.syncErr
}

func (w *fakeFile) Close() error {
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	w.fs.log("close " + w.path)
	return nil
}
