package spool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func strKV(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func intKV(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func fixtureSpans() []*tracepb.ResourceSpans {
	tid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	root := &tracepb.Span{
		TraceId: tid, SpanId: []byte{1, 1, 1, 1, 1, 1, 1, 1}, Name: "root", Kind: tracepb.Span_SPAN_KIND_SERVER,
		StartTimeUnixNano: 1700000000000000000, EndTimeUnixNano: 1700000001000000000,
		Status: &tracepb.Status{}, // pdata always emits a status
	}
	child := &tracepb.Span{
		TraceId: tid, SpanId: []byte{2, 2, 2, 2, 2, 2, 2, 2}, ParentSpanId: root.SpanId, Name: "child",
		Kind:              tracepb.Span_SPAN_KIND_CLIENT,
		StartTimeUnixNano: 1700000000100000000, EndTimeUnixNano: 1700000000900000000,
		Attributes: []*commonpb.KeyValue{
			strKV("s", "x"), intKV("n", 42),
			{Key: "d", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1.5}}},
			{Key: "b", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}},
		},
		Events: []*tracepb.Span_Event{{TimeUnixNano: 1700000000200000000, Name: "ev", Attributes: []*commonpb.KeyValue{strKV("e", "1")}}},
		Links: []*tracepb.Span_Link{{
			TraceId: tid, SpanId: []byte{3, 3, 3, 3, 3, 3, 3, 3},
			Attributes: []*commonpb.KeyValue{strKV("l", "1")},
		}},
		Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "boom"},
	}
	return []*tracepb.ResourceSpans{{
		Resource:   &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strKV("service.name", "svc")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Scope: &commonpb.InstrumentationScope{Name: "pn", Version: "1"}, Spans: []*tracepb.Span{root, child}}},
	}}
}

func fixtureMetrics() *colmetrics.ExportMetricsServiceRequest {
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strKV("service.name", "svc")}},
		ScopeMetrics: []*metricspb.ScopeMetrics{{
			Scope: &commonpb.InstrumentationScope{Name: "pn"},
			Metrics: []*metricspb.Metric{
				{Name: "pn.count", Unit: "{x}", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
					AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, IsMonotonic: true,
					DataPoints: []*metricspb.NumberDataPoint{{
						StartTimeUnixNano: 1700000000000000000, TimeUnixNano: 1700000060000000000,
						Value: &metricspb.NumberDataPoint_AsInt{AsInt: 7},
					}},
				}}},
				{Name: "pn.dur", Unit: "s", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
					AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
					DataPoints: []*metricspb.HistogramDataPoint{{
						StartTimeUnixNano: 1700000000000000000, TimeUnixNano: 1700000060000000000,
						Count: 4, Sum: proto.Float64(12), BucketCounts: []uint64{1, 2, 1}, ExplicitBounds: []float64{1, 5},
						Min: proto.Float64(0.5), Max: proto.Float64(9.5),
					}},
				}}},
			},
		}},
	}}}
}

// decodeTraces is the oracle: the collector's own unmarshaler, then the proto
// marshaler back into the OTLP request type.
func decodeTraces(t *testing.T, rec []byte) *coltrace.ExportTraceServiceRequest {
	t.Helper()
	td, err := (&ptrace.JSONUnmarshaler{}).UnmarshalTraces(rec)
	if err != nil {
		t.Fatalf("collector cannot decode record: %v", err)
	}
	b, err := (&ptrace.ProtoMarshaler{}).MarshalTraces(td)
	if err != nil {
		t.Fatal(err)
	}
	out := &coltrace.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mergeTraces(parts ...*coltrace.ExportTraceServiceRequest) []*tracepb.Span {
	var spans []*tracepb.Span
	for _, p := range parts {
		for _, rs := range p.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				spans = append(spans, ss.Spans...)
			}
		}
	}
	return spans
}

