package exporttest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Providers are what a Scenario instruments. The same Scenario runs once per
// transport, so it MUST be deterministic apart from ids and metric timestamps
// (span timestamps are set explicitly).
type Providers struct {
	Traces  trace.TracerProvider
	Metrics metric.MeterProvider
}

// Scenario is one fixed workload.
type Scenario struct {
	Name string
	// Buckets are the explicit histogram boundaries applied to every
	// histogram (nil selects DefaultBuckets).
	Buckets []float64
	// SpoolRecordBytes caps one spool record for Encoders that take a cap
	// (0 selects the Encoder's default).
	SpoolRecordBytes int
	// WantSplit requires a chunking transport to need more than one record
	// for both signals.
	WantSplit bool
	Run       func(ctx context.Context, p Providers)
}

// DefaultBuckets are the histogram boundaries used when a Scenario sets none.
var DefaultBuckets = []float64{1, 5, 10, 50, 100}

var t0 = time.Unix(1_700_000_000, 0)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

// Scenarios returns the fixed scenario set.
func Scenarios() []Scenario {
	return []Scenario{
		{Name: "nested-spans", Run: runNestedSpans},
		{Name: "delta-counters", Run: runCounters},
		{Name: "histograms", Run: runHistograms},
		{Name: "multi-scope", Run: runMultiScope},
		{Name: "large-batch", SpoolRecordBytes: 8 << 10, WantSplit: true, Run: runLargeBatch},
		{Name: "empty-flush", Run: func(context.Context, Providers) {}},
	}
}

