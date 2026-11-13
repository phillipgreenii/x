package exporttest

import (
	"encoding/binary"
	"sort"
	"strconv"
	"strings"

	"github.com/phillipgreenii/x/otelsetup/spool"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
)

// Capture is everything one transport delivered for one scenario, as OTLP
// protobuf requests in arrival order.
type Capture struct {
	Traces  []*coltrace.ExportTraceServiceRequest
	Metrics []*colmetrics.ExportMetricsServiceRequest
	// Chunked marks a transport that splits by a record cap (the spool), so
	// TraceRecords and MetricRecords are meaningful for Scenario.WantSplit.
	Chunked       bool
	TraceRecords  int
	MetricRecords int
}

// Normalized is the comparable form of a Capture.
type Normalized struct {
	Traces  *coltrace.ExportTraceServiceRequest
	Metrics *colmetrics.ExportMetricsServiceRequest
}

// Normalize makes captures from different transports comparable. It
//
//   - drops the spool-only resource attribute pn.spool.file_id;
//   - merges requests, resources, scopes and same-named metrics, so how a
//     transport batches or splits does not matter, and drops empty ones;
//   - sorts resources, scopes, metrics, data points, spans and attributes
//     (event and link order inside a span is content and is kept);
//   - rewrites span timestamps relative to the earliest span start, and metric
//     timestamps to a set/unset flag (the SDK stamps metrics with the wall
//     clock; ordering is checked separately by checkMetricTimes);
//   - REMAPS trace and span ids by a stable bijection (labels in order of the
//     canonical sort), never blanks them, so parent and link relationships
//     remain part of the comparison.
func Normalize(c Capture) Normalized {
	return Normalized{Traces: normalizeTraces(c.Traces), Metrics: normalizeMetrics(c.Metrics)}
}

var detOpts = proto.MarshalOptions{Deterministic: true}

func key(m proto.Message) string {
	b, _ := detOpts.Marshal(m)
	return string(b)
}

// ---- attributes and resources ----

func sortKVs(kvs []*commonpb.KeyValue) {
	for _, kv := range kvs {
		sortValue(kv.GetValue())
	}
	sort.SliceStable(kvs, func(i, j int) bool { return kvs[i].GetKey() < kvs[j].GetKey() })
}

func sortValue(v *commonpb.AnyValue) {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_KvlistValue:
		sortKVs(x.KvlistValue.GetValues())
	case *commonpb.AnyValue_ArrayValue:
		for _, e := range x.ArrayValue.GetValues() {
			sortValue(e)
		}
	}
}

func normResource(r *resourcepb.Resource) *resourcepb.Resource {
	out := &resourcepb.Resource{}
	if r != nil {
		out = proto.Clone(r).(*resourcepb.Resource)
	}
	kept := out.Attributes[:0]
	for _, kv := range out.Attributes {
		if kv.GetKey() != spool.FileIDAttribute {
			kept = append(kept, kv)
		}
	}
	out.Attributes = kept
	sortKVs(out.Attributes)
	return out
}

func normScope(s *commonpb.InstrumentationScope) *commonpb.InstrumentationScope {
	out := &commonpb.InstrumentationScope{}
	if s != nil {
		out = proto.Clone(s).(*commonpb.InstrumentationScope)
	}
	sortKVs(out.Attributes)
	return out
}

// ---- traces ----

type spanRef struct {
	span *tracepb.Span
	key  string
}

type scopeGroup struct {
	scope *commonpb.InstrumentationScope
	url   string
	spans []spanRef
}

type resGroup struct {
	res    *resourcepb.Resource
	url    string
	scopes map[string]*scopeGroup
}

