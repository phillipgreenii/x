package spool

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// FileIDAttribute is the per-file resource attribute prepended to every
// resource before encoding. The collector fingerprints a file by its first
// 1000 bytes, and same-header files would otherwise be taken for one file.
const FileIDAttribute = "pn.spool.file_id"

// Hooks lets the caller observe failures. There is deliberately no per-publish
// hook: counting every publish would make each metrics flush non-empty.
type Hooks struct {
	// OnError is called once per transient write error and once for the
	// persistent failure that switches the exporter to a no-op. It MUST be
	// cheap and MUST NOT print on its own. May be nil.
	OnError func(err error)
}

// Config is what every spool exporter needs.
type Config struct {
	Sink Sink
	// Encoder defaults to JSONEncoder{}.
	Encoder Encoder
	// Service prefixes the file names; empty becomes "unknown".
	Service string
	Hooks   Hooks

	// Now and Pid are for tests; nil/0 select time.Now and os.Getpid.
	Now func() time.Time
	Pid int
}

// seq makes names unique within the process even when the clock, pid and
// random part repeat.
var seq atomic.Uint64

// publisher is the part shared by the trace and metric exporters: unique
// names, failure latch and the hook.
type publisher struct {
	sink    Sink
	enc     Encoder
	service string
	hooks   Hooks
	now     func() time.Time
	pid     int
	failed  atomic.Bool
}

func newPublisher(cfg Config) *publisher {
	p := &publisher{sink: cfg.Sink, enc: cfg.Encoder, hooks: cfg.Hooks, now: cfg.Now, pid: cfg.Pid}
	if p.enc == nil {
		p.enc = JSONEncoder{}
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.pid == 0 {
		p.pid = os.Getpid()
	}
	p.service = sanitizeService(cfg.Service)
	return p
}

func sanitizeService(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			return r
		}
		return '_'
	}, s)
	if s == "" || strings.HasPrefix(s, ".") {
		return "unknown" + s
	}
	return s
}

// name returns <service>-<utc-ts>-<pid>-<rand>-<seq><ext>.
func (p *publisher) name() (string, error) {
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%d-%s-%d%s", p.service, p.now().UTC().Format("20060102T150405Z"),
		p.pid, hex.EncodeToString(r[:]), seq.Add(1), p.enc.Extension()), nil
}

// publish names one file, lets build encode its records (given the name, for
// the file id) and publishes the concatenation. Empty output creates no file.
// It never returns an error: failures go to the hook and, unless transient,
// latch the exporter to a no-op.
func (p *publisher) publish(signal string, build func(name string) ([][]byte, error)) {
	if p.failed.Load() {
		return
	}
	name, err := p.name()
	if err != nil {
		p.fail(err)
		return
	}
	recs, err := build(name)
	if err != nil {
		p.fail(err)
		return
	}
	if len(recs) == 0 {
		return
	}
	if err := p.sink.Publish(signal, name, bytes.Join(recs, nil)); err != nil {
		p.fail(err)
	}
}

func (p *publisher) fail(err error) {
	if errors.Is(err, ErrTransient) {
		p.report(err)
		return
	}
	if p.failed.CompareAndSwap(false, true) {
		p.report(err)
	}
}

func (p *publisher) report(err error) {
	if p.hooks.OnError != nil {
		p.hooks.OnError(err)
	}
}

// PrependFileID puts the file id attribute first in res's attributes (the
// first entry, so it lands in the file's first bytes) and returns the
// resource, creating one when res is nil.
func PrependFileID(res *resourcepb.Resource, name string) *resourcepb.Resource {
	if res == nil {
		res = &resourcepb.Resource{}
	}
	kv := &commonpb.KeyValue{Key: FileIDAttribute, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: name}}}
	res.Attributes = append([]*commonpb.KeyValue{kv}, res.Attributes...)
	return res
}

// traceClient implements otlptrace.Client by spooling to files.
type traceClient struct {
	p       *publisher
	stopped atomic.Bool
}

// NewTraceClient returns an otlptrace.Client that writes each upload as one
// file. Pass it to otlptrace.New, or use NewTraceExporter.
func NewTraceClient(cfg Config) otlptrace.Client {
	return &traceClient{p: newPublisher(cfg)}
}

// NewTraceExporter builds the exporter with otlptrace.New, so it is the same
// sdktrace.SpanExporter type the OTLP/HTTP path produces.
func NewTraceExporter(ctx context.Context, cfg Config) (*otlptrace.Exporter, error) {
	return otlptrace.New(ctx, NewTraceClient(cfg))
}

// Start implements otlptrace.Client.
func (c *traceClient) Start(context.Context) error { return nil }

// Stop implements otlptrace.Client; later uploads are dropped.
func (c *traceClient) Stop(context.Context) error {
	c.stopped.Store(true)
	return nil
}

// UploadTraces implements otlptrace.Client. It always returns nil: a spool
// failure must never fail or log from the host process (see Hooks).
func (c *traceClient) UploadTraces(_ context.Context, rs []*tracepb.ResourceSpans) error {
	if c.stopped.Load() || spanCount(rs) == 0 {
		return nil
	}
	c.p.publish(SignalTraces, func(name string) ([][]byte, error) {
		req := &coltrace.ExportTraceServiceRequest{ResourceSpans: make([]*tracepb.ResourceSpans, len(rs))}
		for i, r := range rs {
			cl := proto.Clone(r).(*tracepb.ResourceSpans) // never mutate the SDK's data
			cl.Resource = PrependFileID(cl.Resource, name)
			req.ResourceSpans[i] = cl
		}
		return c.p.enc.EncodeTraces(req)
	})
	return nil
}

func spanCount(rs []*tracepb.ResourceSpans) int {
	n := 0
	for _, r := range rs {
		for _, s := range r.GetScopeSpans() {
			n += len(s.GetSpans())
		}
	}
	return n
}
