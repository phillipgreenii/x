package spool

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

var (
	t0 = time.Unix(1700000000, 0)
	t1 = time.Unix(1700000060, 0)
)

func sdkFixture() *metricdata.ResourceMetrics {
	attrs := attribute.NewSet(attribute.String("k", "v"), attribute.Int("n", 3), attribute.StringSlice("l", []string{"a", "b"}))
	return &metricdata.ResourceMetrics{
		Resource: resource.NewWithAttributes("https://schema/res", attribute.String("service.name", "svc")),
		ScopeMetrics: []metricdata.ScopeMetrics{{
			Scope: instrumentation.Scope{Name: "pn", Version: "1", SchemaURL: "https://schema/scope"},
			Metrics: []metricdata.Metrics{
				{Name: "c.int", Description: "d", Unit: "{x}", Data: metricdata.Sum[int64]{
					Temporality: metricdata.DeltaTemporality, IsMonotonic: true,
					DataPoints: []metricdata.DataPoint[int64]{{Attributes: attrs, StartTime: t0, Time: t1, Value: 7}},
				}},
				{Name: "c.float", Unit: "s", Data: metricdata.Sum[float64]{
					Temporality: metricdata.DeltaTemporality, IsMonotonic: true,
					DataPoints: []metricdata.DataPoint[float64]{{StartTime: t0, Time: t1, Value: 1.5}},
				}},
				{Name: "h.int", Unit: "ms", Data: metricdata.Histogram[int64]{
					Temporality: metricdata.DeltaTemporality,
					DataPoints: []metricdata.HistogramDataPoint[int64]{{
						Attributes: attrs, StartTime: t0, Time: t1, Count: 4, Sum: 12,
						Bounds: []float64{1, 5}, BucketCounts: []uint64{1, 2, 1},
						Min: metricdata.NewExtrema[int64](0), Max: metricdata.NewExtrema[int64](9),
					}},
				}},
				{Name: "h.float", Unit: "s", Data: metricdata.Histogram[float64]{
					Temporality: metricdata.DeltaTemporality,
					DataPoints: []metricdata.HistogramDataPoint[float64]{{
						StartTime: t0, Time: t1, Count: 2, Sum: 3.5,
						Bounds: []float64{2}, BucketCounts: []uint64{1, 1},
						Min: metricdata.NewExtrema(0.5), Max: metricdata.NewExtrema(3.0),
					}},
				}},
			},
		}},
	}
}

func TestConvertMetrics(t *testing.T) {
	req, err := ConvertMetrics(sdkFixture())
	if err != nil {
		t.Fatal(err)
	}
	rm := req.ResourceMetrics[0]
	if rm.SchemaUrl != "https://schema/res" || rm.Resource.Attributes[0].Key != "service.name" {
		t.Errorf("resource: %v", rm)
	}
	sm := rm.ScopeMetrics[0]
	if sm.Scope.Name != "pn" || sm.Scope.Version != "1" || sm.SchemaUrl != "https://schema/scope" || len(sm.Metrics) != 4 {
		t.Fatalf("scope: %v", sm)
	}
	wantAttrs := []*commonpb.KeyValue{
		{Key: "k", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "v"}}},
		{Key: "l", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.AnyValue{
			{Value: &commonpb.AnyValue_StringValue{StringValue: "a"}}, {Value: &commonpb.AnyValue_StringValue{StringValue: "b"}},
		}}}}},
		intKV("n", 3),
	}
	s0 := sm.Metrics[0]
	if s0.Name != "c.int" || s0.Description != "d" || s0.Unit != "{x}" {
		t.Errorf("metadata: %v", s0)
	}
	want := &metricspb.Sum{
		AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, IsMonotonic: true,
		DataPoints: []*metricspb.NumberDataPoint{{
			Attributes: wantAttrs, StartTimeUnixNano: 1700000000e9, TimeUnixNano: 1700000060e9,
			Value: &metricspb.NumberDataPoint_AsInt{AsInt: 7},
		}},
	}
	if !proto.Equal(s0.GetSum(), want) {
		t.Errorf("int sum:\n%v\n%v", s0.GetSum(), want)
	}
	if dp := sm.Metrics[1].GetSum().DataPoints[0]; dp.GetAsDouble() != 1.5 || !sm.Metrics[1].GetSum().IsMonotonic {
		t.Errorf("float sum: %v", dp)
	}
	wantH := &metricspb.Histogram{
		AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
		DataPoints: []*metricspb.HistogramDataPoint{{
			Attributes: wantAttrs, StartTimeUnixNano: 1700000000e9, TimeUnixNano: 1700000060e9,
			Count: 4, Sum: proto.Float64(12), BucketCounts: []uint64{1, 2, 1}, ExplicitBounds: []float64{1, 5},
			Min: proto.Float64(0), Max: proto.Float64(9),
		}},
	}
	if !proto.Equal(sm.Metrics[2].GetHistogram(), wantH) {
		t.Errorf("int histogram:\n%v\n%v", sm.Metrics[2].GetHistogram(), wantH)
	}
	fh := sm.Metrics[3].GetHistogram().DataPoints[0]
	if fh.GetSum() != 3.5 || fh.GetMin() != 0.5 || fh.GetMax() != 3 || fh.Count != 2 {
		t.Errorf("float histogram: %v", fh)
	}
}