// TestEncoderOracle is the contract every Encoder must pass.
func TestEncoderOracle(t *testing.T) {
	var enc Encoder = JSONEncoder{}
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: fixtureSpans()}
	recs, err := enc.EncodeTraces(req)
	if err != nil || len(recs) != 1 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	if got := decodeTraces(t, recs[0]); !proto.Equal(got, req) {
		t.Fatalf("trace round trip differs:\n%v\n%v", got, req)
	}
	s := string(recs[0])
	if !strings.Contains(s, `"traceId":"0102030405060708090a0b0c0d0e0f10"`) || !strings.Contains(s, `"spanId":"0101010101010101"`) {
		t.Errorf("ids are not hex: %s", s)
	}
	if !strings.Contains(s, `"kind":3`) || !strings.Contains(s, `"code":2`) {
		t.Errorf("enums are not integers: %s", s)
	}
	if !strings.HasSuffix(s, "\n") || strings.Count(s, "\n") != 1 {
		t.Errorf("record must be exactly one newline-terminated line")
	}

	mreq := fixtureMetrics()
	mrecs, err := enc.EncodeMetrics(mreq)
	if err != nil || len(mrecs) != 1 {
		t.Fatalf("recs=%d err=%v", len(mrecs), err)
	}
	md, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(mrecs[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := (&pmetric.ProtoMarshaler{}).MarshalMetrics(md)
	got := &colmetrics.ExportMetricsServiceRequest{}
	if err := proto.Unmarshal(b, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, mreq) {
		t.Fatalf("metric round trip differs:\n%v\n%v", got, mreq)
	}
}

// splitCap is a cap that fits each fixture span alone but not both together.
func splitCap(t *testing.T) int {
	t.Helper()
	spans := fixtureSpans()[0].ScopeSpans[0].Spans
	maxOne := 0
	for _, sp := range spans {
		one := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: fixtureSpans()[0].Resource, ScopeSpans: []*tracepb.ScopeSpans{{Scope: fixtureSpans()[0].ScopeSpans[0].Scope, Spans: []*tracepb.Span{sp}}},
		}}}
		b, err := encodeTraceJSON(one)
		if err != nil {
			t.Fatal(err)
		}
		maxOne = max(maxOne, len(b))
	}
	return maxOne
}

func metricSplitCap(t *testing.T) int {
	t.Helper()
	req := fixtureMetrics()
	ms := req.ResourceMetrics[0].ScopeMetrics[0].Metrics
	maxOne := 0
	for _, m := range ms {
		one := fixtureMetrics()
		one.ResourceMetrics[0].ScopeMetrics[0].Metrics = []*metricspb.Metric{m}
		b, err := encodeMetricJSON(one)
		if err != nil {
			t.Fatal(err)
		}
		maxOne = max(maxOne, len(b))
	}
	return maxOne
}

func TestEncoderDoesNotMutateRequest(t *testing.T) {
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: fixtureSpans()}
	want := proto.Clone(req)
	if _, err := (JSONEncoder{MaxRecordBytes: splitCap(t)}).EncodeTraces(req); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(req, want) {
		t.Fatal("request modified")
	}
}

func bigSpans(n, payload int) []*tracepb.ResourceSpans {
	rs := fixtureSpans()[0]
	spans := make([]*tracepb.Span, n)
	for i := range spans {
		spans[i] = &tracepb.Span{
			TraceId: bytes.Repeat([]byte{9}, 16), SpanId: []byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 1}, Name: fmt.Sprintf("s%d", i),
			Attributes: []*commonpb.KeyValue{strKV("pad", strings.Repeat("x", payload))},
		}
	}
	rs.ScopeSpans[0].Spans = spans
	return []*tracepb.ResourceSpans{rs}
}

func TestOversizedRequestSplitsIntoStandaloneRecords(t *testing.T) {
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: bigSpans(5, 1<<20)} // ~5 MiB
	recs, err := (JSONEncoder{}).EncodeTraces(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) < 2 {
		t.Fatalf("expected a split, got %d record(s)", len(recs))
	}
	var parts []*coltrace.ExportTraceServiceRequest
	for _, r := range recs {
		if len(r) > DefaultMaxRecordBytes {
			t.Errorf("record of %d bytes over cap", len(r))
		}
		p := decodeTraces(t, r) // standalone: decodes on its own
		if got := p.ResourceSpans[0].Resource.Attributes[0].Key; got != "service.name" {
			t.Errorf("resource not repeated, first attr %q", got)
		}
		parts = append(parts, p)
	}
	if got := len(mergeTraces(parts...)); got != 5 {
		t.Errorf("spans after split = %d, want 5", got)
	}
}

