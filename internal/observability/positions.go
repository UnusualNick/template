package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// PositionMetrics owns the bounded-cardinality instruments used by the LR3
// scanner, worker pool, and committed position-save action.
type PositionMetrics struct {
	saved         metric.Int64Counter
	staleChecks   metric.Int64Counter
	queueSize     metric.Int64Gauge
	activeWorkers metric.Int64Gauge
	requests      metric.Int64Counter
	requestTime   metric.Float64Histogram
}

func NewPositionMetrics() (PositionMetrics, error) {
	meter := otel.Meter(meterName)
	saved, err := meter.Int64Counter("tripgo.positions.saved")
	if err != nil {
		return PositionMetrics{}, fmt.Errorf("create positions-saved counter: %w", err)
	}
	staleChecks, err := meter.Int64Counter("tripgo.stale_checks")
	if err != nil {
		return PositionMetrics{}, fmt.Errorf("create stale-checks counter: %w", err)
	}
	queueSize, err := meter.Int64Gauge("tripgo.position_requests.queue.size")
	if err != nil {
		return PositionMetrics{}, fmt.Errorf("create queue-size gauge: %w", err)
	}
	activeWorkers, err := meter.Int64Gauge("tripgo.position_requests.workers.active")
	if err != nil {
		return PositionMetrics{}, fmt.Errorf("create active-workers gauge: %w", err)
	}
	requests, err := meter.Int64Counter("tripgo.position_requests")
	if err != nil {
		return PositionMetrics{}, fmt.Errorf("create position-requests counter: %w", err)
	}
	requestTime, err := meter.Float64Histogram("tripgo.position_requests.duration", metric.WithUnit("s"))
	if err != nil {
		return PositionMetrics{}, fmt.Errorf("create position-requests duration: %w", err)
	}
	return PositionMetrics{
		saved: saved, staleChecks: staleChecks, queueSize: queueSize,
		activeWorkers: activeWorkers, requests: requests, requestTime: requestTime,
	}, nil
}

func (m PositionMetrics) Saved(ctx context.Context) {
	m.saved.Add(ctx, 1)
}

// Worker hooks have no request context. Use a neutral context and fixed result
// labels; never put a trip ID into a metric attribute.
func (m PositionMetrics) StaleCheck(result string) {
	m.staleChecks.Add(context.Background(), 1, metric.WithAttributes(attribute.String("result", result)))
}

func (m PositionMetrics) QueueDepth(size int) {
	m.queueSize.Record(context.Background(), int64(size))
}

func (m PositionMetrics) WorkersActive(count int) {
	m.activeWorkers.Record(context.Background(), int64(count))
}

func (m PositionMetrics) Request(result string, duration time.Duration) {
	attrs := metric.WithAttributes(attribute.String("result", result))
	m.requests.Add(context.Background(), 1, attrs)
	m.requestTime.Record(context.Background(), duration.Seconds(), attrs)
}
