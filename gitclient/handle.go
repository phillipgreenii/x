package gitclient

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"sync"
)

// Handle represents one git invocation that has already been started
// (cmd.Start(), never cmd.Run()) and runs to completion on its own,
// independent of whether or when a caller ever observes it. newHandle
// launches exactly one internal goroutine, which calls cmd.Wait(),
// classifies the result via classifyRunErr (the same mapping run's own
// buffered spawn uses), stores it, and closes done -- unconditionally,
// whether or not the caller ever calls Wait or AttachStream. That is what
// guarantees a dropped Handle still reaps its process (no zombie) and
// never leaks that goroutine: it always reaches close(done) and returns
// on its own.
//
// A Handle is safe for concurrent use. Wait may be called more than once,
// or from multiple goroutines at once, and always returns the identical
// result. AttachStream may be called at any point in the invocation's
// lifecycle -- before it has produced any output, while it is actively
// producing output, or after it has already completed -- and in every
// case the attached writer receives the COMPLETE output: whatever is
// already buffered is replayed first, and the same mutex that guards that
// replay also registers the writer as the target for everything written
// from that point on, so a byte is never missed (written in the gap
// between replay and registration) or duplicated (replayed again after
// already having been forwarded live).
type Handle struct {
	cmd  *exec.Cmd
	args []string // defensive copy, used only for classifyRunErr's error text

	mu     sync.Mutex
	stdout streamBuf
	stderr streamBuf

	ctx  context.Context
	done chan struct{}
	err  error // valid only once done is closed -- see newHandle's goroutine
}

// streamBuf buffers everything written to it and, once a live sink is
// registered (via attach), forwards every subsequent write there too.
// Every method assumes the caller already holds the owning Handle's mu --
// streamBuf has no lock of its own, by design: the "replay under the same
// mutex that registers the sink" guarantee on Handle's own doc comment
// depends on write and attach never running concurrently with each other,
// which only the shared Handle.mu can provide.
type streamBuf struct {
	buf  bytes.Buffer
	sink io.Writer
}

// write appends p to buf and forwards it to sink, if one is registered.
// The forward is best-effort: a broken or short-writing sink must not
// fail or truncate the underlying invocation, only its own copy of the
// stream.
func (s *streamBuf) write(p []byte) {
	s.buf.Write(p)
	if s.sink != nil {
		s.sink.Write(p)
	}
}

// attach replays everything buffered so far to w (if w is non-nil and
// anything has been buffered), then registers w as the sink for
// subsequent writes -- REPLACING whatever sink was registered before.
// Handle models one current live observer per stream, not a fan-out
// multiplexer: a second AttachStream call is a deliberate hand-off, not
// an addition.
func (s *streamBuf) attach(w io.Writer) {
	if w == nil {
		return
	}
	if s.buf.Len() > 0 {
		w.Write(s.buf.Bytes())
	}
	s.sink = w
}

// handleWriter adapts one of a Handle's two streamBufs to io.Writer, so
// cmd.Stdout/cmd.Stderr can point directly at it. Every write takes h.mu,
// serializing it against AttachStream exactly as streamBuf's own doc
// comment requires. This is the ONLY writer in play: per Handle's
// package-level doc comment, os/exec's own machinery already starts (at
// cmd.Start()) and joins (at cmd.Wait()) the goroutine that copies the
// child's output into a non-*os.File io.Writer -- Handle adds no
// goroutine of its own for this draining.
type handleWriter struct {
	h      *Handle
	stderr bool
}

func (w *handleWriter) Write(p []byte) (int, error) {
	w.h.mu.Lock()
	defer w.h.mu.Unlock()
	if w.stderr {
		w.h.stderr.write(p)
	} else {
		w.h.stdout.write(p)
	}
	return len(p), nil
}

// newHandle starts cmd (cmd.Start(), never Run()) and returns a Handle
// wrapping it, or Start's own error if the process never ran at all --
// there is nothing to wait on in that case, matching run's own
// nothing-was-spawned error shape. args is retained only for
// classifyRunErr's error text; ctx is retained so the completion
// goroutine applies the SAME ctx.Err() check run's context contract
// always has -- cmd itself must already have been built from this ctx
// via newCmd, so cancellation kills the process exactly as newCmd's own
// doc comment describes.
func newHandle(ctx context.Context, cmd *exec.Cmd, args []string) (*Handle, error) {
	h := &Handle{
		cmd:  cmd,
		args: append([]string(nil), args...),
		ctx:  ctx,
		done: make(chan struct{}),
	}
	cmd.Stdout = &handleWriter{h: h}
	cmd.Stderr = &handleWriter{h: h, stderr: true}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go func() {
		runErr := cmd.Wait()
		h.mu.Lock()
		stderrBytes := h.stderr.buf.Bytes()
		h.mu.Unlock()
		h.err = classifyRunErr(h.ctx, h.args, stderrBytes, runErr)
		close(h.done)
	}()

	return h, nil
}

// Wait blocks until the invocation completes and returns the same
// classify()/ctx-error result run() has always computed for an identical
// failure (both funnel through classifyRunErr). Safe to call more than
// once, and concurrently from multiple goroutines -- every call observes
// the identical result without re-waiting on the process itself.
func (h *Handle) Wait() error {
	<-h.done
	return h.err
}

// AttachStream registers stdout and stderr as this Handle's live output
// sinks -- see the Handle doc comment for the full "no missed/duplicated
// bytes, at any point in the lifecycle" guarantee. Either argument may be
// nil to leave that stream's sink unchanged (e.g. AttachStream(w, nil) to
// observe stdout only).
func (h *Handle) AttachStream(stdout, stderr io.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stdout.attach(stdout)
	h.stderr.attach(stderr)
}

// NewFakeHandle returns an already-completed Handle that never spawns a
// process: Wait() returns err immediately (nil or otherwise), and
// AttachStream replays stdout/stderr exactly like a real invocation's
// would.
//
// It exists to solve the "new testing problem" this package's Fetcher/
// WorktreeManager/Syncer/Committer/Pusher roles create (bead pg2-f1cq7):
// since those roles' methods now return the CONCRETE *Handle type rather
// than a plain error, a hand-written fake implementation of one of them
// (a consumer's own unit-test double) can no longer just return an
// error -- it needs something Handle-shaped. NewFakeHandle is the chosen
// answer, in place of the other option considered (an exported
// HandleLike Wait/AttachStream interface substituted for the concrete
// type on every role method): that alternative would have widened every
// role method's return type for every real (non-test) caller too, for a
// benefit only test doubles need.
func NewFakeHandle(stdout, stderr []byte, err error) *Handle {
	h := &Handle{done: make(chan struct{}), err: err}
	h.stdout.buf.Write(stdout)
	h.stderr.buf.Write(stderr)
	close(h.done)
	return h
}