func TestJSONExpansionTriggersFurtherSplit(t *testing.T) {
	// Tiny cap: protobuf pre-split alone would not keep JSON under it.
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: fixtureSpans()}
	recs, err := (JSONEncoder{MaxRecordBytes: splitCap(t)}).EncodeTraces(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	for _, r := range recs {
		if len(r) > splitCap(t) {
			t.Errorf("record %d > cap", len(r))
		}
	}
	if _, err := (JSONEncoder{MaxRecordBytes: 100}).EncodeTraces(req); !errors.Is(err, ErrRecordTooLarge) {
		t.Errorf("indivisible oversized span: err = %v", err)
	}
}

func TestMetricsSplit(t *testing.T) {
	recs, err := (JSONEncoder{MaxRecordBytes: metricSplitCap(t)}).EncodeMetrics(fixtureMetrics())
	if err != nil || len(recs) != 2 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
}

func TestRecordCapBelowReceiverLimit(t *testing.T) {
	if DefaultMaxRecordBytes >= ReceiverMaxLogSize || DefaultMaxRecordBytes != 4<<20 || ReceiverMaxLogSize != 16<<20 {
		t.Fatalf("writer cap %d must stay below receiver max_log_size %d", DefaultMaxRecordBytes, ReceiverMaxLogSize)
	}
}

func TestEmptyEncodeAndNoFile(t *testing.T) {
	recs, err := (JSONEncoder{}).EncodeTraces(&coltrace.ExportTraceServiceRequest{})
	if err != nil || len(recs) != 0 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	fx := newFakeFS(1000)
	c := NewTraceClient(Config{Sink: NewFileSink("/spool", RoleUser, fx), Service: "pn"})
	_ = c.UploadTraces(context.Background(), nil)
	_ = c.UploadTraces(context.Background(), []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{}}}})
	if len(fx.opsList()) != 0 {
		t.Fatalf("an empty batch touched the file system: %v", fx.opsList())
	}
}

func newTestClient(fx *fakeFS, role Role, errs *[]error, mu *sync.Mutex) otelClient {
	return NewTraceClient(Config{
		Sink: NewFileSink("/spool", role, fx), Service: "pn",
		Hooks: Hooks{OnError: func(err error) { mu.Lock(); *errs = append(*errs, err); mu.Unlock() }},
		Now:   func() time.Time { return time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC) }, Pid: 77,
	})
}

type otelClient interface {
	Start(context.Context) error
	Stop(context.Context) error
	UploadTraces(context.Context, []*tracepb.ResourceSpans) error
}

func TestPublishSyncBeforeRenameAndFileID(t *testing.T) {
	fx := newFakeFS(1000)
	var errs []error
	var mu sync.Mutex
	c := newTestClient(fx, RoleUser, &errs, &mu)
	in := fixtureSpans()
	orig := proto.Clone(in[0])
	if err := c.UploadTraces(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(orig, in[0]) {
		t.Fatal("UploadTraces mutated the SDK data")
	}
	files := fx.published()
	if len(files) != 1 {
		t.Fatalf("published %d files", len(files))
	}
	for p, data := range files {
		base := filepath.Base(p)
		if !strings.HasPrefix(base, "pn-20261113T010203Z-77-") || !strings.HasSuffix(base, ".jsonl") || filepath.Dir(p) != "/spool/traces" {
			t.Errorf("bad path %s", p)
		}
		head := data
		if len(head) > 1000 {
			head = head[:1000]
		}
		if !bytes.Contains(head, []byte(FileIDAttribute)) || !bytes.Contains(head, []byte(base)) {
			t.Errorf("first 1000 bytes lack the file id: %s", head)
		}
		td := decodeTraces(t, data)
		attrs := td.ResourceSpans[0].Resource.Attributes
		if attrs[0].Key != FileIDAttribute || attrs[0].Value.GetStringValue() != base || attrs[1].Key != "service.name" {
			t.Errorf("file id not first: %v", attrs)
		}
	}
	ops := fx.opsList()
	idx := func(prefix string) int {
		for i, o := range ops {
			if strings.HasPrefix(o, prefix) {
				return i
			}
		}
		return -1
	}
	if idx("sync") < 0 || idx("write") >= idx("sync") || idx("sync") >= idx("close") || idx("close") >= idx("rename") {
		t.Errorf("order must be write, sync, close, rename: %v", ops)
	}
	for _, o := range ops {
		if strings.HasPrefix(o, "open ") && !strings.Contains(o, "/spool/traces/.pn-") {
			t.Errorf("tmp name wrong: %s", o)
		}
	}
	if len(errs) != 0 {
		t.Errorf("unexpected errors %v", errs)
	}
}

func TestTmpNamesNeverMatchExtensionGlob(t *testing.T) {
	fx := newFakeFS(1000)
	s := NewFileSink("/spool", RoleUser, fx)
	if err := s.Publish(SignalTraces, "a.jsonl", []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	for _, o := range fx.opsList() {
		if strings.HasPrefix(o, "open ") {
			base := filepath.Base(strings.TrimPrefix(o, "open "))
			if m, _ := filepath.Match("*.jsonl", base); m {
				t.Errorf("tmp name %q matches *.jsonl", base)
			}
			if !strings.HasPrefix(base, ".") {
				t.Errorf("tmp name %q not hidden", base)
			}
		}
	}
}

func TestDistinctNames(t *testing.T) {
	fx := newFakeFS(1000)
	sink := NewFileSink("/spool", RoleUser, fx)
	cfg := Config{Sink: sink, Service: "pn", Pid: 5, Now: func() time.Time { return time.Unix(1, 0) }}
	a, b := NewTraceClient(cfg), NewTraceClient(cfg) // two Inits, one pid, one clock
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = a.UploadTraces(context.Background(), fixtureSpans()) }()
		go func() { defer wg.Done(); _ = b.UploadTraces(context.Background(), fixtureSpans()) }()
	}
	wg.Wait()
	if n := len(fx.published()); n != 50 {
		t.Fatalf("distinct files = %d, want 50", n)
	}
}

