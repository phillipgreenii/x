package exporttest

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"net/http"
	"testing"
	"time"

	"github.com/phillipgreenii/x/otelsetup/spool"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// NEGATIVE CONTROLS (results). Each was applied BY HAND once to the spool path
// (a temporary edit to a production file, reverted before the commit; not a
// mutation-tool run), and RunContract(HTTP, Spool(JSONCodec),
// Spool(JSONCodecIndependent)) was run against it:
//
//	drop a span event (spool trace upload)   -> FAILED: traces, nested-spans and large-batch
//	change a bucket bound (histPoints)       -> FAILED: metrics, histograms and multi-scope
//	flip delta to cumulative (sum and hist)  -> FAILED: metrics, delta-counters, histograms,
//	                                            multi-scope and large-batch
//	drop Min (histPoints)                    -> FAILED: metrics, histograms and multi-scope
//	reverse attribute order and ScopeMetrics -> PASSED (normalization absorbs it)
//	production-isolation test with a file
//	  importing net/http/httptest in spool   -> FAILED, passes again once removed
//
// TestNormalizeDetectsPerturbations and TestNormalizeAbsorbsReordering pin the
// same detections on captured data, so they stay in the repo as a guard.

func TestContractHTTPVsSpool(t *testing.T) {
	RunContract(t, HTTP(), Spool(JSONCodec()), Spool(JSONCodecIndependent()))
}

// base64Codec is a second, deliberately trivial Encoder: one base64 line per
// request, protobuf inside. Registering it is all the swap contract takes.
func base64Codec(drop func(*coltrace.ExportTraceServiceRequest)) Codec {
	return Codec{
		Name:       "base64-proto",
		Unbounded:  true,
		NewEncoder: func(int) spool.Encoder { return b64Encoder{drop: drop} },
		DecodeTraces: func(file []byte) ([]*coltrace.ExportTraceServiceRequest, error) {
			var out []*coltrace.ExportTraceServiceRequest
			for _, l := range lines(file) {
				raw, err := base64.StdEncoding.DecodeString(string(l))
				if err != nil {
					return nil, err
				}
				m := &coltrace.ExportTraceServiceRequest{}
				if err := proto.Unmarshal(raw, m); err != nil {
					return nil, err
				}
				out = append(out, m)
			}
			return out, nil
		},
		DecodeMetrics: func(file []byte) ([]*colmetrics.ExportMetricsServiceRequest, error) {
			var out []*colmetrics.ExportMetricsServiceRequest
			for _, l := range lines(file) {
				raw, err := base64.StdEncoding.DecodeString(string(l))
				if err != nil {
					return nil, err
				}
				m := &colmetrics.ExportMetricsServiceRequest{}
				if err := proto.Unmarshal(raw, m); err != nil {
					return nil, err
				}
				out = append(out, m)
			}
			return out, nil
		},
	}
}

type b64Encoder struct {
	drop func(*coltrace.ExportTraceServiceRequest)
}

func (b64Encoder) Extension() string { return ".b64" }

func (e b64Encoder) EncodeTraces(req *coltrace.ExportTraceServiceRequest) ([][]byte, error) {
	if e.drop != nil {
		req = proto.Clone(req).(*coltrace.ExportTraceServiceRequest)
		e.drop(req)
	}
	raw, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	return [][]byte{append([]byte(base64.StdEncoding.EncodeToString(raw)), '\n')}, nil
}

func (b64Encoder) EncodeMetrics(req *colmetrics.ExportMetricsServiceRequest) ([][]byte, error) {
	raw, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	return [][]byte{append([]byte(base64.StdEncoding.EncodeToString(raw)), '\n')}, nil
}

func TestSecondEncoderPasses(t *testing.T) {
	RunContract(t, HTTP(), Spool(base64Codec(nil)))
}

func TestEncoderDroppingAFieldFails(t *testing.T) {
	dropStatus := func(r *coltrace.ExportTraceServiceRequest) {
		for _, rs := range r.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					sp.Status = nil
				}
			}
		}
	}
	ms := ContractMismatches(t.TempDir, HTTP(), Spool(base64Codec(dropStatus)))
	if len(ms) == 0 {
		t.Fatal("an Encoder that drops span status passed the contract")
	}
	for _, m := range ms {
		if m.Signal != "traces" {
			t.Errorf("unexpected mismatch %v", m)
		}
	}
}

func TestFailingTransportIsReported(t *testing.T) {
	ms := ContractMismatches(t.TempDir, HTTP(), failing{})
	if len(ms) != len(Scenarios()) {
		t.Fatalf("got %d mismatches, want one per scenario: %v", len(ms), ms)
	}
}

