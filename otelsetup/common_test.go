package otelsetup

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func collect(t *testing.T, r *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func TestRegisterCommonSignals(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r))
	start := time.Unix(1700000000, 0)
	sig, err := RegisterCommonSignals(mp.Meter("t"), "v1.2.3", start)
	if err != nil {
		t.Fatal(err)
	}
	sig.ObserveShutdown(context.Background(), 2*time.Second, true)
	sig.ObserveShutdown(context.Background(), time.Second, false)

	got := collect(t, r)
	st := got[MetricProcessStartTime].Data.(metricdata.Gauge[float64])
	if v := st.DataPoints[0].Value; v != 1700000000 {
		t.Errorf("start time = %v, want 1700000000", v)
	}
	bi := got[MetricBuildInfo].Data.(metricdata.Gauge[int64])
	if v, ok := bi.DataPoints[0].Attributes.Value("version"); !ok || v.AsString() != "v1.2.3" || bi.DataPoints[0].Value != 1 {
		t.Errorf("build.info = %+v", bi.DataPoints[0])
	}
	h := got[MetricShutdownDuration].Data.(metricdata.Histogram[float64])
	if h.DataPoints[0].Count != 2 || h.DataPoints[0].Sum != 3 {
		t.Errorf("shutdown.duration count/sum = %d/%v, want 2/3", h.DataPoints[0].Count, h.DataPoints[0].Sum)
	}
	o := got[MetricShutdownOverruns].Data.(metricdata.Sum[int64])
	if o.DataPoints[0].Value != 1 {
		t.Errorf("overruns = %d, want 1", o.DataPoints[0].Value)
	}
}

func TestObserveShutdownNilReceiver(t *testing.T) {
	var c *CommonSignals
	c.ObserveShutdown(context.Background(), time.Second, true) // must not panic
}
