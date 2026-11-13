// Package otelsetup wires OpenTelemetry tracing and metrics for short-lived
// CLI processes (pn, pg-nix-log-wrapped) that export either to a local
// OTLP/HTTP collector (Config.Endpoint) or to files in a spool directory that a
// collector ingests (Config.SpoolDir).
//
// Design notes:
//   - Init is a Null Object factory: with an empty Config.Endpoint and an empty
//     Config.SpoolDir it returns a working Telemetry backed by no-op providers
//     and touches neither the network nor the file system.
//   - The transport is chosen by two small factories (newSpanExporter and
//     newMetricExporter); everything after them (resource, batcher, periodic
//     reader, histogram View, delta temporality, timeouts, ForceFlush,
//     Shutdown) is one shared path. Switching transport is a Config change and
//     touches no call site; callers only use Telemetry.Tracer and Meter.
//   - Spool mode (package spool) writes OTLP-JSON files. A write failure never
//     blocks or prints: the exporter turns into a no-op and the error reaches
//     the caller through WithOnError and the pn.spool.write_errors counter. Init
//     also runs the throttled retention sweep of the spool directory and records
//     pn.spool.swept_files once the meter provider exists.
//   - Metrics use DELTA temporality (selected on the OTLP exporter, not via a
//     View) so each process exports only its own increments; a collector-side
//     deltatocumulative processor accumulates across processes.
//   - Histograms get explicit second-based bucket boundaries via a View (the
//     SDK defaults are tuned for milliseconds). A histogram whose catalog entry
//     (Metrics) declares Buckets gets those instead.
//   - HTTP mode uses the OTLP/HTTP exporters (otlptracehttp, otlpmetrichttp).
//   - Export retry (HTTP mode only) is opt-in via WithRetry. The OTLP exporters' default retry
//     waits 5 s before the first retry, which a short flush budget can never
//     afford, so a single transient collector refusal (HTTP 503) would drop the
//     whole batch; WithRetry sets a short initial interval and a bounded
//     elapsed time that fits the caller's flush budget.
//   - ForceFlush and Shutdown are bounded by a timeout so a down collector can
//     never hang the host process.
//   - No resource attribute that is unique per process (e.g. service.instance.id)
//     is set: it would turn each run into a new Prometheus series.
//   - Init does not install global providers; callers use the returned Tracer
//     and Meter.
package otelsetup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/x/otelsetup/spool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	// DefaultSpanQueueSize is the span batch processor queue size used when
	// WithSpanQueueSize is not given.
	DefaultSpanQueueSize = 2048
	// DefaultTimeout bounds ForceFlush and Shutdown when WithTimeout is not given.
	DefaultTimeout = 5 * time.Second
	// DefaultMetricInterval is the periodic reader interval. Short-lived
	// processes rely on explicit ForceFlush/Shutdown, so this is deliberately
	// long.
	DefaultMetricInterval = 60 * time.Second
)

// DefaultHistogramBuckets are explicit histogram boundaries in seconds,
// covering sub-second commands through multi-hour nix builds.
var DefaultHistogramBuckets = []float64{
	0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200,
}

// Config identifies the service and where to export. An empty Endpoint and an
// empty SpoolDir disable telemetry entirely; setting both is an error.
type Config struct {
	// Endpoint of the OTLP/HTTP collector: "host:port" or a base URL such as
	// "http://127.0.0.1:4318". Empty means no HTTP export.
	Endpoint string
	// SpoolDir selects the file spool transport: the absolute directory that
	// holds the traces and metrics spool directories. A relative path is an
	// error. Empty means no spool export.
	SpoolDir string
	// SpoolRole selects the spool's directory and file modes, expected owner
	// and the spool counter attribute. The zero value is spool.RoleUser; the
	// wrapper's root spool uses spool.RoleRoot. Ignored without SpoolDir.
	SpoolRole spool.Role
	// ServiceName is the service.name resource attribute.
	ServiceName string
	// Version is the service.version resource attribute.
	Version string
}

