package otelsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/x/otelsetup/exporttest"
	"github.com/phillipgreenii/x/otelsetup/spool"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func listFiles(dir, signal string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(dir, signal))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		out = append(out, filepath.Join(dir, signal, e.Name()))
	}
	return out, nil
}

func spoolFilesIn(t *testing.T, dir, signal string) []string {
	t.Helper()
	out, err := listFiles(dir, signal)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func spoolMetrics(t *testing.T, dir string) []*colmetrics.ExportMetricsServiceRequest {
	t.Helper()
	var out []*colmetrics.ExportMetricsServiceRequest
	for _, f := range spoolFilesIn(t, dir, spool.SignalMetrics) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		reqs, err := exporttest.JSONCodec().DecodeMetrics(b)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, reqs...)
	}
	return out
}

// sumOf adds up the int sum points named name whose attributes include every
// want pair, across all requests.
func sumOf(reqs []*colmetrics.ExportMetricsServiceRequest, name string, want map[string]string) int64 {
	var total int64
	for _, r := range reqs {
		for _, rm := range r.GetResourceMetrics() {
			for _, sm := range rm.GetScopeMetrics() {
				for _, m := range sm.GetMetrics() {
					if m.GetName() != name {
						continue
					}
					for _, dp := range m.GetSum().GetDataPoints() {
						if hasAttrs(dp, want) {
							total += dp.GetAsInt()
						}
					}
				}
			}
		}
	}
	return total
}