func normalizeTraces(reqs []*coltrace.ExportTraceServiceRequest) *coltrace.ExportTraceServiceRequest {
	groups := map[string]*resGroup{}
	var base uint64
	for _, req := range reqs {
		for _, rs := range req.GetResourceSpans() {
			res := normResource(rs.GetResource())
			rk := key(res) + "\x00" + rs.GetSchemaUrl()
			g := groups[rk]
			if g == nil {
				g = &resGroup{res: res, url: rs.GetSchemaUrl(), scopes: map[string]*scopeGroup{}}
				groups[rk] = g
			}
			for _, ss := range rs.GetScopeSpans() {
				sc := normScope(ss.GetScope())
				sk := key(sc) + "\x00" + ss.GetSchemaUrl()
				sg := g.scopes[sk]
				if sg == nil {
					sg = &scopeGroup{scope: sc, url: ss.GetSchemaUrl()}
					g.scopes[sk] = sg
				}
				for _, sp := range ss.GetSpans() {
					cl := proto.Clone(sp).(*tracepb.Span)
					sortKVs(cl.Attributes)
					for _, e := range cl.Events {
						sortKVs(e.Attributes)
					}
					for _, l := range cl.Links {
						sortKVs(l.Attributes)
					}
					if cl.StartTimeUnixNano != 0 && (base == 0 || cl.StartTimeUnixNano < base) {
						base = cl.StartTimeUnixNano
					}
					sg.spans = append(sg.spans, spanRef{span: cl})
				}
			}
		}
	}

	rel := func(v uint64) uint64 {
		if v == 0 {
			return 0
		}
		return v - base
	}
	blank := func(b []byte) []byte {
		if len(b) == 0 {
			return nil
		}
		return []byte{1}
	}
	for _, g := range groups {
		for _, sg := range g.scopes {
			for i := range sg.spans {
				sp := sg.spans[i].span
				sp.StartTimeUnixNano, sp.EndTimeUnixNano = rel(sp.StartTimeUnixNano), rel(sp.EndTimeUnixNano)
				for _, e := range sp.Events {
					e.TimeUnixNano = rel(e.TimeUnixNano)
				}
				// The sort key sees ids only as present/absent, so it cannot depend on random ids.
				k := proto.Clone(sp).(*tracepb.Span)
				k.TraceId, k.SpanId, k.ParentSpanId = blank(k.TraceId), blank(k.SpanId), blank(k.ParentSpanId)
				for _, l := range k.Links {
					l.TraceId, l.SpanId = blank(l.TraceId), blank(l.SpanId)
				}
				sg.spans[i].key = key(k)
			}
		}
	}

	ids := newIDMap()
	out := &coltrace.ExportTraceServiceRequest{}
	for _, rk := range sortedKeys(groups) {
		g := groups[rk]
		rsOut := &tracepb.ResourceSpans{Resource: g.res, SchemaUrl: g.url}
		for _, sk := range sortedKeys(g.scopes) {
			sg := g.scopes[sk]
			sort.SliceStable(sg.spans, func(i, j int) bool { return sg.spans[i].key < sg.spans[j].key })
			ssOut := &tracepb.ScopeSpans{Scope: sg.scope, SchemaUrl: sg.url}
			for _, s := range sg.spans {
				ssOut.Spans = append(ssOut.Spans, s.span)
			}
			if len(ssOut.Spans) > 0 {
				rsOut.ScopeSpans = append(rsOut.ScopeSpans, ssOut)
			}
		}
		if len(rsOut.ScopeSpans) > 0 {
			out.ResourceSpans = append(out.ResourceSpans, rsOut)
		}
	}
	// Two passes: labels for every span's own ids first (in canonical order), then
	// the ids they point at, so a parent met before its child gets the same label.
	for _, rs := range out.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				sp.TraceId, sp.SpanId = ids.trace(sp.TraceId), ids.span(sp.SpanId)
			}
		}
	}
	for _, rs := range out.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if len(sp.ParentSpanId) > 0 {
					sp.ParentSpanId = ids.span(sp.ParentSpanId)
				}
				for _, l := range sp.Links {
					l.TraceId, l.SpanId = ids.trace(l.TraceId), ids.span(l.SpanId)
				}
			}
		}
	}
	if len(out.ResourceSpans) == 0 {
		return &coltrace.ExportTraceServiceRequest{}
	}
	return out
}

type idMap struct {
	traces, spans map[string]uint64
}

func newIDMap() *idMap { return &idMap{traces: map[string]uint64{}, spans: map[string]uint64{}} }

func (m *idMap) trace(id []byte) []byte {
	n, ok := m.traces[string(id)]
	if !ok {
		n = uint64(len(m.traces) + 1)
		m.traces[string(id)] = n
	}
	out := make([]byte, 16)
	binary.BigEndian.PutUint64(out[8:], n)
	return out
}

func (m *idMap) span(id []byte) []byte {
	n, ok := m.spans[string(id)]
	if !ok {
		n = uint64(len(m.spans) + 1)
		m.spans[string(id)] = n
	}
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, n)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ---- metrics ----

