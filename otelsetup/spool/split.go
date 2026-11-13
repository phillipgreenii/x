package spool

import (
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// splitTraces partitions req into requests of at most limit protobuf bytes
// (greedy at span granularity; a lone larger span gets its own request). Each
// part repeats its resource and scope. The input is not modified; spans are
// shared, not copied. An input with no spans yields no parts.
func splitTraces(req *coltrace.ExportTraceServiceRequest, limit int) []*coltrace.ExportTraceServiceRequest {
	var parts []*coltrace.ExportTraceServiceRequest
	var cur *coltrace.ExportTraceServiceRequest
	curSize := 0
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				sz := proto.Size(sp) + 16
				if cur != nil && curSize+sz > limit {
					cur = nil
				}
				if cur == nil {
					cur = &coltrace.ExportTraceServiceRequest{}
					parts = append(parts, cur)
					curSize = 0
				}
				ssOut := traceSlot(cur, rs, ss)
				ssOut.Spans = append(ssOut.Spans, sp)
				curSize += sz
			}
		}
	}
	return parts
}

// traceSlot returns the ScopeSpans of cur that mirrors rs and ss, creating them on first use.
func traceSlot(cur *coltrace.ExportTraceServiceRequest, rs *tracepb.ResourceSpans, ss *tracepb.ScopeSpans) *tracepb.ScopeSpans {
	var rsOut *tracepb.ResourceSpans
	if n := len(cur.ResourceSpans); n > 0 && cur.ResourceSpans[n-1].Resource == rs.Resource && cur.ResourceSpans[n-1].SchemaUrl == rs.SchemaUrl {
		rsOut = cur.ResourceSpans[n-1]
	} else {
		rsOut = &tracepb.ResourceSpans{Resource: rs.Resource, SchemaUrl: rs.SchemaUrl}
		cur.ResourceSpans = append(cur.ResourceSpans, rsOut)
	}
	if n := len(rsOut.ScopeSpans); n > 0 && rsOut.ScopeSpans[n-1].Scope == ss.Scope && rsOut.ScopeSpans[n-1].SchemaUrl == ss.SchemaUrl {
		return rsOut.ScopeSpans[n-1]
	}
	ssOut := &tracepb.ScopeSpans{Scope: ss.Scope, SchemaUrl: ss.SchemaUrl}
	rsOut.ScopeSpans = append(rsOut.ScopeSpans, ssOut)
	return ssOut
}

// splitMetrics is splitTraces at metric granularity.
func splitMetrics(req *colmetrics.ExportMetricsServiceRequest, limit int) []*colmetrics.ExportMetricsServiceRequest {
	var parts []*colmetrics.ExportMetricsServiceRequest
	var cur *colmetrics.ExportMetricsServiceRequest
	curSize := 0
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				sz := proto.Size(m) + 16
				if cur != nil && curSize+sz > limit {
					cur = nil
				}
				if cur == nil {
					cur = &colmetrics.ExportMetricsServiceRequest{}
					parts = append(parts, cur)
					curSize = 0
				}
				smOut := metricSlot(cur, rm, sm)
				smOut.Metrics = append(smOut.Metrics, m)
				curSize += sz
			}
		}
	}
	return parts
}

func metricSlot(cur *colmetrics.ExportMetricsServiceRequest, rm *metricspb.ResourceMetrics, sm *metricspb.ScopeMetrics) *metricspb.ScopeMetrics {
	var rmOut *metricspb.ResourceMetrics
	if n := len(cur.ResourceMetrics); n > 0 && cur.ResourceMetrics[n-1].Resource == rm.Resource && cur.ResourceMetrics[n-1].SchemaUrl == rm.SchemaUrl {
		rmOut = cur.ResourceMetrics[n-1]
	} else {
		rmOut = &metricspb.ResourceMetrics{Resource: rm.Resource, SchemaUrl: rm.SchemaUrl}
		cur.ResourceMetrics = append(cur.ResourceMetrics, rmOut)
	}
	if n := len(rmOut.ScopeMetrics); n > 0 && rmOut.ScopeMetrics[n-1].Scope == sm.Scope && rmOut.ScopeMetrics[n-1].SchemaUrl == sm.SchemaUrl {
		return rmOut.ScopeMetrics[n-1]
	}
	smOut := &metricspb.ScopeMetrics{Scope: sm.Scope, SchemaUrl: sm.SchemaUrl}
	rmOut.ScopeMetrics = append(rmOut.ScopeMetrics, smOut)
	return smOut
}