func hasAttrs(dp *metricspb.NumberDataPoint, want map[string]string) bool {
	got := map[string]string{}
	for _, kv := range dp.GetAttributes() {
		got[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestInit_SpoolDirReachesTheSink(t *testing.T) {
	dir := t.TempDir()
	tel, err := Init(context.Background(), Config{SpoolDir: dir, ServiceName: "svc", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !tel.Enabled() {
		t.Fatal("spool mode must be enabled")
	}
	ctx := context.Background()
	_, sp := tel.Tracer.Start(ctx, "op")
	sp.End()
	c, _ := tel.Meter.Int64Counter("t.count")
	c.Add(ctx, 3)
	if err := tel.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(spoolFilesIn(t, dir, spool.SignalTraces)) == 0 {
		t.Fatal("no trace file after ForceFlush")
	}
	c.Add(ctx, 4)
	if err := tel.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sumOf(spoolMetrics(t, dir), "t.count", nil); got != 7 {
		t.Fatalf("t.count delta sum = %d, want 7 (3 then 4)", got)
	}
}

func TestInit_SpoolConfigErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Init(ctx, Config{SpoolDir: "relative/dir"}); err == nil {
		t.Error("relative SpoolDir must be an error")
	}
	if _, err := Init(ctx, Config{SpoolDir: t.TempDir(), Endpoint: "127.0.0.1:4318"}); err == nil {
		t.Error("Endpoint and SpoolDir together must be an error")
	}
}

func TestInit_EmptyConfigTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	tel, err := Init(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if tel.Enabled() {
		t.Fatal("empty config must be the no-op")
	}
	_, sp := tel.Tracer.Start(context.Background(), "x")
	sp.End()
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("no-op created %d entries", len(ents))
	}
}

func TestInit_UnwritableSpoolYieldsWorkingNoopAndReportsError(t *testing.T) {
	// The spool "directory" lives under a regular file, so no uid can create it.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var errs []error
	tel, err := Init(context.Background(), Config{SpoolDir: filepath.Join(blocker, "spool"), ServiceName: "svc"},
		WithOnError(func(e error) { mu.Lock(); errs = append(errs, e); mu.Unlock() }))
	if err != nil {
		t.Fatalf("an unwritable spool must not fail Init: %v", err)
	}
	ctx := context.Background()
	_, sp := tel.Tracer.Start(ctx, "op")
	sp.End()
	c, _ := tel.Meter.Int64Counter("t.count")
	c.Add(ctx, 1)
	_ = tel.ForceFlush(ctx)
	_ = tel.Shutdown(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(errs) == 0 {
		t.Fatal("OnError never received the write failure")
	}
}

func TestInit_WriteErrorsCounter(t *testing.T) {
	dir := t.TempDir()
	// A file where the traces directory must be: trace writes fail, metric
	// writes still work, so the counter can be read back from the sink.
	if err := os.WriteFile(filepath.Join(dir, spool.SignalTraces), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var n int
	var mu sync.Mutex
	tel, err := Init(context.Background(), Config{SpoolDir: dir, ServiceName: "svc"},
		WithOnError(func(error) { mu.Lock(); n++; mu.Unlock() }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, sp := tel.Tracer.Start(ctx, "op")
	sp.End()
	_ = tel.ForceFlush(ctx) // the trace write fails here
	_ = tel.ForceFlush(ctx) // so the counter delta is certainly in this flush
	_ = tel.Shutdown(ctx)
	mu.Lock()
	hooks := n
	mu.Unlock()
	if hooks == 0 {
		t.Fatal("OnError not called")
	}
	if got := sumOf(spoolMetrics(t, dir), "pn.spool.write_errors", map[string]string{"spool": "user"}); got != int64(hooks) {
		t.Fatalf("write_errors = %d, want %d (one per OnError call)", got, hooks)
	}
}

func TestInit_SweepCountersRecordedAfterProviderExists(t *testing.T) {
	dir := t.TempDir()
	sigDir := filepath.Join(dir, spool.SignalTraces)
	if err := os.MkdirAll(sigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(sigDir, "svc-20200101T000000Z-1-deadbeef-1.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	tel, err := Init(context.Background(), Config{SpoolDir: dir, ServiceName: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file was not swept: %v", err)
	}
	got := sumOf(spoolMetrics(t, dir), "pn.spool.swept_files", map[string]string{"spool": "user", "reason": "age"})
	if got != 1 {
		t.Fatalf("swept_files{user,age} = %d, want 1", got)
	}
}

// fixedProvider hands every scope the one Tracer Init returned, so a contract
// Scenario (which asks for several scopes) can run through Init, which exposes
// a single Tracer and Meter. Both transports see the same collapse.
type fixedProvider struct {
	embedded.TracerProvider
	tr trace.Tracer
}

func (p fixedProvider) Tracer(string, ...trace.TracerOption) trace.Tracer { return p.tr }

type fixedMeterProvider struct {
	metric.MeterProvider
	m metric.Meter
}

func (p fixedMeterProvider) Meter(string, ...metric.MeterOption) metric.Meter { return p.m }

// initTransport is an exporttest.Transport that goes through Init only; the
// transport is nothing but a Config.
type initTransport struct {
	name string
	cfg  func(dir string, rcv *exporttest.LoopbackReceiver) Config
}

func (t initTransport) Name() string { return t.name }

func (t initTransport) Run(ctx context.Context, dir string, s exporttest.Scenario) (exporttest.Capture, error) {
	rcv := exporttest.NewLoopbackReceiver()
	defer rcv.Close()
	cfg := t.cfg(dir, rcv)
	cfg.ServiceName, cfg.Version = "contract", "9.9.9"
	tel, err := Init(ctx, cfg, WithHistogramBuckets(exporttest.DefaultBuckets))
	if err != nil {
		return exporttest.Capture{}, err
	}
	s.Run(ctx, exporttest.Providers{
		Traces:  fixedProvider{tr: tel.Tracer},
		Metrics: fixedMeterProvider{m: tel.Meter},
	})
	if err := tel.Shutdown(ctx); err != nil {
		return exporttest.Capture{}, err
	}
	if cfg.SpoolDir == "" {
		return exporttest.Capture{Traces: rcv.Traces(), Metrics: rcv.Metrics()}, errors.Join(rcv.Errs()...)
	}
	var c exporttest.Capture
	codec := exporttest.JSONCodec()
	tfiles, err := listFiles(dir, spool.SignalTraces)
	if err != nil {
		return c, err
	}
	for _, f := range tfiles {
		b, err := os.ReadFile(f)
		if err != nil {
			return c, err
		}
		reqs, err := codec.DecodeTraces(b)
		if err != nil {
			return c, err
		}
		c.Traces = append(c.Traces, reqs...)
	}
	mfiles, err := listFiles(dir, spool.SignalMetrics)
	if err != nil {
		return c, err
	}
	for _, f := range mfiles {
		b, err := os.ReadFile(f)
		if err != nil {
			return c, err
		}
		reqs, err := codec.DecodeMetrics(b)
		if err != nil {
			return c, err
		}
		c.Metrics = append(c.Metrics, reqs...)
	}
	return c, nil
}

// TestContractThroughInit runs the transport-parity contract with Init as the
// only code that picks the transport: the same instrumentation yields the same
// normalized requests over HTTP and over the spool, so Init holds no
// transport-specific code after its two factories. The record-cap scenario
// split is a spool Encoder detail that Init does not expose, so the
// split requirement is dropped here (the exporttest suite covers it).
func TestContractThroughInit(t *testing.T) {
	http := initTransport{name: "init-http", cfg: func(_ string, rcv *exporttest.LoopbackReceiver) Config {
		return Config{Endpoint: rcv.Addr()}
	}}
	spl := initTransport{name: "init-spool", cfg: func(dir string, _ *exporttest.LoopbackReceiver) Config {
		return Config{SpoolDir: dir}
	}}
	for _, s := range exporttest.Scenarios() {
		s.WantSplit, s.SpoolRecordBytes = false, 0
		t.Run(s.Name, func(t *testing.T) {
			for _, m := range exporttest.CompareScenario(func() string { return t.TempDir() }, s, http, spl) {
				t.Error(m)
			}
		})
	}
}
