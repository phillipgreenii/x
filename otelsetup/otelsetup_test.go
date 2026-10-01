package otelsetup

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// collector is an httptest OTLP/HTTP sink recording requests by path.
type collector struct {
	srv *httptest.Server
	mu  sync.Mutex
	// hits counts every request received.
	hits    int
	metrics []*colmetrics.ExportMetricsServiceRequest
	traces  int
}

func newCollector(t *testing.T) *collector {
	t.Helper()
	c := &collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err == nil {
				body = gz
			}
		}
		raw, _ := io.ReadAll(body)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.hits++
		switch r.URL.Path {
		case "/v1/metrics":
			req := &colmetrics.ExportMetricsServiceRequest{}
			if err := proto.Unmarshal(raw, req); err == nil {
				c.metrics = append(c.metrics, req)
			}
		case "/v1/traces":
			c.traces++
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *collector) snapshot() (hits, traces int, metrics []*colmetrics.ExportMetricsServiceRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.traces, append([]*colmetrics.ExportMetricsServiceRequest(nil), c.metrics...)
}

// sumValue finds the counter data point sum in a request.
func sumPoint(t *testing.T, req *colmetrics.ExportMetricsServiceRequest, name string) (int64, metricspb.AggregationTemporality) {
	t.Helper()
	for _, rm := range req.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					s := m.GetSum()
					if s == nil || len(s.DataPoints) == 0 {
						t.Fatalf("metric %s has no sum data points", name)
					}
					return s.DataPoints[0].GetAsInt(), s.AggregationTemporality
				}
			}
		}
	}
	t.Fatalf("metric %s not found in request", name)
	return 0, 0
}

func TestInit_NoopWhenEndpointEmpty_MakesNoNetworkCalls(t *testing.T) {
	c := newCollector(t) // exists, but must never be contacted
	ctx := context.Background()

	tel, err := Init(ctx, Config{ServiceName: "svc", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if tel.Enabled() {
		t.Fatal("empty endpoint must yield a disabled Telemetry")
	}
	_, span := tel.Tracer.Start(ctx, "op")
	span.End()
	ctr, err := tel.Meter.Int64Counter("x")
	if err != nil {
		t.Fatal(err)
	}
	ctr.Add(ctx, 1)
	if err := tel.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if hits, _, _ := c.snapshot(); hits != 0 {
		t.Fatalf("no-op made %d network calls, want 0", hits)
	}
}

func TestDelta_TwoExportsYieldOnlyIncrements(t *testing.T) {
	c := newCollector(t)
	ctx := context.Background()
	tel, err := Init(ctx, Config{Endpoint: c.srv.URL, ServiceName: "svc", Version: "1"},
		WithMetricInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ctr, err := tel.Meter.Int64Counter("nix.derivations")
	if err != nil {
		t.Fatal(err)
	}

	ctr.Add(ctx, 5)
	if err := tel.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	ctr.Add(ctx, 3)
	if err := tel.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	_, _, reqs := c.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("got %d metric exports, want >= 2", len(reqs))
	}
	v1, temp1 := sumPoint(t, reqs[0], "nix.derivations")
	v2, temp2 := sumPoint(t, reqs[1], "nix.derivations")
	if v1 != 5 || v2 != 3 {
		t.Fatalf("exports = %d then %d, want 5 then 3 (increments, not cumulative 5 then 8)", v1, v2)
	}
	if temp1 != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA ||
		temp2 != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
		t.Fatalf("temporality = %v/%v, want DELTA", temp1, temp2)
	}
}

func TestHistogram_ExplicitBuckets(t *testing.T) {
	c := newCollector(t)
	ctx := context.Background()
	want := []float64{1, 2, 3}
	tel, err := Init(ctx, Config{Endpoint: c.srv.URL, ServiceName: "svc"},
		WithHistogramBuckets(want), WithMetricInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := tel.Meter.Float64Histogram("pn.command.duration")
	h.Record(ctx, 1.5)
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, reqs := c.snapshot()
	var got []float64
	for _, r := range reqs {
		for _, rm := range r.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					if m.Name == "pn.command.duration" {
						got = m.GetHistogram().DataPoints[0].ExplicitBounds
					}
				}
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bounds = %v, want %v", got, want)
		}
	}
}

func TestSpansExported(t *testing.T) {
	c := newCollector(t)
	ctx := context.Background()
	tel, err := Init(ctx, Config{Endpoint: c.srv.URL, ServiceName: "svc"}, WithSpanQueueSize(16))
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.Tracer.Start(ctx, "op")
	span.End()
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, traces, _ := c.snapshot(); traces == 0 {
		t.Fatal("no trace export reached the collector")
	}
}

func TestBoundedWhenCollectorDown(t *testing.T) {
	// Reserve a port, then close it so connections are refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	ctx := context.Background()
	tel, err := Init(ctx, Config{Endpoint: "http://" + addr, ServiceName: "svc"},
		WithTimeout(500*time.Millisecond), WithMetricInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.Tracer.Start(ctx, "op")
	span.End()
	ctr, _ := tel.Meter.Int64Counter("c")
	ctr.Add(ctx, 1)

	start := time.Now()
	_ = tel.ForceFlush(ctx)
	_ = tel.Shutdown(ctx)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("flush+shutdown took %v with collector down, want bounded (~1s)", d)
	}
}

func TestBoundedWhenCollectorHangs(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-hang }))
	t.Cleanup(func() { close(hang); srv.Close() })

	ctx := context.Background()
	tel, err := Init(ctx, Config{Endpoint: srv.URL, ServiceName: "svc"},
		WithTimeout(300*time.Millisecond), WithMetricInterval(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.Tracer.Start(ctx, "op")
	span.End()
	start := time.Now()
	_ = tel.Shutdown(ctx)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("shutdown took %v against a hung collector, want bounded", d)
	}
}

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		in       string
		host     string
		insecure bool
		path     string
		wantErr  bool
	}{
		{"127.0.0.1:4318", "127.0.0.1:4318", true, "", false},
		{"http://h:4318", "h:4318", true, "", false},
		{"https://h:4318/pre/", "h:4318", false, "/pre", false},
		{"ftp://h", "", false, "", true},
		{"http://", "", false, "", true},
	}
	for _, tc := range cases {
		h, ins, p, err := parseEndpoint(tc.in)
		if (err != nil) != tc.wantErr || h != tc.host || ins != tc.insecure || p != tc.path {
			t.Errorf("parseEndpoint(%q) = %q,%v,%q,%v", tc.in, h, ins, p, err)
		}
	}
}

func TestInit_InvalidEndpoint(t *testing.T) {
	if _, err := Init(context.Background(), Config{Endpoint: "ftp://x"}); err == nil {
		t.Fatal("want error for unsupported scheme")
	}
}

func TestMetricsJSONUpToDate(t *testing.T) {
	want, err := MetricsJSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("metrics.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("metrics.json is stale; run `go generate ./otelsetup`")
	}
}