type failing struct{}

func (failing) Name() string { return "failing" }
func (failing) Run(context.Context, string, Scenario) (Capture, error) {
	return Capture{}, fmt.Errorf("boom")
}

func TestContractNeedsAnOracleAndAnother(t *testing.T) {
	if ms := ContractMismatches(t.TempDir, HTTP()); len(ms) == 0 {
		t.Fatal("a single transport must not pass vacuously")
	}
}

// ---- Normalize on captured data ----

func capture(t *testing.T, name string) Capture {
	t.Helper()
	for _, s := range Scenarios() {
		if s.Name == name {
			c, err := HTTP().Run(context.Background(), t.TempDir(), s)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
	}
	t.Fatalf("no scenario %q", name)
	return Capture{}
}

func cloneCapture(c Capture) Capture {
	var out Capture
	for _, r := range c.Traces {
		out.Traces = append(out.Traces, proto.Clone(r).(*coltrace.ExportTraceServiceRequest))
	}
	for _, r := range c.Metrics {
		out.Metrics = append(out.Metrics, proto.Clone(r).(*colmetrics.ExportMetricsServiceRequest))
	}
	return out
}

func same(t *testing.T, a, b Capture) {
	t.Helper()
	na, nb := Normalize(a), Normalize(b)
	if d := equalDiff(na.Traces, nb.Traces); d != "" {
		t.Errorf("traces differ: %s", d)
	}
	if d := equalDiff(na.Metrics, nb.Metrics); d != "" {
		t.Errorf("metrics differ: %s", d)
	}
}

func differ(t *testing.T, what string, a, b Capture) {
	t.Helper()
	na, nb := Normalize(a), Normalize(b)
	if equalDiff(na.Traces, nb.Traces) == "" && equalDiff(na.Metrics, nb.Metrics) == "" {
		t.Errorf("%s was not detected", what)
	}
}

func TestNormalizeIsStableAcrossRuns(t *testing.T) {
	// Two runs share content but not ids or metric timestamps.
	same(t, capture(t, "nested-spans"), capture(t, "nested-spans"))
	same(t, capture(t, "histograms"), capture(t, "histograms"))
}

func TestNormalizeAbsorbsReordering(t *testing.T) {
	c := capture(t, "multi-scope")
	n := cloneCapture(c)
	rng := rand.New(rand.NewPCG(1, 2))
	for _, r := range n.Traces {
		rng.Shuffle(len(r.ResourceSpans), func(i, j int) { r.ResourceSpans[i], r.ResourceSpans[j] = r.ResourceSpans[j], r.ResourceSpans[i] })
		for _, rs := range r.ResourceSpans {
			rng.Shuffle(len(rs.ScopeSpans), func(i, j int) { rs.ScopeSpans[i], rs.ScopeSpans[j] = rs.ScopeSpans[j], rs.ScopeSpans[i] })
			a := rs.Resource.Attributes
			rng.Shuffle(len(a), func(i, j int) { a[i], a[j] = a[j], a[i] })
			for _, ss := range rs.ScopeSpans {
				rng.Shuffle(len(ss.Spans), func(i, j int) { ss.Spans[i], ss.Spans[j] = ss.Spans[j], ss.Spans[i] })
			}
		}
	}
	for _, r := range n.Metrics {
		for _, rm := range r.ResourceMetrics {
			rng.Shuffle(len(rm.ScopeMetrics), func(i, j int) { rm.ScopeMetrics[i], rm.ScopeMetrics[j] = rm.ScopeMetrics[j], rm.ScopeMetrics[i] })
			for _, sm := range rm.ScopeMetrics {
				rng.Shuffle(len(sm.Metrics), func(i, j int) { sm.Metrics[i], sm.Metrics[j] = sm.Metrics[j], sm.Metrics[i] })
			}
		}
	}
	same(t, c, n)

	// Splitting one request into two is also absorbed.
	cs := capture(t, "nested-spans")
	sp := cloneCapture(cs)
	spans := sp.Traces[0].ResourceSpans[0].ScopeSpans[0].Spans
	half := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepbRS{proto.Clone(sp.Traces[0].ResourceSpans[0]).(*tracepbRS)}}
	half.ResourceSpans[0].ScopeSpans = half.ResourceSpans[0].ScopeSpans[:1]
	half.ResourceSpans[0].ScopeSpans[0].Spans = spans[len(spans)/2:]
	sp.Traces[0].ResourceSpans[0].ScopeSpans[0].Spans = spans[:len(spans)/2]
	sp.Traces = append(sp.Traces, half)
	same(t, cs, sp)
}

