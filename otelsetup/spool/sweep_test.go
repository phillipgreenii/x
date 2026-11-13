package spool

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var sweepNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

type sfEntry struct {
	size    int64
	mtime   time.Time
	symlink bool
	dir     bool
}

type sweepFS struct {
	mu        sync.Mutex
	e         map[string]sfEntry
	removeErr map[string]error
	onRemove  func(p string)
	touched   map[string]time.Time
}

func newSweepFS() *sweepFS {
	f := &sweepFS{e: map[string]sfEntry{}, removeErr: map[string]error{}, touched: map[string]time.Time{}}
	f.e["/sp"] = sfEntry{dir: true}
	f.e["/sp/traces"] = sfEntry{dir: true}
	f.e["/sp/metrics"] = sfEntry{dir: true}
	return f
}

type sfInfo struct {
	n string
	e sfEntry
}

func (i sfInfo) Name() string       { return i.n }
func (i sfInfo) Size() int64        { return i.e.size }
func (i sfInfo) ModTime() time.Time { return i.e.mtime }
func (i sfInfo) IsDir() bool        { return i.e.dir }
func (i sfInfo) Sys() any           { return nil }
func (i sfInfo) Mode() fs.FileMode {
	switch {
	case i.e.symlink:
		return os.ModeSymlink | 0o777
	case i.e.dir:
		return os.ModeDir | 0o700
	}
	return 0o600
}

func (f *sweepFS) MkdirAll(string, os.FileMode) error { return nil }
func (f *sweepFS) OpenFile(string, int, os.FileMode) (File, error) {
	return nil, errors.New("unused")
}
func (f *sweepFS) Rename(string, string) error   { return errors.New("unused") }
func (f *sweepFS) Owner(os.FileInfo) (int, bool) { return 0, true }
func (f *sweepFS) Uid() int                      { return 0 }
func (f *sweepFS) Lstat(p string) (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.e[p]
	if !ok {
		return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
	}
	return sfInfo{p, e}, nil
}