func TestConvertHistogramWithoutMinMax(t *testing.T) {
	rm := sdkFixture()
	h := rm.ScopeMetrics[0].Metrics[2].Data.(metricdata.Histogram[int64])
	h.DataPoints[0].Min, h.DataPoints[0].Max = metricdata.Extrema[int64]{}, metricdata.Extrema[int64]{}
	rm.ScopeMetrics[0].Metrics = rm.ScopeMetrics[0].Metrics[2:3]
	req, _ := ConvertMetrics(rm)
	dp := req.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetHistogram().DataPoints[0]
	if dp.Min != nil || dp.Max != nil {
		t.Errorf("undefined min/max must stay unset: %v", dp)
	}
}

func TestConvertEmptyIsNil(t *testing.T) {
	empty := &metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Metrics: []metricdata.Metrics{
		{Name: "a", Data: metricdata.Sum[int64]{Temporality: metricdata.DeltaTemporality, IsMonotonic: true}},
		{Name: "b", Data: metricdata.Histogram[float64]{Temporality: metricdata.DeltaTemporality}},
	}}}}
	for _, rm := range []*metricdata.ResourceMetrics{nil, {}, empty} {
		if req, err := ConvertMetrics(rm); req != nil || err != nil {
			t.Errorf("req=%v err=%v", req, err)
		}
	}
}

func TestConvertUnsupportedKinds(t *testing.T) {
	cases := map[string]metricdata.Aggregation{
		"gauge":      metricdata.Gauge[int64]{DataPoints: []metricdata.DataPoint[int64]{{Value: 1}}},
		"exp-hist":   metricdata.ExponentialHistogram[float64]{Temporality: metricdata.DeltaTemporality, DataPoints: []metricdata.ExponentialHistogramDataPoint[float64]{{}}},
		"summary":    metricdata.Summary{},
		"cumulative": metricdata.Sum[int64]{Temporality: metricdata.CumulativeTemporality, IsMonotonic: true},
		"updown":     metricdata.Sum[float64]{Temporality: metricdata.DeltaTemporality},
		"cumul-hist": metricdata.Histogram[int64]{Temporality: metricdata.CumulativeTemporality},
		"nil":        nil,
	}
	for name, agg := range cases {
		t.Run(name, func(t *testing.T) {
			rm := sdkFixture()
			rm.ScopeMetrics[0].Metrics = append(rm.ScopeMetrics[0].Metrics, metricdata.Metrics{Name: "bad", Data: agg})
			req, err := ConvertMetrics(rm)
			if req != nil || !errors.Is(err, ErrUnsupportedKind) {
				t.Fatalf("req=%v err=%v", req, err)
			}
			fx := newFakeFS(1000)
			e := NewMetricExporter(Config{Sink: NewFileSink("/spool", RoleUser, fx), Service: "pn"})
			if err := e.Export(context.Background(), rm); !errors.Is(err, ErrUnsupportedKind) {
				t.Errorf("Export err = %v", err)
			}
			if len(fx.opsList()) != 0 {
				t.Errorf("wrote on unsupported kind: %v", fx.opsList())
			}
		})
	}
}

