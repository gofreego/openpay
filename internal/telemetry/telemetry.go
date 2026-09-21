// Package telemetry wires OpenTelemetry: traces and metrics exported over OTLP.
//
// One SDK covers both signals and stays vendor-neutral — the collector decides
// where data actually goes, so switching backends is a collector config change
// rather than a code change.
package telemetry

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	// Must match the semconv the SDK's resource.Default() uses, or
	// resource.Merge rejects the combination with a schema URL conflict.
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/gofreego/goutils/logger"
)

type Config struct {
	// Enabled turns exporting on. When false the service still runs and the
	// OTel API still works — it just resolves to no-ops, so local development
	// never requires a collector.
	Enabled bool `yaml:"Enabled"`

	ServiceName    string `yaml:"ServiceName"`
	ServiceVersion string `yaml:"ServiceVersion"`
	Environment    string `yaml:"Environment"`

	// Endpoint is the OTLP/gRPC collector address, e.g. localhost:4317.
	Endpoint string `yaml:"Endpoint"`
	// Insecure disables TLS to the collector. True for a local collector,
	// false anywhere the data crosses a network worth protecting.
	Insecure bool `yaml:"Insecure"`

	// SampleRatio is the fraction of traces recorded, 0..1. Payments volume is
	// low (plan.md Q6), so sampling everything is affordable and much more
	// useful when investigating a single customer's failed payment.
	SampleRatio float64 `yaml:"SampleRatio"`

	// MetricInterval is how often metrics are pushed to the collector.
	MetricInterval time.Duration `yaml:"MetricInterval"`
}

func (c *Config) withDefaults() {
	if c.ServiceName == "" {
		c.ServiceName = "openpay"
	}
	if c.Endpoint == "" {
		c.Endpoint = "localhost:4317"
	}
	if c.SampleRatio <= 0 {
		c.SampleRatio = 1
	}
	if c.MetricInterval <= 0 {
		c.MetricInterval = 30 * time.Second
	}
}

// Shutdown flushes pending telemetry. Call it on the way out, or the last spans
// before a crash — the interesting ones — are lost.
type Shutdown func(ctx context.Context) error

// Setup installs the global tracer and meter providers and returns a shutdown.
//
// It never returns an error for a missing collector: telemetry must not be able
// to stop a payments service from starting. An unreachable collector shows up
// as export errors in the log, not as a failed boot.
func Setup(ctx context.Context, cfg *Config) (Shutdown, error) {
	cfg.withDefaults()

	// Propagators are installed either way, so trace context from opengate is
	// carried through even when this service is not exporting.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Route SDK-internal errors into our logger rather than stderr.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Error(context.Background(), "opentelemetry: %v", err)
	}))

	if !cfg.Enabled {
		logger.Info(ctx, "telemetry disabled: tracing and metrics resolve to no-ops")
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		attribute.String("deployment.environment", cfg.Environment),
	))
	if err != nil {
		return nil, err
	}

	// Omitting WithInsecure leaves the exporter on its secure default (TLS with
	// the system root pool), which is what we want anywhere but a local
	// collector.
	traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
	}

	traceExporter, err := otlptracegrpc.New(ctx, traceOpts...)
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(traceExporter),
		// ParentBased keeps a trace whole: once opengate decides to sample a
		// request, every downstream span for it is kept too.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(tracerProvider)

	metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
	}

	metricExporter, err := otlpmetricgrpc.New(ctx, metricOpts...)
	if err != nil {
		return nil, err
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter,
			sdkmetric.WithInterval(cfg.MetricInterval))),
	)
	otel.SetMeterProvider(meterProvider)

	logger.Info(ctx, "telemetry enabled: service=%s env=%s endpoint=%s sample=%.2f",
		cfg.ServiceName, cfg.Environment, cfg.Endpoint, cfg.SampleRatio)

	return func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}, nil
}

// Tracer returns a tracer for a component, so callers do not repeat the
// instrumentation scope name.
func Tracer(name string) trace.Tracer {
	return otel.Tracer("github.com/gofreego/openpay/" + name)
}

// Meter returns a meter for a component.
func Meter(name string) metric.Meter {
	return otel.Meter("github.com/gofreego/openpay/" + name)
}
