package exporttest

import (
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

type (
	tracepbRS   = tracepb.ResourceSpans
	tracepbSpan = tracepb.Span
	histPoint   = metricspb.HistogramDataPoint
)

const metricsCumulative = metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE

func kvString(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
