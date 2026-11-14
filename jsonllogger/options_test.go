package jsonllogger_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/jsonllogger"
	"github.com/phillipgreenii/x/jsonllogger/jsonlloggertest"
)

func TestWithServiceLevelAndRenames(t *testing.T) {
	l, buf := jsonlloggertest.New(t,
		jsonllogger.WithService("svc"),
		jsonllogger.WithLevel(slog.LevelWarn),
		jsonllogger.WithKeyRenames(map[string]string{"time": "ts", "msg": "event"}))
	l.Info("dropped")
	l.Warn("kept", "file", "f1")
	recs := buf.Records(t)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %s", len(recs), buf.String())
	}
	r := recs[0]
	if r["service"] != "svc" || r["event"] != "kept" || r["level"] != "warn" || r["file"] != "f1" {
		t.Errorf("record = %v", r)
	}
	if _, ok := r["ts"]; !ok {
		t.Errorf("ts missing: %v", r)
	}
	if !strings.HasSuffix(r["ts"].(string), "Z") {
		t.Errorf("ts not UTC: %v", r["ts"])
	}
}

func TestNewWithWriter(t *testing.T) {
	buf := &jsonlloggertest.Buffer{}
	l, err := jsonllogger.New("ignored", jsonllogger.WithWriter(buf), jsonllogger.WithService("s"))
	if err != nil {
		t.Fatal(err)
	}
	l.Info("hi")
	if recs := buf.Records(t); len(recs) != 1 || recs[0]["service"] != "s" {
		t.Errorf("records = %v", recs)
	}
}

func TestRotatingFileReopensAfterExternalRename(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "a.jsonl")
	f, err := jsonllogger.OpenRotatingFile(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two\n" {
		t.Errorf("new file = %q, want two", b)
	}
	if b, _ := os.ReadFile(p + ".old"); string(b) != "one\n" {
		t.Errorf("old file = %q, want one", b)
	}
	// Removal is followed too.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("three\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "three\n" {
		t.Errorf("after remove = %q", b)
	}
}

func TestRotatingFileRotatesAtMaxBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	f, err := jsonllogger.OpenRotatingFile(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, s := range []string{"aaaaaa\n", "bbbbbb\n", "cccccc\n"} {
		if _, err := f.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "cccccc\n" {
		t.Errorf("live = %q", b)
	}
	if b, _ := os.ReadFile(p + ".1"); string(b) != "bbbbbb\n" {
		t.Errorf("backup = %q (only the newest backup is kept)", b)
	}
}

func TestNewMaxBytesCapsFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	l, err := jsonllogger.New("capapp", jsonllogger.WithMaxBytes(200))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		l.Info(strings.Repeat("x", 60))
	}
	p := filepath.Join(os.Getenv("XDG_STATE_HOME"), "capapp", "capapp.jsonl")
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf("no backup created: %v", err)
	}
	if st, _ := os.Stat(p); st.Size() > 200 {
		t.Errorf("live file %d bytes, over cap", st.Size())
	}
}

type errHandler struct{ slog.Handler }

func (errHandler) Handle(context.Context, slog.Record) error { return errors.New("boom") }

func TestFanoutDeliversToAllAndJoinsErrors(t *testing.T) {
	a, b := &jsonlloggertest.Buffer{}, &jsonlloggertest.Buffer{}
	h := jsonllogger.Fanout(
		jsonllogger.NewHandler(a),
		errHandler{jsonllogger.NewHandler(&jsonlloggertest.Buffer{})},
		jsonllogger.NewHandler(b, jsonllogger.WithLevel(slog.LevelError)),
	).WithAttrs([]slog.Attr{slog.String("k", "v")})
	l := slog.New(h)
	l.Info("hello")
	if recs := a.Records(t); len(recs) != 1 || recs[0]["k"] != "v" {
		t.Errorf("a = %v", recs)
	}
	if len(b.Records(t)) != 0 {
		t.Errorf("b got a below-level record")
	}
	r := slog.NewRecord(timeNow(), slog.LevelInfo, "x", 0)
	if err := h.Handle(context.Background(), r); err == nil {
		t.Error("handler error not surfaced")
	}
}

func timeNow() time.Time { return time.Now() }

func (e errHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return errHandler{e.Handler.WithAttrs(a)}
}