type metricGroup struct {
	metric *metricspb.Metric
	// points are the number or histogram points with their sort keys.
	numbers []keyed[*metricspb.NumberDataPoint]
	hists   []keyed[*metricspb.HistogramDataPoint]
}

type keyed[T any] struct {
	v   T
	key string
}

type metricScope struct {
	scope   *commonpb.InstrumentationScope
	url     string
	metrics map[string]*metricGroup
}

type metricRes struct {
	res    *resourcepb.Resource
	url    string
	scopes map[string]*metricScope
}

func flag(v uint64) uint64 {
	if v != 0 {
		return 1
	}
	return 0
}

func normalizeMetrics(reqs []*colmetrics.ExportMetricsServiceRequest) *colmetrics.ExportMetricsServiceRequest {
	groups := map[string]*metricRes{}
	for _, req := range reqs {
		for _, rm := range req.GetResourceMetrics() {
			res := normResource(rm.GetResource())
			rk := key(res) + "\x00" + rm.GetSchemaUrl()
			g := groups[rk]
			if g == nil {
				g = &metricRes{res: res, url: rm.GetSchemaUrl(), scopes: map[string]*metricScope{}}
				groups[rk] = g
			}
			for _, sm := range rm.GetScopeMetrics() {
				sc := normScope(sm.GetScope())
				sk := key(sc) + "\x00" + sm.GetSchemaUrl()
				sg := g.scopes[sk]
				if sg == nil {
					sg = &metricScope{scope: sc, url: sm.GetSchemaUrl(), metrics: map[string]*metricGroup{}}
					g.scopes[sk] = sg
				}
				for _, m := range sm.GetMetrics() {
					mergeMetric(sg, m)
				}
			}
		}
	}
	out := &colmetrics.ExportMetricsServiceRequest{}
	for _, rk := range sortedKeys(groups) {
		g := groups[rk]
		rmOut := &metricspb.ResourceMetrics{Resource: g.res, SchemaUrl: g.url}
		for _, sk := range sortedKeys(g.scopes) {
			sg := g.scopes[sk]
			smOut := &metricspb.ScopeMetrics{Scope: sg.scope, SchemaUrl: sg.url}
			for _, mk := range sortedKeys(sg.metrics) {
				if m := sg.metrics[mk].build(); m != nil {
					smOut.Metrics = append(smOut.Metrics, m)
				}
			}
			if len(smOut.Metrics) > 0 {
				rmOut.ScopeMetrics = append(rmOut.ScopeMetrics, smOut)
			}
		}
		if len(rmOut.ScopeMetrics) > 0 {
			out.ResourceMetrics = append(out.ResourceMetrics, rmOut)
		}
	}
	return out
}

func attrKey(kvs []*commonpb.KeyValue) string {
	return key(&commonpb.KeyValue{Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{Values: kvs}}}})
}

