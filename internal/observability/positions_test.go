package observability

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestPositionMetricsHaveExpectedNamesTypesAndBoundedLabels(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(context.Background()) })
	metrics, err := NewPositionMetrics()
	if err != nil {
		t.Fatal(err)
	}
	metrics.Saved(context.Background())
	metrics.StaleCheck("queued")
	metrics.StaleCheck("dropped")
	metrics.QueueDepth(3)
	metrics.WorkersActive(2)
	metrics.Request("success", 125*time.Millisecond)
	metrics.Request("dropped", 0)

	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, scope := range data.ScopeMetrics {
		for _, entry := range scope.Metrics {
			seen[entry.Name] = true
			switch value := entry.Data.(type) {
			case metricdata.Sum[int64]:
				for _, point := range value.DataPoints {
					if point.Value < 1 || point.Attributes.Len() > 1 {
						t.Errorf("counter %s = %+v", entry.Name, point)
					}
				}
			case metricdata.Gauge[int64]:
				if len(value.DataPoints) != 1 || value.DataPoints[0].Attributes.Len() != 0 {
					t.Errorf("gauge %s = %+v", entry.Name, value.DataPoints)
				}
			case metricdata.Histogram[float64]:
				for _, point := range value.DataPoints {
					if point.Count != 1 || point.Attributes.Len() != 1 {
						t.Errorf("duration histogram = %+v", point)
					}
				}
			default:
				t.Errorf("unexpected metric type for %s: %T", entry.Name, entry.Data)
			}
		}
	}
	for _, name := range []string{
		"tripgo.positions.saved", "tripgo.stale_checks", "tripgo.position_requests.queue.size",
		"tripgo.position_requests.workers.active", "tripgo.position_requests", "tripgo.position_requests.duration",
	} {
		if !seen[name] {
			t.Errorf("missing metric %s", name)
		}
	}
}
