// Package jsonlloggertest is the test helper for code that logs through
// jsonllogger: a goroutine-safe capture buffer that decodes the JSONL lines.
package jsonlloggertest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/x/jsonllogger"
)

// Buffer is a goroutine-safe io.Writer that can be read while a daemon under
// test still writes.
type Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends p.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

// String returns what was written so far.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// Records decodes every complete line written so far; an undecodable line
// fails the test.
func (b *Buffer) Records(t testing.TB) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(b.String(), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("jsonlloggertest: undecodable line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// New returns a logger built with jsonllogger.NewLogger over a fresh Buffer.
func New(t testing.TB, opts ...jsonllogger.Option) (*slog.Logger, *Buffer) {
	t.Helper()
	buf := &Buffer{}
	return jsonllogger.NewLogger(buf, opts...), buf
}