func runNestedSpans(ctx context.Context, p Providers) {
	tr := p.Traces.Tracer("contract.nested", trace.WithInstrumentationVersion("1.2.3"))
	other := p.Traces.Tracer("contract.other")

	_, linked := other.Start(context.Background(), "linked-target", trace.WithTimestamp(at(0)))
	linked.End(trace.WithTimestamp(at(1)))

	ctx, root := tr.Start(ctx, "root",
		trace.WithTimestamp(at(10)),
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithLinks(trace.Link{
			SpanContext: linked.SpanContext(),
			Attributes:  []attribute.KeyValue{attribute.String("link.kind", "follows"), attribute.Int("link.n", 7)},
		}),
		trace.WithAttributes(
			attribute.String("s", "text"), attribute.Int64("i", -42), attribute.Float64("f", 2.5),
			attribute.Bool("b", true),
			attribute.StringSlice("ss", []string{"a", "b"}), attribute.Int64Slice("is", []int64{1, 2, 3}),
			attribute.Float64Slice("fs", []float64{0.5, 1.5}), attribute.BoolSlice("bs", []bool{true, false}),
		))
	root.AddEvent("started", trace.WithTimestamp(at(11)), trace.WithAttributes(attribute.String("phase", "one")))

	cctx, child := tr.Start(ctx, "child", trace.WithTimestamp(at(12)), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.system", "x")))
	_, grand := tr.Start(cctx, "grandchild", trace.WithTimestamp(at(13)), trace.WithSpanKind(trace.SpanKindInternal))
	grand.RecordError(errors.New("boom"), trace.WithTimestamp(at(14)), trace.WithAttributes(attribute.String("where", "grand")))
	grand.SetStatus(codes.Error, "grandchild failed")
	grand.End(trace.WithTimestamp(at(15)))
	child.AddEvent("second", trace.WithTimestamp(at(16)))
	child.AddEvent("third", trace.WithTimestamp(at(17)), trace.WithAttributes(attribute.Int("n", 3)))
	child.SetStatus(codes.Ok, "")
	child.End(trace.WithTimestamp(at(18)))

	_, sib := tr.Start(ctx, "sibling", trace.WithTimestamp(at(19)), trace.WithSpanKind(trace.SpanKindProducer))
	sib.End(trace.WithTimestamp(at(20)))
	root.SetStatus(codes.Error, "root failed")
	root.End(trace.WithTimestamp(at(21)))

	// A second trace.
	_, solo := other.Start(context.Background(), "solo", trace.WithTimestamp(at(30)), trace.WithSpanKind(trace.SpanKindConsumer))
	solo.End(trace.WithTimestamp(at(31)))
}

func runCounters(ctx context.Context, p Providers) {
	m := p.Metrics.Meter("contract.counters")
	ic, _ := m.Int64Counter("contract.requests", metric.WithUnit("{request}"), metric.WithDescription("Requests served."))
	ic.Add(ctx, 3, metric.WithAttributes(attribute.String("route", "/a"), attribute.Int("code", 200)))
	ic.Add(ctx, 4, metric.WithAttributes(attribute.String("route", "/a"), attribute.Int("code", 200)))
	ic.Add(ctx, 1, metric.WithAttributes(attribute.String("route", "/a"), attribute.Int("code", 500)))
	ic.Add(ctx, 9, metric.WithAttributes(attribute.String("route", "/b")))
	ic.Add(ctx, 2)
	fc, _ := m.Float64Counter("contract.bytes", metric.WithUnit("By"), metric.WithDescription("Bytes moved."))
	fc.Add(ctx, 1.5, metric.WithAttributes(attribute.Bool("ok", true)))
	fc.Add(ctx, 2.25, metric.WithAttributes(attribute.Bool("ok", true)))
	fc.Add(ctx, 0.75, metric.WithAttributes(attribute.Bool("ok", false)))
}

func runHistograms(ctx context.Context, p Providers) {
	m := p.Metrics.Meter("contract.histograms")
	ih, _ := m.Int64Histogram("contract.size", metric.WithUnit("By"), metric.WithDescription("Payload size."))
	for _, v := range []int64{0, 1, 3, 7, 50, 51, 400} {
		ih.Record(ctx, v, metric.WithAttributes(attribute.String("kind", "in")))
	}
	ih.Record(ctx, 5, metric.WithAttributes(attribute.String("kind", "out")))
	fh, _ := m.Float64Histogram("contract.latency", metric.WithUnit("s"), metric.WithDescription("Latency."))
	for _, v := range []float64{0.25, 1, 4.5, 10, 99.9, 100.5} {
		fh.Record(ctx, v)
	}
	fh.Record(ctx, 2.5, metric.WithAttributes(attribute.Int("shard", 1)))
}

func runMultiScope(ctx context.Context, p Providers) {
	for i, name := range []string{"contract.scope.a", "contract.scope.b"} {
		tr := p.Traces.Tracer(name, trace.WithInstrumentationVersion(fmt.Sprintf("0.%d", i)),
			trace.WithSchemaURL(fmt.Sprintf("https://example.test/schema/%d", i)),
			trace.WithInstrumentationAttributes(attribute.String("scope.idx", fmt.Sprint(i))))
		_, s := tr.Start(ctx, "op-"+name, trace.WithTimestamp(at(100+i)))
		s.End(trace.WithTimestamp(at(110 + i)))

		m := p.Metrics.Meter(name, metric.WithInstrumentationVersion(fmt.Sprintf("0.%d", i)),
			metric.WithSchemaURL(fmt.Sprintf("https://example.test/schema/%d", i)),
			metric.WithInstrumentationAttributes(attribute.String("scope.idx", fmt.Sprint(i))))
		c, _ := m.Int64Counter("contract.scoped")
		c.Add(ctx, int64(i+1))
		h, _ := m.Float64Histogram("contract.scoped.hist")
		h.Record(ctx, float64(i)+0.5)
	}
}

func runLargeBatch(ctx context.Context, p Providers) {
	tr := p.Traces.Tracer("contract.large")
	for i := range 300 {
		_, s := tr.Start(ctx, fmt.Sprintf("bulk-%03d", i), trace.WithTimestamp(at(1000+i)),
			trace.WithAttributes(
				attribute.Int("idx", i), attribute.String("payload", fmt.Sprintf("%0200d", i)),
				attribute.String("a", "alpha"), attribute.String("b", "beta"),
			))
		s.AddEvent("tick", trace.WithTimestamp(at(1000+i)), trace.WithAttributes(attribute.Int("idx", i)))
		s.End(trace.WithTimestamp(at(1500 + i)))
	}
	m := p.Metrics.Meter("contract.large")
	for i := range 60 {
		c, _ := m.Int64Counter(fmt.Sprintf("contract.bulk.%02d", i), metric.WithUnit("{op}"))
		for j := range 3 {
			c.Add(ctx, int64(i+j+1), metric.WithAttributes(attribute.Int("j", j), attribute.String("pad", fmt.Sprintf("%0100d", i))))
		}
	}
}