func (f *sweepFS) Remove(p string) error {
	if f.onRemove != nil {
		f.onRemove(p)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.removeErr[p]; err != nil {
		return err
	}
	if _, ok := f.e[p]; !ok {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
	}
	delete(f.e, p)
	return nil
}

func (f *sweepFS) ReadDir(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for p := range f.e {
		if strings.HasPrefix(p, dir+"/") && !strings.Contains(p[len(dir)+1:], "/") {
			out = append(out, p[len(dir)+1:])
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *sweepFS) Touch(p string, _ os.FileMode, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.e[p] = sfEntry{mtime: t}
	f.touched[p] = t
	return nil
}

func (f *sweepFS) has(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.e[p]
	return ok
}

func runName(i int, ext string) string {
	return "pn-20261112T100000Z-" + itoa(1000+i) + "-deadbeef-" + itoa(i) + ext
}

func itoa(i int) string { return strconv.Itoa(i) }

func (f *sweepFS) add(p string, size int64, age time.Duration) {
	f.e[p] = sfEntry{size: size, mtime: sweepNow.Add(-age)}
}

func cfgFor(f *sweepFS) SweepConfig {
	return SweepConfig{Dir: "/sp", Role: RoleUser, Extensions: []string{".jsonl", ".pb"}, FS: f, Now: func() time.Time { return sweepNow }}
}

func TestSweepAgeBoundary(t *testing.T) {
	f := newSweepFS()
	f.add("/sp/traces/"+runName(1, ".jsonl"), 1, SweepMaxAge-time.Second)
	f.add("/sp/traces/"+runName(2, ".jsonl"), 1, SweepMaxAge)
	f.add("/sp/traces/"+runName(3, ".jsonl"), 1, SweepMaxAge+time.Second)
	f.add("/sp/metrics/"+runName(4, ".pb"), 1, 48*time.Hour)
	r := Sweep(cfgFor(f))
	if !r.Ran || r.Age != 2 || r.Cap != 0 {
		t.Fatalf("got %+v", r)
	}
	if !f.has("/sp/traces/"+runName(1, ".jsonl")) || !f.has("/sp/traces/"+runName(2, ".jsonl")) || f.has("/sp/traces/"+runName(3, ".jsonl")) {
		t.Fatal("wrong survivors")
	}
}

func TestSweepCapBytesOldestFirst(t *testing.T) {
	f := newSweepFS()
	// Three 100 MiB files: 300 MiB > 200 MiB, so only the oldest goes.
	for i, age := range []time.Duration{3 * time.Hour, 2 * time.Hour, time.Hour} {
		f.add("/sp/traces/"+runName(i, ".jsonl"), 100<<20, age)
	}
	r := Sweep(cfgFor(f))
	if r.Cap != 1 || r.Age != 0 || f.has("/sp/traces/"+runName(0, ".jsonl")) || !f.has("/sp/traces/"+runName(1, ".jsonl")) {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepExactlyAtByteCapKept(t *testing.T) {
	f := newSweepFS()
	f.add("/sp/traces/"+runName(0, ".jsonl"), 100<<20, 3*time.Hour)
	f.add("/sp/traces/"+runName(1, ".jsonl"), 100<<20, 2*time.Hour)
	if r := Sweep(cfgFor(f)); r.Cap != 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepCapFileCount(t *testing.T) {
	for _, tc := range []struct{ n, want int }{{SweepMaxFiles, 0}, {SweepMaxFiles + 3, 3}} {
		f := newSweepFS()
		for i := 0; i < tc.n; i++ {
			f.add("/sp/traces/"+runName(i, ".jsonl"), 1, time.Hour+time.Duration(tc.n-i)*time.Second)
		}
		r := Sweep(cfgFor(f))
		if r.Cap != tc.want {
			t.Fatalf("n=%d got %+v", tc.n, r)
		}
		for i := 0; i < tc.want; i++ { // oldest are the lowest i
			if f.has("/sp/traces/" + runName(i, ".jsonl")) {
				t.Fatalf("file %d should be gone", i)
			}
		}
	}
}

func TestSweepAgeAndCapCountedSeparately(t *testing.T) {
	f := newSweepFS()
	f.add("/sp/traces/"+runName(0, ".jsonl"), 1, 30*time.Hour)
	f.add("/sp/traces/"+runName(1, ".jsonl"), 150<<20, 3*time.Hour)
	f.add("/sp/traces/"+runName(2, ".jsonl"), 150<<20, 2*time.Hour)
	r := Sweep(cfgFor(f))
	if r.Age != 1 || r.Cap != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepRecentAndFutureKept(t *testing.T) {
	f := newSweepFS()
	f.add("/sp/traces/"+runName(0, ".jsonl"), 300<<20, 5*time.Minute)
	f.add("/sp/traces/"+runName(1, ".jsonl"), 300<<20, SweepMinAge)
	f.add("/sp/traces/"+runName(2, ".jsonl"), 300<<20, -48*time.Hour)
	r := Sweep(cfgFor(f))
	if r.Age != 0 || r.Cap != 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepTmp(t *testing.T) {
	f := newSweepFS()
	stale := "/sp/traces/." + runName(1, ".jsonl") + ".tmp"
	fresh := "/sp/traces/." + runName(2, ".jsonl") + ".tmp"
	f.add(stale, 1, SweepMinAge+time.Second)
	f.add(fresh, 1, time.Minute)
	r := Sweep(cfgFor(f))
	if f.has(stale) || !f.has(fresh) || r.StaleTmp != 1 || r.Age != 0 || r.Cap != 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepForeignAndWrongExtensionKept(t *testing.T) {
	f := newSweepFS()
	for _, n := range []string{"notes.txt", runName(1, ".other"), ".sweep2", ".foo.tmp"} {
		f.add("/sp/traces/"+n, 1, 100*time.Hour)
	}
	if r := Sweep(cfgFor(f)); r.Age+r.Cap+r.StaleTmp != 0 {
		t.Fatalf("got %+v", r)
	}
	if len(f.e) != 3+4+1 {
		t.Fatalf("entries changed: %d", len(f.e))
	}
}

func TestSweepSymlinkAndSubdirKept(t *testing.T) {
	f := newSweepFS()
	n := "/sp/traces/" + runName(1, ".jsonl")
	f.e[n] = sfEntry{symlink: true, mtime: sweepNow.Add(-100 * time.Hour)}
	d := "/sp/traces/" + runName(2, ".jsonl")
	f.e[d] = sfEntry{dir: true, mtime: sweepNow.Add(-100 * time.Hour)}
	// A symlinked signal directory is not scanned at all.
	f.e["/sp/metrics"] = sfEntry{symlink: true, dir: true}
	f.add("/sp/metrics/"+runName(3, ".jsonl"), 1, 100*time.Hour)
	if r := Sweep(cfgFor(f)); r.Age != 0 || !f.has(n) || !f.has(d) || !f.has("/sp/metrics/"+runName(3, ".jsonl")) {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepENOENTIsSuccessAndUncounted(t *testing.T) {
	f := newSweepFS()
	a := "/sp/traces/" + runName(1, ".jsonl")
	b := "/sp/traces/" + runName(2, ".jsonl")
	f.add(a, 1, 30*time.Hour)
	f.add(b, 1, 30*time.Hour)
	f.onRemove = func(p string) { // the collector deletes it first
		if p == a {
			f.mu.Lock()
			delete(f.e, a)
			f.mu.Unlock()
		}
	}
	r := Sweep(cfgFor(f))
	if r.Age != 1 {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepOtherRemoveErrorUncounted(t *testing.T) {
	f := newSweepFS()
	a := "/sp/traces/" + runName(1, ".jsonl")
	f.add(a, 1, 30*time.Hour)
	f.removeErr[a] = os.ErrPermission
	if r := Sweep(cfgFor(f)); r.Age != 0 || !f.has(a) {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepConcurrentCountsEachFileOnce(t *testing.T) {
	f := newSweepFS()
	for i := 0; i < 50; i++ {
		f.add("/sp/traces/"+runName(i, ".jsonl"), 1, 30*time.Hour)
	}
	var wg sync.WaitGroup
	res := make([]SweepResult, 4)
	for i := range res {
		wg.Add(1)
		go func() { defer wg.Done(); res[i] = Sweep(cfgFor(f)) }()
	}
	wg.Wait()
	total := 0
	for _, r := range res {
		total += r.Age
	}
	if total != 50 {
		t.Fatalf("counted %d, want 50", total)
	}
}

func TestSweepThrottle(t *testing.T) {
	f := newSweepFS()
	now := sweepNow
	cfg := cfgFor(f)
	cfg.Now = func() time.Time { return now }
	if r := Sweep(cfg); !r.Ran {
		t.Fatal("first sweep must run")
	}
	if f.touched["/sp/.sweep"] != sweepNow {
		t.Fatal("marker not touched")
	}
	f.add("/sp/traces/"+runName(1, ".jsonl"), 1, 30*time.Hour)
	f.e["/sp/traces/"+runName(1, ".jsonl")] = sfEntry{size: 1, mtime: now.Add(-30 * time.Hour)}
	now = sweepNow.Add(SweepInterval)
	if r := Sweep(cfg); r.Ran || r.Age != 0 {
		t.Fatalf("inside interval got %+v", r)
	}
	now = sweepNow.Add(SweepInterval + time.Second)
	if r := Sweep(cfg); !r.Ran || r.Age != 1 {
		t.Fatalf("after interval got %+v", r)
	}
}

func TestSweepFutureMarkerMeansJustRan(t *testing.T) {
	f := newSweepFS()
	f.e["/sp/.sweep"] = sfEntry{mtime: sweepNow.Add(24 * time.Hour)}
	if r := Sweep(cfgFor(f)); r.Ran {
		t.Fatal("future marker must throttle")
	}
}

func TestSweepMissingDirDoesNotCreateMarker(t *testing.T) {
	f := &sweepFS{e: map[string]sfEntry{}, removeErr: map[string]error{}, touched: map[string]time.Time{}}
	if r := Sweep(cfgFor(f)); !r.Ran || len(f.touched) != 0 {
		t.Fatalf("got %+v", r)
	}
}

func TestSweepMatchesWhatExportersWrite(t *testing.T) {
	p := &publisher{service: sanitizeService("pg-nix-log"), enc: JSONEncoder{}, now: func() time.Time { return sweepNow }, pid: 4242}
	n, err := p.name()
	if err != nil {
		t.Fatal(err)
	}
	if !runFileRegexp([]string{".jsonl"}).MatchString(n) {
		t.Fatalf("pattern does not match %q", n)
	}
}

func TestOSFSSweepEndToEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/traces", 0o700); err != nil {
		t.Fatal(err)
	}
	old := dir + "/traces/" + runName(1, ".jsonl")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tm := time.Now().Add(-30 * time.Hour)
	if err := os.Chtimes(old, tm, tm); err != nil {
		t.Fatal(err)
	}
	cfg := SweepConfig{Dir: dir, Extensions: []string{".jsonl"}}
	if r := Sweep(cfg); !r.Ran || r.Age != 1 {
		t.Fatalf("got %+v", r)
	}
	if _, err := os.Stat(dir + "/.sweep"); err != nil {
		t.Fatal(err)
	}
	if r := Sweep(cfg); r.Ran {
		t.Fatal("second sweep must be throttled")
	}
}