func TestWriteFailuresAreSilentNoOps(t *testing.T) {
	cases := map[string]func(*fakeFS){
		"enospc":     func(f *fakeFS) { f.writeErr = syscall.ENOSPC },
		"eacces":     func(f *fakeFS) { f.openErr = &fs.PathError{Op: "open", Err: syscall.EACCES} },
		"short":      func(f *fakeFS) { f.shortN = 3 },
		"sync":       func(f *fakeFS) { f.syncErr = syscall.EIO },
		"rename-err": func(f *fakeFS) { f.renameErr = syscall.EXDEV },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newFakeFS(1000)
			setup(fx)
			var errs []error
			var mu sync.Mutex
			c := newTestClient(fx, RoleUser, &errs, &mu)
			for i := 0; i < 3; i++ {
				if err := c.UploadTraces(context.Background(), fixtureSpans()); err != nil {
					t.Fatalf("host must never see an error: %v", err)
				}
			}
			if len(errs) != 1 {
				t.Fatalf("OnError calls = %d, want exactly 1 (then no-op): %v", len(errs), errs)
			}
			if len(fx.published()) != 0 {
				t.Error("a file was published")
			}
			for p := range fx.files {
				t.Errorf("leftover file %s", p)
			}
			if name == "short" && !errors.Is(errs[0], io.ErrShortWrite) {
				t.Errorf("want short write, got %v", errs[0])
			}
		})
	}
}

func TestRenameENOENTIsTransient(t *testing.T) {
	fx := newFakeFS(1000)
	fx.renameErr = &fs.PathError{Op: "rename", Err: syscall.ENOENT}
	var errs []error
	var mu sync.Mutex
	c := newTestClient(fx, RoleUser, &errs, &mu)
	_ = c.UploadTraces(context.Background(), fixtureSpans())
	if len(errs) != 1 || !errors.Is(errs[0], ErrTransient) {
		t.Fatalf("errs = %v", errs)
	}
	fx.renameErr = nil
	_ = c.UploadTraces(context.Background(), fixtureSpans())
	if len(fx.published()) != 1 || len(errs) != 1 {
		t.Fatalf("exporter must keep working after a transient error: published=%d errs=%v", len(fx.published()), errs)
	}
}