func TestConvertDoesNotShareState(t *testing.T) {
	rm := sdkFixture()
	h := rm.ScopeMetrics[0].Metrics[2].Data.(metricdata.Histogram[int64])
	req, _ := ConvertMetrics(rm)
	req.ResourceMetrics[0].ScopeMetrics[0].Metrics[2].GetHistogram().DataPoints[0].BucketCounts[0] = 99
	if h.DataPoints[0].BucketCounts[0] != 1 {
		t.Error("converter aliases SDK slices")
	}
}

func TestMetricExporterTemporalityIsDelta(t *testing.T) {
	e := NewMetricExporter(Config{Sink: NewFileSink("/spool", RoleUser, newFakeFS(1000))})
	kinds := []sdkmetric.InstrumentKind{
		sdkmetric.InstrumentKindCounter, sdkmetric.InstrumentKindUpDownCounter, sdkmetric.InstrumentKindHistogram,
		sdkmetric.InstrumentKindGauge, sdkmetric.InstrumentKindObservableCounter,
		sdkmetric.InstrumentKindObservableUpDownCounter, sdkmetric.InstrumentKindObservableGauge,
	}
	for _, k := range kinds {
		if got := e.Temporality(k); got != metricdata.DeltaTemporality {
			t.Errorf("kind %v: temporality %v", k, got)
		}
		if e.Aggregation(k) == nil {
			t.Errorf("kind %v: nil aggregation", k)
		}
	}
}

func newTestMetricExporter(fx *fakeFS, role Role, errs *[]error, mu *sync.Mutex) sdkmetric.Exporter {
	return NewMetricExporter(Config{
		Sink: NewFileSink("/spool", role, fx), Service: "pn",
		Hooks: Hooks{OnError: func(err error) { mu.Lock(); *errs = append(*errs, err); mu.Unlock() }},
		Now:   func() time.Time { return time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC) }, Pid: 77,
	})
}