// Telemetry is the result of Init.
type Telemetry struct {
	Tracer trace.Tracer
	Meter  metric.Meter

	forceFlush func(context.Context) error
	shutdown   func(context.Context) error
	timeout    time.Duration
}

// Enabled reports whether telemetry is exporting (false for the no-op).
func (t *Telemetry) Enabled() bool { return t.shutdown != nil }

// ForceFlush exports pending spans and metrics, bounded by the configured
// timeout (also bounded by ctx).
func (t *Telemetry) ForceFlush(ctx context.Context) error {
	if t.forceFlush == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.forceFlush(ctx)
}

// Shutdown flushes and releases resources, bounded by the configured timeout
// (also bounded by ctx). It is safe to call on the no-op.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t.shutdown == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	return t.shutdown(ctx)
}

type options struct {
	spanQueueSize  int
	timeout        time.Duration
	buckets        []float64
	metricInterval time.Duration
	retry          bool
	retryInitial   time.Duration
	retryMaxElapse time.Duration
	onError        func(error)
}

// Option customizes Init.
type Option func(*options)

// WithSpanQueueSize sets the span batch processor queue size.
func WithSpanQueueSize(n int) Option { return func(o *options) { o.spanQueueSize = n } }

// WithTimeout sets the bound applied to ForceFlush and Shutdown.
func WithTimeout(d time.Duration) Option { return func(o *options) { o.timeout = d } }

// WithHistogramBuckets overrides the explicit histogram boundaries (seconds).
func WithHistogramBuckets(b []float64) Option { return func(o *options) { o.buckets = b } }

// WithMetricInterval overrides the periodic reader interval.
func WithMetricInterval(d time.Duration) Option { return func(o *options) { o.metricInterval = d } }

// WithRetry makes both exporters retry a transient failure (HTTP 429, 502,
// 503, 504, or a network error) with exponential backoff: the first wait is
// about initial and the total time spent on one request, retries included, is
// at most maxElapsed. maxElapsed is clamped to the WithTimeout bound, because
// ForceFlush and Shutdown give up at that bound anyway; pass a value below the
// caller's flush budget so a retry can still complete inside it. Non-positive
// arguments leave the exporters' defaults (first retry after 5 s) untouched.
func WithRetry(initial, maxElapsed time.Duration) Option {
	return func(o *options) {
		if initial <= 0 || maxElapsed <= 0 {
			return
		}
		o.retry, o.retryInitial, o.retryMaxElapse = true, initial, maxElapsed
	}
}

// WithOnError registers fn to be called for every spool write error (a
// transient one, and the persistent one that turns the exporter into a no-op).
// Spool write failures are otherwise silent. fn MUST be cheap, MAY be called
// from an exporter goroutine, and is ignored in HTTP mode.
func WithOnError(fn func(error)) Option { return func(o *options) { o.onError = fn } }