func TestNormalizeDetectsPerturbations(t *testing.T) {
	traces := capture(t, "nested-spans")
	hist := capture(t, "histograms")

	firstSpan := func(c Capture) *tracepbSpan { return c.Traces[0].ResourceSpans[0].ScopeSpans[0].Spans[0] }
	findSpan := func(c Capture, name string) *tracepbSpan {
		for _, rs := range c.Traces[0].ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					if sp.Name == name {
						return sp
					}
				}
			}
		}
		t.Fatalf("no span %q", name)
		return nil
	}

	c := cloneCapture(traces)
	sp := findSpan(c, "child")
	sp.Events = sp.Events[1:]
	differ(t, "a dropped span event", traces, c)

	c = cloneCapture(traces)
	sp = findSpan(c, "grandchild")
	sp.ParentSpanId = firstSpan(c).SpanId // re-parent: ids are remapped, relations must still count
	differ(t, "a changed parent", traces, c)

	c = cloneCapture(traces)
	findSpan(c, "root").Links[0].SpanId = findSpan(c, "child").SpanId
	differ(t, "a changed link target", traces, c)

	c = cloneCapture(traces)
	findSpan(c, "root").Status.Message = "other"
	differ(t, "a changed status", traces, c)

	c = cloneCapture(traces)
	c.Traces[0].ResourceSpans[0].ScopeSpans[0].Spans[0].EndTimeUnixNano++
	differ(t, "a changed span end", traces, c)

	h := func(c Capture) *histPoint {
		for _, rm := range c.Metrics[0].ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name == "contract.size" {
						return m.GetHistogram().DataPoints[0]
					}
				}
			}
		}
		t.Fatal("no contract.size")
		return nil
	}
	c = cloneCapture(hist)
	h(c).ExplicitBounds[1]++
	differ(t, "a changed bucket bound", hist, c)

	c = cloneCapture(hist)
	h(c).Min = nil
	differ(t, "a dropped Min", hist, c)

	c = cloneCapture(hist)
	h(c).Max = nil
	differ(t, "a dropped Max", hist, c)

	c = cloneCapture(hist)
	for _, rm := range c.Metrics[0].ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if hh := m.GetHistogram(); hh != nil {
					hh.AggregationTemporality = metricsCumulative
				}
			}
		}
	}
	differ(t, "delta flipped to cumulative", hist, c)

	c = cloneCapture(hist)
	h(c).BucketCounts[0]++
	differ(t, "a changed bucket count", hist, c)
}

func TestNormalizeDropsOnlyTheFileID(t *testing.T) {
	c := capture(t, "nested-spans")
	n := cloneCapture(c)
	res := n.Traces[0].ResourceSpans[0].Resource
	spool.PrependFileID(res, "some-file")
	same(t, c, n)
	res.Attributes = append(res.Attributes, kvString("pn.spool.other", "x"))
	differ(t, "an extra resource attribute", c, n)
}

func TestCheckMetricTimes(t *testing.T) {
	c := capture(t, "delta-counters")
	if bad := checkMetricTimes(c.Metrics); len(bad) != 0 {
		t.Fatalf("real SDK data flagged: %v", bad)
	}
	n := cloneCapture(c)
	p := n.Metrics[0].ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetSum().DataPoints[0]
	p.StartTimeUnixNano = p.TimeUnixNano + 1
	if bad := checkMetricTimes(n.Metrics); len(bad) == 0 {
		t.Fatal("start after time was not flagged")
	}
}

// ---- LoopbackReceiver ----

func TestLoopbackReceiverRejectsGarbage(t *testing.T) {
	r := NewLoopbackReceiver()
	defer r.Close()
	post := func(path, ct string, body []byte) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+r.Addr()+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", ct)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("/v1/traces", "application/json", nil); c != http.StatusUnsupportedMediaType {
		t.Errorf("json: %d", c)
	}
	if c := post("/v1/traces", "application/x-protobuf", []byte{0xff, 0xff}); c != http.StatusBadRequest {
		t.Errorf("garbage: %d", c)
	}
	if c := post("/v1/other", "application/x-protobuf", nil); c != http.StatusNotFound {
		t.Errorf("path: %d", c)
	}
	if c := post("/v1/metrics", "application/x-protobuf", nil); c != http.StatusOK {
		t.Errorf("empty metrics: %d", c)
	}
	if len(r.Errs()) != 3 || len(r.Metrics()) != 1 || len(r.Traces()) != 0 {
		t.Errorf("errs=%d metrics=%d traces=%d", len(r.Errs()), len(r.Metrics()), len(r.Traces()))
	}
}
