// Package otelsetup wires OpenTelemetry tracing and metrics for short-lived
// CLI processes (pn, pg-nix-log-wrapped) that export to a local OTLP/HTTP
// collector.
//
// Design notes:
//   - Init is a Null Object factory: with an empty Config.Endpoint it returns
//     a working Telemetry backed by no-op providers and makes no network calls.
//   - Metrics use DELTA temporality (selected on the OTLP exporter, not via a
//     View) so each process exports only its own increments; a collector-side
//     deltatocumulative processor accumulates across processes.
//   - Histograms get explicit second-based bucket boundaries via a View (the
//     SDK defaults are tuned for milliseconds).
//   - Only the OTLP/HTTP exporters are used (otlptracehttp, otlpmetrichttp).
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
	"strings"
	"time"

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

// Config identifies the service and where to export. An empty Endpoint disables
// telemetry entirely.
type Config struct {
	// Endpoint of the OTLP/HTTP collector: "host:port" or a base URL such as
	// "http://127.0.0.1:4318". Empty means no-op.
	Endpoint string
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

// Init builds tracing and metrics for cfg. With an empty cfg.Endpoint it
// returns a no-op Telemetry and nil error without touching the network.
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
	if cfg.Endpoint == "" {
		return &Telemetry{
			Tracer:  tracenoop.NewTracerProvider().Tracer(cfg.ServiceName),
			Meter:   metricnoop.NewMeterProvider().Meter(cfg.ServiceName),
			timeout: o.timeout,
		}, nil
	}

	host, insecure, basePath, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName(cfg.ServiceName),
		attribute.String("service.version", cfg.Version),
	))
	if err != nil {
		return nil, fmt.Errorf("otelsetup: resource: %w", err)
	}

	traceOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(host),
		otlptracehttp.WithURLPath(basePath + "/v1/traces"),
		otlptracehttp.WithTimeout(o.timeout),
	}
	metricOpts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(host),
		otlpmetrichttp.WithURLPath(basePath + "/v1/metrics"),
		otlpmetrichttp.WithTimeout(o.timeout),
		// Delta temporality is a property of the exporter's selector.
		otlpmetrichttp.WithTemporalitySelector(deltaSelector),
	}
	if insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}

	traceExp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("otelsetup: trace exporter: %w", err)
	}
	metricExp, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		return nil, fmt.Errorf("otelsetup: metric exporter: %w", err)
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
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Kind: sdkmetric.InstrumentKindHistogram},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
				Boundaries: o.buckets,
			}},
		)),
	)

	return &Telemetry{
		Tracer:  tp.Tracer(cfg.ServiceName),
		Meter:   mp.Meter(cfg.ServiceName),
		timeout: o.timeout,
		forceFlush: func(ctx context.Context) error {
			return errors.Join(tp.ForceFlush(ctx), mp.ForceFlush(ctx))
		},
		shutdown: func(ctx context.Context) error {
			return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
		},
	}, nil
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