// Init builds tracing and metrics for cfg. With an empty cfg.Endpoint and an
// empty cfg.SpoolDir it returns a no-op Telemetry and nil error without
// touching the network or the file system. Setting both, or a relative
// SpoolDir, is an error.
func Init(ctx context.Context, cfg Config, opts ...Option) (*Telemetry, error) {
	o := options{
		spanQueueSize:  DefaultSpanQueueSize,
		timeout:        DefaultTimeout,
		buckets:        DefaultHistogramBuckets,
		metricInterval: DefaultMetricInterval,
	}
	for _, f := range opts {
		f(&o)
	}
	if o.timeout <= 0 {
		o.timeout = DefaultTimeout
	}
	if cfg.Endpoint == "" && cfg.SpoolDir == "" {
		return &Telemetry{
			Tracer:  tracenoop.NewTracerProvider().Tracer(cfg.ServiceName),
			Meter:   metricnoop.NewMeterProvider().Meter(cfg.ServiceName),
			timeout: o.timeout,
		}, nil
	}
	if cfg.Endpoint != "" && cfg.SpoolDir != "" {
		return nil, errors.New("otelsetup: Endpoint and SpoolDir are mutually exclusive")
	}
	if cfg.SpoolDir != "" && !filepath.IsAbs(cfg.SpoolDir) {
		return nil, fmt.Errorf("otelsetup: SpoolDir %q must be an absolute path", cfg.SpoolDir)
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName(cfg.ServiceName),
		attribute.String("service.version", cfg.Version),
	))
	if err != nil {
		return nil, fmt.Errorf("otelsetup: resource: %w", err)
	}

	// The write-error counter exists only after the meter provider; the hook
	// reads it through a pointer set below. No export can happen before then.
	var writeErrors atomic.Pointer[metric.Int64Counter]
	spoolAttrs := metric.WithAttributes(attribute.String("spool", cfg.SpoolRole.String()))
	onError := func(err error) {
		if c := writeErrors.Load(); c != nil {
			(*c).Add(context.Background(), 1, spoolAttrs)
		}
		if o.onError != nil {
			o.onError(err)
		}
	}

	traceExp, err := newSpanExporter(ctx, cfg, o, onError)
	if err != nil {
		return nil, err
	}
	metricExp, err := newMetricExporter(ctx, cfg, o, onError)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(
			traceExp,
			sdktrace.WithMaxQueueSize(o.spanQueueSize),
			sdktrace.WithExportTimeout(o.timeout),
		),
	)
	reader := sdkmetric.NewPeriodicReader(
		metricExp,
		sdkmetric.WithInterval(o.metricInterval),
		sdkmetric.WithTimeout(o.timeout),
	)
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
		sdkmetric.WithView(histogramBucketsView(o.buckets)),
	)

	meter := mp.Meter(cfg.ServiceName)
	if cfg.SpoolDir != "" {
		recordSpool(meter, cfg, &writeErrors)
	}

	return &Telemetry{
		Tracer:  tp.Tracer(cfg.ServiceName),
		Meter:   meter,
		timeout: o.timeout,
		forceFlush: func(ctx context.Context) error {
			return joinConcurrently(
				func() error { return tp.ForceFlush(ctx) },
				func() error { return mp.ForceFlush(ctx) },
			)
		},
		shutdown: func(ctx context.Context) error {
			return joinConcurrently(
				func() error { return tp.Shutdown(ctx) },
				func() error { return mp.Shutdown(ctx) },
			)
		},
	}, nil
}

// newSpanExporter is the span half of the transport choice: OTLP/HTTP for
// cfg.Endpoint, the file spool for cfg.SpoolDir.
func newSpanExporter(ctx context.Context, cfg Config, o options, onError func(error)) (sdktrace.SpanExporter, error) {
	if cfg.SpoolDir != "" {
		exp, err := spool.NewTraceExporter(ctx, spoolConfig(cfg, onError))
		if err != nil {
			return nil, fmt.Errorf("otelsetup: trace exporter: %w", err)
		}
		return exp, nil
	}
	host, insecure, basePath, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(host),
		otlptracehttp.WithURLPath(basePath + "/v1/traces"),
		otlptracehttp.WithTimeout(o.timeout),
	}
	if o.retry {
		maxElapsed := min(o.retryMaxElapse, o.timeout)
		// MaxInterval caps the pre-randomization backoff; the elapsed bound is
		// what limits the total wait.
		opts = append(opts, otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled: true, InitialInterval: o.retryInitial, MaxInterval: maxElapsed, MaxElapsedTime: maxElapsed,
		}))
	}
	if insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otelsetup: trace exporter: %w", err)
	}
	return exp, nil
}

// newMetricExporter is the metric half of the transport choice. Both
// transports use delta temporality: the HTTP exporter is given deltaSelector,
// and the spool exporter's Temporality already returns delta.
func newMetricExporter(ctx context.Context, cfg Config, o options, onError func(error)) (sdkmetric.Exporter, error) {
	if cfg.SpoolDir != "" {
		return spool.NewMetricExporter(spoolConfig(cfg, onError)), nil
	}
	host, insecure, basePath, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(host),
		otlpmetrichttp.WithURLPath(basePath + "/v1/metrics"),
		otlpmetrichttp.WithTimeout(o.timeout),
		// Delta temporality is a property of the exporter's selector.
		otlpmetrichttp.WithTemporalitySelector(deltaSelector),
	}
	if o.retry {
		maxElapsed := min(o.retryMaxElapse, o.timeout)
		opts = append(opts, otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{
			Enabled: true, InitialInterval: o.retryInitial, MaxInterval: maxElapsed, MaxElapsedTime: maxElapsed,
		}))
	}
	if insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	exp, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otelsetup: metric exporter: %w", err)
	}
	return exp, nil
}

