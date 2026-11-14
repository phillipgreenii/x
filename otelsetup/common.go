package otelsetup

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Names of the signals RegisterCommonSignals pre-registers. They are the
// shared kit every daemon is expected to expose, so alert rules can be written
// once (restart detection, shutdown overruns) instead of per app.
const (
	MetricProcessStartTime = "process.start_time"
	MetricBuildInfo        = "build.info"
	MetricShutdownDuration = "shutdown.duration"
	MetricShutdownOverruns = "shutdown.overruns"
)

// CommonSignals holds the instruments RegisterCommonSignals created. The
// process start time and build info are observable gauges that need no calls.
type CommonSignals struct {
	shutdownDuration metric.Float64Histogram
	shutdownOverruns metric.Int64Counter
}

// RegisterCommonSignals registers on meter the signals every daemon should
// carry: process.start_time (unix seconds, so a crash loop shows as a moving
// value), build.info (constant 1 with version and go_version attributes),
// shutdown.duration (seconds) and shutdown.overruns (a counter). Pass the
// Telemetry.Meter, or any meter in a test. start is the process start time.
func RegisterCommonSignals(meter metric.Meter, version string, start time.Time) (*CommonSignals, error) {
	_, err := meter.Float64ObservableGauge(MetricProcessStartTime,
		metric.WithUnit("s"),
		metric.WithDescription("Process start time as unix seconds."),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			o.Observe(float64(start.UnixNano()) / 1e9)
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("otelsetup: %s: %w", MetricProcessStartTime, err)
	}
	info := metric.WithAttributes(
		attribute.String("version", version),
		attribute.String("go_version", runtime.Version()),
	)
	_, err = meter.Int64ObservableGauge(MetricBuildInfo,
		metric.WithDescription("Constant 1 labelled with the build's version."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(1, info)
			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("otelsetup: %s: %w", MetricBuildInfo, err)
	}
	c := &CommonSignals{}
	c.shutdownDuration, err = meter.Float64Histogram(MetricShutdownDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Time from the shutdown signal to the last phase finishing."))
	if err != nil {
		return nil, fmt.Errorf("otelsetup: %s: %w", MetricShutdownDuration, err)
	}
	c.shutdownOverruns, err = meter.Int64Counter(MetricShutdownOverruns,
		metric.WithUnit("{overrun}"),
		metric.WithDescription("Shutdowns that hit the hard deadline."))
	if err != nil {
		return nil, fmt.Errorf("otelsetup: %s: %w", MetricShutdownOverruns, err)
	}
	return c, nil
}

// ObserveShutdown records one finished shutdown of duration d; overran marks a
// shutdown that hit the hard deadline. Safe on a nil receiver.
func (c *CommonSignals) ObserveShutdown(ctx context.Context, d time.Duration, overran bool) {
	if c == nil {
		return
	}
	c.shutdownDuration.Record(ctx, d.Seconds())
	if overran {
		c.shutdownOverruns.Add(ctx, 1)
	}
}