// mergeMetric folds m into sg. Metrics with the same name, unit, description,
// kind, temporality and monotonicity are one metric whose points are the union.
func mergeMetric(sg *metricScope, m *metricspb.Metric) {
	head := &metricspb.Metric{Name: m.Name, Description: m.Description, Unit: m.Unit, Metadata: m.Metadata}
	var k strings.Builder
	k.WriteString(key(head))
	cl := proto.Clone(m).(*metricspb.Metric)
	var grp *metricGroup
	get := func(kind string) *metricGroup {
		k.WriteString("\x00" + kind)
		g := sg.metrics[k.String()]
		if g == nil {
			g = &metricGroup{}
			sg.metrics[k.String()] = g
		}
		return g
	}
	switch d := cl.Data.(type) {
	case *metricspb.Metric_Sum:
		k.WriteString(d.Sum.AggregationTemporality.String())
		if d.Sum.IsMonotonic {
			k.WriteString("mono")
		}
		grp = get("sum")
		grp.metric = &metricspb.Metric{
			Name: m.Name, Description: m.Description, Unit: m.Unit, Metadata: m.Metadata,
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{AggregationTemporality: d.Sum.AggregationTemporality, IsMonotonic: d.Sum.IsMonotonic}},
		}
		for _, p := range d.Sum.DataPoints {
			sortKVs(p.Attributes)
			p.StartTimeUnixNano, p.TimeUnixNano = flag(p.StartTimeUnixNano), flag(p.TimeUnixNano)
			grp.numbers = append(grp.numbers, keyed[*metricspb.NumberDataPoint]{p, attrKey(p.Attributes) + key(p)})
		}
	case *metricspb.Metric_Gauge:
		grp = get("gauge")
		grp.metric = &metricspb.Metric{
			Name: m.Name, Description: m.Description, Unit: m.Unit, Metadata: m.Metadata,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{}},
		}
		for _, p := range d.Gauge.DataPoints {
			sortKVs(p.Attributes)
			p.StartTimeUnixNano, p.TimeUnixNano = flag(p.StartTimeUnixNano), flag(p.TimeUnixNano)
			grp.numbers = append(grp.numbers, keyed[*metricspb.NumberDataPoint]{p, attrKey(p.Attributes) + key(p)})
		}
	case *metricspb.Metric_Histogram:
		k.WriteString(d.Histogram.AggregationTemporality.String())
		grp = get("hist")
		grp.metric = &metricspb.Metric{
			Name: m.Name, Description: m.Description, Unit: m.Unit, Metadata: m.Metadata,
			Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{AggregationTemporality: d.Histogram.AggregationTemporality}},
		}
		for _, p := range d.Histogram.DataPoints {
			sortKVs(p.Attributes)
			p.StartTimeUnixNano, p.TimeUnixNano = flag(p.StartTimeUnixNano), flag(p.TimeUnixNano)
			grp.hists = append(grp.hists, keyed[*metricspb.HistogramDataPoint]{p, attrKey(p.Attributes) + key(p)})
		}
	default:
		// Kinds the contract does not model (the spool rejects them) are kept
		// verbatim, so a transport that emits one still compares unequal.
		grp = get("other:" + key(cl))
		grp.metric = cl
	}
}

func (g *metricGroup) build() *metricspb.Metric {
	sort.SliceStable(g.numbers, func(i, j int) bool { return g.numbers[i].key < g.numbers[j].key })
	sort.SliceStable(g.hists, func(i, j int) bool { return g.hists[i].key < g.hists[j].key })
	switch d := g.metric.Data.(type) {
	case *metricspb.Metric_Sum:
		for _, p := range g.numbers {
			d.Sum.DataPoints = append(d.Sum.DataPoints, p.v)
		}
		if len(d.Sum.DataPoints) == 0 {
			return nil
		}
	case *metricspb.Metric_Gauge:
		for _, p := range g.numbers {
			d.Gauge.DataPoints = append(d.Gauge.DataPoints, p.v)
		}
		if len(d.Gauge.DataPoints) == 0 {
			return nil
		}
	case *metricspb.Metric_Histogram:
		for _, p := range g.hists {
			d.Histogram.DataPoints = append(d.Histogram.DataPoints, p.v)
		}
		if len(d.Histogram.DataPoints) == 0 {
			return nil
		}
	}
	return g.metric
}

// checkMetricTimes asserts what Normalize discards: every point has a start
// time not after its time.
func checkMetricTimes(reqs []*colmetrics.ExportMetricsServiceRequest) []string {
	var bad []string
	chk := func(name string, start, end uint64) {
		if start == 0 || end == 0 || start > end {
			bad = append(bad, name)
		}
	}
	for _, req := range reqs {
		for _, rm := range req.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					for _, p := range m.GetSum().GetDataPoints() {
						chk(m.Name, p.StartTimeUnixNano, p.TimeUnixNano)
					}
					for _, p := range m.GetGauge().GetDataPoints() {
						chk(m.Name, p.StartTimeUnixNano, p.TimeUnixNano)
					}
					for _, p := range m.GetHistogram().GetDataPoints() {
						chk(m.Name, p.StartTimeUnixNano, p.TimeUnixNano)
					}
				}
			}
		}
	}
	return bad
}

// equalDiff compares two normalized messages and returns "" when equal, else a
// description naming the first differing line of their text forms.
func equalDiff(a, b proto.Message) string {
	if proto.Equal(a, b) {
		return ""
	}
	la := strings.Split(format(a), "\n")
	lb := strings.Split(format(b), "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			return "first difference at line " + strconv.Itoa(i+1) + ":\n  oracle: " + strings.TrimSpace(x) + "\n  other:  " + strings.TrimSpace(y)
		}
	}
	return "messages differ"
}

func format(m proto.Message) string {
	return prototext.MarshalOptions{Multiline: true}.Format(m)
}