// spoolConfig is the exporter configuration shared by both spool exporters.
func spoolConfig(cfg Config, onError func(error)) spool.Config {
	return spool.Config{
		Sink:    spool.NewFileSink(cfg.SpoolDir, cfg.SpoolRole, nil),
		Service: cfg.ServiceName,
		Hooks:   spool.Hooks{OnError: onError},
	}
}

// recordSpool creates the spool counters on meter, arms the write-error
// counter, and runs the throttled sweep, recording its counts. It runs after
// the meter provider exists so the sweep result is not lost.
func recordSpool(meter metric.Meter, cfg Config, writeErrors *atomic.Pointer[metric.Int64Counter]) {
	if c, err := meter.Int64Counter("pn.spool.write_errors", metric.WithUnit("{error}")); err == nil {
		writeErrors.Store(&c)
	}
	swept, err := meter.Int64Counter("pn.spool.swept_files", metric.WithUnit("{file}"))
	if err != nil {
		return
	}
	res := spool.Sweep(spool.SweepConfig{
		Dir:        cfg.SpoolDir,
		Role:       cfg.SpoolRole,
		Extensions: []string{spool.JSONEncoder{}.Extension()},
	})
	role := attribute.String("spool", cfg.SpoolRole.String())
	for _, r := range []struct {
		reason string
		n      int
	}{{spool.ReasonAge, res.Age}, {spool.ReasonCap, res.Cap}} {
		if r.n > 0 {
			swept.Add(context.Background(), int64(r.n),
				metric.WithAttributes(role, attribute.String("reason", r.reason)))
		}
	}
}

// histogramBucketsView gives every histogram the default boundaries, except a
// histogram whose catalog entry (Metrics) declares its own Buckets, which gets
// those. One view with a per-instrument choice (rather than a default view plus
// per-metric views) because every matching view creates its own stream, which
// would export a histogram twice.
func histogramBucketsView(defaults []float64) sdkmetric.View {
	return func(i sdkmetric.Instrument) (sdkmetric.Stream, bool) {
		if i.Kind != sdkmetric.InstrumentKindHistogram {
			return sdkmetric.Stream{}, false
		}
		b := defaults
		if own, ok := catalogBuckets(i.Name); ok {
			b = own
		}
		return sdkmetric.NewView(
			sdkmetric.Instrument{Kind: sdkmetric.InstrumentKindHistogram, Name: i.Name},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: b}},
		)(i)
	}
}

// joinConcurrently runs the trace and metric provider calls at the same time
// under the caller's one deadline, waits for both, and joins their errors.
// Running them one after the other would let a refused or stalled trace export
// use up the whole budget and leave the metric export an expired context.
func joinConcurrently(traces, metrics func() error) error {
	var traceErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		traceErr = traces()
	}()
	metricErr := metrics()
	<-done
	return errors.Join(traceErr, metricErr)
}

// deltaSelector reports delta temporality for every instrument kind.
func deltaSelector(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.DeltaTemporality
}

// parseEndpoint accepts "host:port" (insecure) or a base URL.
func parseEndpoint(ep string) (host string, insecure bool, basePath string, err error) {
	if !strings.Contains(ep, "://") {
		return ep, true, "", nil
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return "", false, "", fmt.Errorf("otelsetup: invalid endpoint %q", ep)
	}
	switch u.Scheme {
	case "http":
		insecure = true
	case "https":
	default:
		return "", false, "", fmt.Errorf("otelsetup: unsupported endpoint scheme %q", u.Scheme)
	}
	return u.Host, insecure, strings.TrimRight(u.Path, "/"), nil
}