func TestMetricExportPublishAndRoundTrip(t *testing.T) {
	fx := newFakeFS(1000)
	var errs []error
	var mu sync.Mutex
	e := newTestMetricExporter(fx, RoleUser, &errs, &mu)
	in := sdkFixture()
	if err := e.Export(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := e.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	files := fx.published()
	if len(files) != 1 {
		t.Fatalf("published %d files", len(files))
	}
	for p, data := range files {
		base := filepath.Base(p)
		if !strings.HasPrefix(base, "pn-20261113T010203Z-77-") || !strings.HasSuffix(base, ".jsonl") || filepath.Dir(p) != "/spool/metrics" {
			t.Errorf("bad path %s", p)
		}
		head := data
		if len(head) > 1000 {
			head = head[:1000]
		}
		if !strings.Contains(string(head), FileIDAttribute) || !strings.Contains(string(head), base) {
			t.Errorf("first 1000 bytes lack the file id: %s", head)
		}
		md, err := (&pmetric.JSONUnmarshaler{}).UnmarshalMetrics(data)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := (&pmetric.ProtoMarshaler{}).MarshalMetrics(md)
		got := &colmetrics.ExportMetricsServiceRequest{}
		if err := proto.Unmarshal(b, got); err != nil {
			t.Fatal(err)
		}
		want, _ := ConvertMetrics(in)
		want.ResourceMetrics[0].Resource = PrependFileID(want.ResourceMetrics[0].Resource, base)
		if !proto.Equal(got, want) {
			t.Fatalf("round trip differs:\n%v\n%v", got, want)
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
	if len(errs) != 0 {
		t.Errorf("unexpected errors %v", errs)
	}
	// The SDK data must be untouched.
	if !proto.Equal(mustConvert(t, in), mustConvert(t, sdkFixture())) {
		t.Error("Export mutated the SDK data")
	}
}

func mustConvert(t *testing.T, rm *metricdata.ResourceMetrics) *colmetrics.ExportMetricsServiceRequest {
	t.Helper()
	req, err := ConvertMetrics(rm)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestMetricExportEmptyWritesNothing(t *testing.T) {
	fx := newFakeFS(1000)
	e := NewMetricExporter(Config{Sink: NewFileSink("/spool", RoleUser, fx), Service: "pn"})
	_ = e.Export(context.Background(), &metricdata.ResourceMetrics{})
	_ = e.Export(context.Background(), nil)
	if len(fx.opsList()) != 0 {
		t.Fatalf("an empty flush touched the file system: %v", fx.opsList())
	}
}

func TestMetricExportAfterShutdownDropped(t *testing.T) {
	fx := newFakeFS(1000)
	e := NewMetricExporter(Config{Sink: NewFileSink("/spool", RoleUser, fx), Service: "pn"})
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Export(context.Background(), sdkFixture()); err != nil || len(fx.opsList()) != 0 {
		t.Fatalf("err=%v ops=%v", err, fx.opsList())
	}
}

func TestMetricWriteFailuresAreSilentNoOps(t *testing.T) {
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
			e := newTestMetricExporter(fx, RoleUser, &errs, &mu)
			for i := 0; i < 3; i++ {
				if err := e.Export(context.Background(), sdkFixture()); err != nil {
					t.Fatalf("host must never see an error: %v", err)
				}
			}
			if len(errs) != 1 {
				t.Fatalf("OnError calls = %d, want exactly 1: %v", len(errs), errs)
			}
			if len(fx.published()) != 0 || len(fx.files) != 0 {
				t.Errorf("leftover files %v", fx.files)
			}
		})
	}
}

func TestMetricRenameENOENTIsTransient(t *testing.T) {
	fx := newFakeFS(1000)
	fx.renameErr = &fs.PathError{Op: "rename", Err: syscall.ENOENT}
	var errs []error
	var mu sync.Mutex
	e := newTestMetricExporter(fx, RoleUser, &errs, &mu)
	_ = e.Export(context.Background(), sdkFixture())
	if len(errs) != 1 || !errors.Is(errs[0], ErrTransient) {
		t.Fatalf("errs = %v", errs)
	}
	fx.renameErr = nil
	_ = e.Export(context.Background(), sdkFixture())
	if len(fx.published()) != 1 || len(errs) != 1 {
		t.Fatalf("must keep working: published=%d errs=%v", len(fx.published()), errs)
	}
}

func TestMetricUnsafeDirRejectedOnce(t *testing.T) {
	fx := newFakeFS(1000)
	fx.dirs["/spool"] = fakeDir{mode: 0o700, owner: 0}
	var errs []error
	var mu sync.Mutex
	e := newTestMetricExporter(fx, RoleUser, &errs, &mu)
	_ = e.Export(context.Background(), sdkFixture())
	_ = e.Export(context.Background(), sdkFixture())
	if len(errs) != 1 || !errors.Is(errs[0], ErrUnsafeDir) || len(fx.files) != 0 {
		t.Fatalf("errs=%v files=%v", errs, fx.files)
	}
}

func TestMetricRootRoleModes(t *testing.T) {
	fx := newFakeFS(0)
	e := NewMetricExporter(Config{Sink: NewFileSink("/spool", RoleRoot, fx), Service: "pn"})
	_ = e.Export(context.Background(), sdkFixture())
	if fx.perms["/spool/metrics"] != 0o755 {
		t.Errorf("perms %v", fx.perms)
	}
}
