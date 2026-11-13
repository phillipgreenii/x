package spool

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// ErrUnsupportedKind reports a metric kind the converter does not handle:
// anything but a monotonic delta Sum or a delta explicit-bucket Histogram.
var ErrUnsupportedKind = errors.New("spool: unsupported metric kind")

// metricExporter implements sdkmetric.Exporter by spooling to files.
type metricExporter struct {
	p       *publisher
	stopped atomic.Bool
}

// NewMetricExporter returns the sdkmetric.Exporter that writes each export as
// files under <dir>/metrics/. It is the same interface otlpmetrichttp.New
// returns, so the periodic reader, View and temporality wiring are shared.
// Temporality is delta for every instrument kind.
func NewMetricExporter(cfg Config) sdkmetric.Exporter {
	return &metricExporter{p: newPublisher(cfg)}
}

// Temporality implements sdkmetric.Exporter.
func (*metricExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.DeltaTemporality
}

// Aggregation implements sdkmetric.Exporter.
func (*metricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

// Export implements sdkmetric.Exporter. A spool failure returns nil (see
// Hooks); an unsupported kind is a programming error and is returned, with
// nothing written.
func (e *metricExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	if e.stopped.Load() {
		return nil
	}
	req, err := ConvertMetrics(rm)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	e.p.publish(SignalMetrics, func(name string) ([][]byte, error) {
		for _, r := range req.ResourceMetrics {
			r.Resource = PrependFileID(r.Resource, name)
		}
		return e.p.enc.EncodeMetrics(req)
	})
	return nil
}

// ForceFlush implements sdkmetric.Exporter; files are published synchronously.
func (*metricExporter) ForceFlush(context.Context) error { return nil }

// Shutdown implements sdkmetric.Exporter; later exports are dropped.
func (e *metricExporter) Shutdown(context.Context) error {
	e.stopped.Store(true)
	return nil
}

// ConvertMetrics converts the SDK's metric data to the standard OTLP request.
// It returns nil, nil when there is no data point to send, and an error
// wrapping ErrUnsupportedKind (and no request) for any kind other than
// monotonic delta Sum or delta explicit-bucket Histogram, Int64 or Float64.
func ConvertMetrics(rm *metricdata.ResourceMetrics) (*colmetrics.ExportMetricsServiceRequest, error) {
	if rm == nil {
		return nil, nil
	}
	out := &metricspb.ResourceMetrics{Resource: convertResource(rm.Resource)}
	if rm.Resource != nil {
		out.SchemaUrl = rm.Resource.SchemaURL()
	}
	for _, sm := range rm.ScopeMetrics {
		so := &metricspb.ScopeMetrics{Scope: convertScope(sm.Scope), SchemaUrl: sm.Scope.SchemaURL}
		for _, m := range sm.Metrics {
			mo, err := convertMetric(m)
			if err != nil {
				return nil, err
			}
			if mo != nil {
				so.Metrics = append(so.Metrics, mo)
			}
		}
		if len(so.Metrics) > 0 {
			out.ScopeMetrics = append(out.ScopeMetrics, so)
		}
	}
	if len(out.ScopeMetrics) == 0 {
		return nil, nil
	}
	return &colmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{out}}, nil
}

func convertMetric(m metricdata.Metrics) (*metricspb.Metric, error) {
	out := &metricspb.Metric{Name: m.Name, Description: m.Description, Unit: m.Unit}
	switch a := m.Data.(type) {
	case metricdata.Sum[int64]:
		return sumMetric(out, m, a.Temporality, a.IsMonotonic, numberPoints(a.DataPoints, intPoint))
	case metricdata.Sum[float64]:
		return sumMetric(out, m, a.Temporality, a.IsMonotonic, numberPoints(a.DataPoints, doublePoint))
	case metricdata.Histogram[int64]:
		return histMetric(out, m, a.Temporality, histPoints(a.DataPoints, func(v int64) float64 { return float64(v) }))
	case metricdata.Histogram[float64]:
		return histMetric(out, m, a.Temporality, histPoints(a.DataPoints, func(v float64) float64 { return v }))
	default:
		return nil, fmt.Errorf("%w: %q is %T", ErrUnsupportedKind, m.Name, m.Data)
	}
}

