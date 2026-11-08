// Package observability provides optional OTLP telemetry and HTTP instrumentation.
// Integrate with a chi router via router.Use(Recover(logger), Tracing(),
// Logging(logger), HTTPMiddleware(logger)). Record trip counters at successful
// create/finish boundaries. No endpoint is added.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const meterName = "github.com/UnusualNick/template/internal/observability"

// ServiceName is the service name shared by resource attributes and logs.
func ServiceName() string {
	if name := os.Getenv("OTEL_SERVICE_NAME"); name != "" {
		return name
	}
	return "trip-service"
}

// Init installs OTLP HTTP/protobuf (default) or gRPC tracing and metrics.
// Exporting happens in the background; an unavailable collector cannot prevent
// startup. OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG control sampling.
// Standard OTEL_EXPORTER_OTLP_* variables configure exporters. Call shutdown
// with a bounded context on exit.
func Init(ctx context.Context, logger *slog.Logger) func(context.Context) error {
	if logger == nil {
		logger = slog.Default()
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Warn("telemetry error", "error", err)
	}))
	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", ServiceName()),
			attribute.String("service.version", serviceVersion()),
			attribute.String("service.namespace", "tripgo"),
			attribute.String("deployment.environment", "local"),
		),
		resource.WithFromEnv(), resource.WithTelemetrySDK(),
	)
	if err != nil {
		logger.Warn("telemetry resource unavailable", "error.type", "resource")
		res = resource.Default()
	}
	sampler := traceSampler(logger)
	protocol := os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	var shutdowns []func(context.Context) error
	if exporter, exportErr := newTraceExporter(ctx, protocol); exportErr == nil {
		provider := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSampler(sampler), sdktrace.WithBatcher(exporter))
		otel.SetTracerProvider(provider)
		shutdowns = append(shutdowns, provider.Shutdown)
	} else {
		logger.Warn("trace exporter unavailable", "error.type", "exporter")
	}
	if exporter, exportErr := newMetricExporter(ctx, protocol); exportErr == nil {
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(15*time.Second))))
		otel.SetMeterProvider(provider)
		shutdowns = append(shutdowns, provider.Shutdown)
	} else {
		logger.Warn("metric exporter unavailable", "error.type", "exporter")
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return func(shutdownCtx context.Context) error {
		// Flush both providers concurrently: a slow metric exporter must not
		// consume the entire shutdown budget before traces get a chance to flush.
		results := make(chan error, len(shutdowns))
		for _, shutdown := range shutdowns {
			go func() { results <- shutdown(shutdownCtx) }()
		}
		var errs []error
		for range shutdowns {
			if err := <-results; err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

func newTraceExporter(ctx context.Context, protocol string) (sdktrace.SpanExporter, error) {
	switch protocol {
	case "", "http/protobuf":
		return otlptracehttp.New(ctx)
	case "grpc":
		return otlptracegrpc.New(ctx)
	default:
		return nil, errors.New("unsupported OTLP protocol")
	}
}

func newMetricExporter(ctx context.Context, protocol string) (sdkmetric.Exporter, error) {
	switch protocol {
	case "", "http/protobuf":
		return otlpmetrichttp.New(ctx)
	case "grpc":
		return otlpmetricgrpc.New(ctx)
	default:
		return nil, errors.New("unsupported OTLP protocol")
	}
}

func serviceVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && len(setting.Value) >= 12 {
			return setting.Value[:12]
		}
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func traceSampler(logger *slog.Logger) sdktrace.Sampler {
	ratio := 1.0
	if raw := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); raw != "" {
		if n, err := strconv.ParseFloat(raw, 64); err == nil && n >= 0 && n <= 1 {
			ratio = n
		} else {
			logger.Warn("invalid trace sampling ratio")
		}
	}
	switch os.Getenv("OTEL_TRACES_SAMPLER") {
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(ratio)
	case "parentbased_traceidratio":
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	default:
		logger.Warn("unsupported trace sampler; using parentbased_always_on")
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
}

// TripCounters owns instruments created once at startup, without per-trip labels.
type TripCounters struct {
	created   metric.Int64Counter
	completed metric.Int64Counter
}

// NewTripCounters reports registration errors rather than silently losing metrics.
func NewTripCounters() (TripCounters, error) {
	meter := otel.Meter(meterName)
	created, err := meter.Int64Counter("tripgo.trips.created")
	if err != nil {
		return TripCounters{}, fmt.Errorf("create trips-created counter: %w", err)
	}
	completed, err := meter.Int64Counter("tripgo.trips.completed")
	if err != nil {
		return TripCounters{}, fmt.Errorf("create trips-completed counter: %w", err)
	}
	return TripCounters{created: created, completed: completed}, nil
}

// Created records one successful creation. Call only after a committed transaction.
func (c TripCounters) Created(ctx context.Context) { c.created.Add(ctx, 1) }

// Completed records one successful completion. Call only after a committed transaction.
func (c TripCounters) Completed(ctx context.Context) { c.completed.Add(ctx, 1) }

func dbErrorType(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "database_error"
}