func TestUnsafeDirectoriesRejected(t *testing.T) {
	cases := map[string]fakeDir{
		"symlink":     {mode: 0o700, owner: 1000, link: true},
		"wrong-owner": {mode: 0o700, owner: 0},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newFakeFS(1000)
			fx.dirs["/spool"] = d
			var errs []error
			var mu sync.Mutex
			c := newTestClient(fx, RoleUser, &errs, &mu)
			_ = c.UploadTraces(context.Background(), fixtureSpans())
			if len(errs) != 1 || !errors.Is(errs[0], ErrUnsafeDir) {
				t.Fatalf("errs = %v", errs)
			}
			if len(fx.files) != 0 {
				t.Error("wrote into an unsafe directory")
			}
		})
	}
	// The per-signal directory is checked too.
	fx := newFakeFS(1000)
	fx.dirs["/spool"] = fakeDir{mode: 0o700, owner: 1000}
	fx.dirs["/spool/traces"] = fakeDir{mode: 0o700, owner: 1000, link: true}
	if err := NewFileSink("/spool", RoleUser, fx).Publish(SignalTraces, "a.jsonl", []byte("x")); !errors.Is(err, ErrUnsafeDir) {
		t.Errorf("symlinked signal dir: %v", err)
	}
}

func TestRoleModesAndOwner(t *testing.T) {
	t.Run("user", func(t *testing.T) {
		fx := newFakeFS(1000)
		if err := NewFileSink("/spool", RoleUser, fx).Publish(SignalTraces, "a.jsonl", []byte("x")); err != nil {
			t.Fatal(err)
		}
		if fx.perms["/spool"] != 0o700 || fx.perms["/spool/traces"] != 0o700 || fx.perms["/spool/traces/a.jsonl"] != 0o600 {
			t.Errorf("perms %v", fx.perms)
		}
		fx.dirs["/spool"] = fakeDir{mode: 0o700, owner: 0} // root-owned: wrong for a user spool
		if err := NewFileSink("/spool", RoleUser, fx).Publish(SignalTraces, "b.jsonl", []byte("x")); !errors.Is(err, ErrUnsafeDir) {
			t.Errorf("user role must reject uid 0 owner: %v", err)
		}
	})
	t.Run("root", func(t *testing.T) {
		fx := newFakeFS(0)
		if err := NewFileSink("/spool", RoleRoot, fx).Publish(SignalMetrics, "a.jsonl", []byte("x")); err != nil {
			t.Fatal(err)
		}
		if fx.perms["/spool"] != 0o755 || fx.perms["/spool/metrics"] != 0o755 || fx.perms["/spool/metrics/a.jsonl"] != 0o644 {
			t.Errorf("perms %v", fx.perms)
		}
		// Expected owner is uid 0 whatever the process uid is.
		fx2 := newFakeFS(1000)
		fx2.dirs["/spool"] = fakeDir{mode: 0o755, owner: 1000}
		if err := NewFileSink("/spool", RoleRoot, fx2).Publish(SignalMetrics, "a.jsonl", []byte("x")); !errors.Is(err, ErrUnsafeDir) {
			t.Errorf("root role must reject a non-root owner: %v", err)
		}
		fx3 := newFakeFS(1000)
		fx3.dirs["/spool"] = fakeDir{mode: 0o755, owner: 0}
		fx3.dirs["/spool/metrics"] = fakeDir{mode: 0o755, owner: 0}
		if err := NewFileSink("/spool", RoleRoot, fx3).Publish(SignalMetrics, "a.jsonl", []byte("x")); err != nil {
			t.Errorf("root-owned dir must be accepted: %v", err)
		}
	})
	if RoleUser.String() != "user" || RoleRoot.String() != "root" {
		t.Error("role attribute values")
	}
}

func TestSinkRejectsBadInput(t *testing.T) {
	s := NewFileSink("/spool", RoleUser, newFakeFS(1))
	for _, c := range [][2]string{{"logs", "a.jsonl"}, {SignalTraces, "../a.jsonl"}, {SignalTraces, ".a.jsonl"}, {SignalTraces, ""}} {
		if err := s.Publish(c[0], c[1], []byte("x")); err == nil {
			t.Errorf("Publish(%q,%q) accepted", c[0], c[1])
		}
	}
	if err := s.Publish(SignalTraces, "a.jsonl", nil); err == nil {
		t.Error("empty data accepted")
	}
}

// --- a second Encoder proves the exporter is format-agnostic ---

type protoEncoder struct{}