func sumMetric(out *metricspb.Metric, m metricdata.Metrics, t metricdata.Temporality, mono bool, pts []*metricspb.NumberDataPoint) (*metricspb.Metric, error) {
	if t != metricdata.DeltaTemporality || !mono {
		return nil, fmt.Errorf("%w: %q is a Sum with temporality %v monotonic %v (need monotonic delta)", ErrUnsupportedKind, m.Name, t, mono)
	}
	if len(pts) == 0 {
		return nil, nil
	}
	out.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{
		AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
		IsMonotonic:            true, DataPoints: pts,
	}}
	return out, nil
}

func histMetric(out *metricspb.Metric, m metricdata.Metrics, t metricdata.Temporality, pts []*metricspb.HistogramDataPoint) (*metricspb.Metric, error) {
	if t != metricdata.DeltaTemporality {
		return nil, fmt.Errorf("%w: %q is a Histogram with temporality %v (need delta)", ErrUnsupportedKind, m.Name, t)
	}
	if len(pts) == 0 {
		return nil, nil
	}
	out.Data = &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
		AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, DataPoints: pts,
	}}
	return out, nil
}

func numberPoints[N int64 | float64](dps []metricdata.DataPoint[N], val func(N) *metricspb.NumberDataPoint) []*metricspb.NumberDataPoint {
	out := make([]*metricspb.NumberDataPoint, 0, len(dps))
	for _, dp := range dps {
		p := val(dp.Value)
		p.Attributes = convertAttrs(dp.Attributes)
		p.StartTimeUnixNano = nanos(dp.StartTime)
		p.TimeUnixNano = nanos(dp.Time)
		out = append(out, p)
	}
	return out
}

func intPoint(v int64) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{Value: &metricspb.NumberDataPoint_AsInt{AsInt: v}}
}

func doublePoint(v float64) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: v}}
}

func histPoints[N int64 | float64](dps []metricdata.HistogramDataPoint[N], f func(N) float64) []*metricspb.HistogramDataPoint {
	out := make([]*metricspb.HistogramDataPoint, 0, len(dps))
	for _, dp := range dps {
		p := &metricspb.HistogramDataPoint{
			Attributes:        convertAttrs(dp.Attributes),
			StartTimeUnixNano: nanos(dp.StartTime),
			TimeUnixNano:      nanos(dp.Time),
			Count:             dp.Count,
			Sum:               proto.Float64(f(dp.Sum)),
			BucketCounts:      append([]uint64(nil), dp.BucketCounts...),
			ExplicitBounds:    append([]float64(nil), dp.Bounds...),
		}
		if v, ok := dp.Min.Value(); ok {
			p.Min = proto.Float64(f(v))
		}
		if v, ok := dp.Max.Value(); ok {
			p.Max = proto.Float64(f(v))
		}
		out = append(out, p)
	}
	return out
}

func nanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}

func convertResource(r *resource.Resource) *resourcepb.Resource {
	if r == nil {
		return nil
	}
	return &resourcepb.Resource{Attributes: convertKVs(r.Attributes())}
}

func convertScope(s instrumentation.Scope) *commonpb.InstrumentationScope {
	return &commonpb.InstrumentationScope{Name: s.Name, Version: s.Version, Attributes: convertAttrs(s.Attributes)}
}

func convertAttrs(s attribute.Set) []*commonpb.KeyValue {
	return convertKVs(s.ToSlice())
}

func convertKVs(kvs []attribute.KeyValue) []*commonpb.KeyValue {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]*commonpb.KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, &commonpb.KeyValue{Key: string(kv.Key), Value: convertValue(kv.Value)})
	}
	return out
}

func convertValue(v attribute.Value) *commonpb.AnyValue {
	switch v.Type() {
	case attribute.BOOL:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v.AsBool()}}
	case attribute.INT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v.AsInt64()}}
	case attribute.FLOAT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v.AsFloat64()}}
	case attribute.STRING:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v.AsString()}}
	case attribute.BOOLSLICE:
		return arrayOf(v.AsBoolSlice(), func(b bool) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: b}}
		})
	case attribute.INT64SLICE:
		return arrayOf(v.AsInt64Slice(), func(i int64) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: i}}
		})
	case attribute.FLOAT64SLICE:
		return arrayOf(v.AsFloat64Slice(), func(f float64) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: f}}
		})
	case attribute.STRINGSLICE:
		return arrayOf(v.AsStringSlice(), func(s string) *commonpb.AnyValue {
			return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
		})
	}
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v.Emit()}}
}

func arrayOf[T any](xs []T, f func(T) *commonpb.AnyValue) *commonpb.AnyValue {
	vals := make([]*commonpb.AnyValue, len(xs))
	for i, x := range xs {
		vals[i] = f(x)
	}
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: vals}}}
}
