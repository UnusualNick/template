package observability

import (
	"context"
	"testing"
	"time"
)

func TestOTLPProtocolSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protocol  string
		supported bool
	}{
		{"default HTTP", "", true},
		{"HTTP protobuf", "http/protobuf", true},
		{"gRPC", "grpc", true},
		{"unsupported", "http/json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			traces, traceErr := newTraceExporter(ctx, tc.protocol)
			metrics, metricErr := newMetricExporter(ctx, tc.protocol)
			if tc.supported {
				if traceErr != nil || metricErr != nil || traces == nil || metrics == nil {
					t.Fatalf("protocol %q: trace=%v, metric=%v", tc.protocol, traceErr, metricErr)
				}
				if err := traces.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
				if err := metrics.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			} else if traceErr == nil || metricErr == nil {
				t.Fatalf("unsupported protocol %q must not be silently ignored", tc.protocol)
			}
		})
	}
}