func (protoEncoder) Extension() string { return ".pb" }
func (protoEncoder) EncodeTraces(r *coltrace.ExportTraceServiceRequest) ([][]byte, error) {
	b, err := proto.Marshal(r)
	return [][]byte{b}, err
}

func (protoEncoder) EncodeMetrics(r *colmetrics.ExportMetricsServiceRequest) ([][]byte, error) {
	b, err := proto.Marshal(r)
	return [][]byte{b}, err
}

func TestSecondEncoderWorksUnchanged(t *testing.T) {
	fx := newFakeFS(1000)
	c := NewTraceClient(Config{Sink: NewFileSink("/spool", RoleUser, fx), Encoder: protoEncoder{}, Service: "pn"})
	if err := c.UploadTraces(context.Background(), fixtureSpans()); err != nil {
		t.Fatal(err)
	}
	for p, data := range fx.published() {
		if !strings.HasSuffix(p, ".pb") {
			t.Errorf("extension from the encoder not used: %s", p)
		}
		got := &coltrace.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(data, got); err != nil {
			t.Fatal(err)
		}
		if got.ResourceSpans[0].Resource.Attributes[0].Key != FileIDAttribute {
			t.Error("file id not prepended")
		}
	}
	if len(fx.published()) != 1 {
		t.Fatal("no file")
	}
}

func TestStopMakesUploadsNoOp(t *testing.T) {
	fx := newFakeFS(1000)
	c := NewTraceClient(Config{Sink: NewFileSink("/spool", RoleUser, fx), Service: "pn"})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = c.Stop(context.Background())
	_ = c.UploadTraces(context.Background(), fixtureSpans())
	if len(fx.opsList()) != 0 {
		t.Error("upload after Stop wrote")
	}
}

// --- real writer on a temp dir ---

func TestRealWriterEndToEnd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "otel-spool")
	var errs []error
	exp, err := NewTraceExporter(context.Background(), Config{
		Sink: NewFileSink(dir, RoleUser, nil), Service: "pn",
		Hooks: Hooks{OnError: func(e error) { errs = append(errs, e) }},
	})
	if err != nil {
		t.Fatal(err)
	}
	var _ sdktrace.SpanExporter = exp
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	_, span := tp.Tracer("t").Start(context.Background(), "op")
	span.End()
	if err := tp.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(dir, "traces"))
	if err != nil || len(ents) != 1 {
		t.Fatalf("entries=%v err=%v", ents, err)
	}
	e := ents[0]
	if !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasPrefix(e.Name(), ".") {
		t.Errorf("name %q", e.Name())
	}
	fi, _ := e.Info()
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	for _, d := range []string{dir, filepath.Join(dir, "traces")} {
		st, err := os.Stat(d)
		if err != nil || st.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", d, st, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, "traces", e.Name()))
	if !bytes.Contains(data[:min(1000, len(data))], []byte(FileIDAttribute)) {
		t.Error("file id not in first 1000 bytes")
	}
	td := decodeTraces(t, data)
	if td.ResourceSpans[0].ScopeSpans[0].Spans[0].Name != "op" || len(errs) != 0 {
		t.Errorf("content %v errs %v", td, errs)
	}
}

func TestRealWriterRejectsSymlinkAndLeavesNoTmp(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := NewFileSink(link, RoleUser, nil).Publish(SignalTraces, "a.jsonl", []byte("x\n"))
	if !errors.Is(err, ErrUnsafeDir) {
		t.Fatalf("err = %v", err)
	}
	if ents, _ := os.ReadDir(target); len(ents) != 0 {
		t.Errorf("wrote through the symlink: %v", ents)
	}
	good := filepath.Join(root, "good")
	if err := NewFileSink(good, RoleUser, nil).Publish(SignalMetrics, "a.jsonl", []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Join(good, "metrics"))
	if len(ents) != 1 || ents[0].Name() != "a.jsonl" {
		t.Errorf("entries %v (a tmp file was left?)", ents)
	}
	// A pre-planted tmp file is never reused or followed (O_EXCL).
	planted := filepath.Join(good, "metrics", ".b.jsonl.tmp")
	if err := os.WriteFile(planted, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewFileSink(good, RoleUser, nil).Publish(SignalMetrics, "b.jsonl", []byte("new\n")); err == nil {
		t.Error("existing tmp file reused")
	}
}
