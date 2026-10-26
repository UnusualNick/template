package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestPGXTracerRecordsAllQueriesWithSafeLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(context.Background()) })

	tracer := NewPGXTracer()
	cases := []struct {
		sql        string
		err        error
		operation  string
		collection string
		errorType  string
	}{
		{"INSERT INTO trips (id) VALUES ($1) /* secret-id */", nil, "INSERT", "trips", ""},
		{"SELECT 1", nil, "SELECT", "unknown", ""},
		{"begin", nil, "BEGIN", "unknown", ""},
		{"commit", nil, "COMMIT", "unknown", ""},
		{"INSERT INTO trip_status_history (reason) VALUES ($1)", errors.New("secret-message"), "INSERT", "trip_status_history", "database_error"},
		{"SELECT * FROM private_secret_table WHERE id = 'secret-id'", context.DeadlineExceeded, "SELECT", "unknown", "timeout"},
		{"UPDATE trips SET status = 'secret-value'", context.Canceled, "UPDATE", "trips", "canceled"},
		{"ALTER TABLE secrets ADD COLUMN hidden TEXT", nil, "OTHER", "unknown", ""},
	}
	for _, tc := range cases {
		ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: tc.sql, Args: []any{"secret-arg"}})
		tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: tc.err})
	}

	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	count := 0
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "db.client.operation.duration" {
				continue
			}
			if m.Unit != "s" {
				t.Errorf("unit = %q, want seconds", m.Unit)
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("unexpected metric data: %T", m.Data)
			}
			for _, point := range histogram.DataPoints {
				if point.Count != 1 || point.Sum < 0 {
					t.Errorf("unexpected histogram point: %+v", point)
				}
				attrs := map[string]string{}
				for _, attr := range point.Attributes.ToSlice() {
					attrs[string(attr.Key)] = attr.Value.AsString()
				}
				if len(attrs) != 2 && len(attrs) != 3 {
					t.Errorf("unexpected attribute count: %v", attrs)
				}
				for _, tc := range cases {
					if attrs["db.operation.name"] == tc.operation && attrs["db.collection.name"] == tc.collection && attrs["error.type"] == tc.errorType {
						seen[fmt.Sprint(tc.operation, tc.collection, tc.errorType)] = true
						break
					}
				}
				for key, value := range attrs {
					if strings.Contains(key, "secret") || strings.Contains(value, "secret") || strings.Contains(value, "private") {
						t.Errorf("sensitive metric attribute: %s=%q", key, value)
					}
				}
				count += int(point.Count)
			}
		}
	}
	if count != len(cases) {
		t.Errorf("recorded query count = %d, want %d", count, len(cases))
	}
	for _, tc := range cases {
		if !seen[fmt.Sprint(tc.operation, tc.collection, tc.errorType)] {
			t.Errorf("missing labels for SQL %q", tc.sql)
		}
	}
}

func TestPGXTracerEmitsSafeQuerySpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })

	tracer := NewPGXTracer()
	// Query spans are started only with a recording parent span.
	ctx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	cases := []struct {
		sql, operation, collection, errorType string
		err                                   error
		status                                codes.Code
	}{
		{"SELECT id FROM trips WHERE id = $1", "SELECT", "trips", "", nil, codes.Unset},
		{"INSERT INTO trip_status_history (trip_id) VALUES ($1)", "INSERT", "trip_status_history", "", nil, codes.Unset},
		{"UPDATE trips SET status = $1 WHERE id = $2", "UPDATE", "trips", "timeout", context.DeadlineExceeded, codes.Error},
		{"ROLLBACK /* private-value */", "ROLLBACK", "unknown", "database_error", errors.New("private-value: SQL id 123456"), codes.Error},
	}
	for _, tc := range cases {
		queryCtx := tracer.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: tc.sql, Args: []any{"private-value"}})
		tracer.TraceQueryEnd(queryCtx, nil, pgx.TraceQueryEndData{Err: tc.err})
	}
	parent.End()

	spans := recorder.Ended()
	if len(spans) != len(cases)+1 {
		t.Fatalf("ended spans = %d, want %d query spans and one parent", len(spans), len(cases))
	}
	for i, tc := range cases {
		span := spans[i]
		if span.Name() != "query "+tc.operation+" "+tc.collection {
			t.Errorf("span %d name = %q", i, span.Name())
		}
		if span.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Errorf("span %d not parented by the active span", i)
		}
		if span.Status().Code != tc.status || span.Status().Description != "" {
			t.Errorf("span %d status = %+v, want %v with no description", i, span.Status(), tc.status)
		}
		if len(span.Events()) != 0 {
			t.Errorf("span %d has unsafe events: %+v", i, span.Events())
		}
		attrs := make(map[string]string)
		for _, attr := range span.Attributes() {
			attrs[string(attr.Key)] = attr.Value.AsString()
			if strings.Contains(string(attr.Key), "private") || strings.Contains(attr.Value.Emit(), "private-value") || strings.Contains(attr.Value.Emit(), "123456") || (strings.Contains(tc.sql, " ") && strings.Contains(attr.Value.Emit(), tc.sql)) {
				t.Errorf("span %d contains query data in attribute %s", i, attr.Key)
			}
		}
		if attrs["db.operation.name"] != tc.operation || attrs["db.collection.name"] != tc.collection {
			t.Errorf("span %d labels = %v, want %s/%s", i, attrs, tc.operation, tc.collection)
		}
		if value, ok := attrs["error.type"]; ok != (tc.err != nil) || (tc.err != nil && value != tc.errorType) {
			t.Errorf("span %d error.type = %q (present %t), want %q", i, value, ok, tc.errorType)
		}
		if (strings.Contains(tc.sql, " ") && strings.Contains(span.Name(), tc.sql)) || strings.Contains(span.Name(), "private") {
			t.Errorf("span %d name contains query data", i)
		}
	}
}

func TestPGXTracerOtherSpansDoNotLeakErrors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })

	tracer := NewPGXTracer().(*dbTracer)
	ctx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	unsafeErr := errors.New("private-value: SELECT id FROM trips WHERE id = 123456")
	prepareCtx := tracer.TracePrepareStart(ctx, nil, pgx.TracePrepareStartData{
		Name: "private-value", SQL: "SELECT id FROM trips WHERE id = 123456",
	})
	tracer.TracePrepareEnd(prepareCtx, nil, pgx.TracePrepareEndData{Err: unsafeErr})
	connectCtx := tracer.TraceConnectStart(ctx, pgx.TraceConnectStartData{})
	tracer.TraceConnectEnd(connectCtx, pgx.TraceConnectEndData{Err: unsafeErr})
	acquireCtx := tracer.TraceAcquireStart(ctx, nil, pgxpool.TraceAcquireStartData{})
	tracer.TraceAcquireEnd(acquireCtx, nil, pgxpool.TraceAcquireEndData{Err: unsafeErr})
	copyCtx := tracer.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{TableName: pgx.Identifier{"private-value"}})
	tracer.TraceCopyFromEnd(copyCtx, nil, pgx.TraceCopyFromEndData{Err: unsafeErr})
	batchCtx := tracer.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{})
	tracer.TraceBatchQuery(batchCtx, nil, pgx.TraceBatchQueryData{SQL: "SELECT * FROM trips WHERE id = 123456", Args: []any{"private-value"}, Err: unsafeErr})
	tracer.TraceBatchEnd(batchCtx, nil, pgx.TraceBatchEndData{Err: unsafeErr})
	parent.End()

	spans := recorder.Ended()
	if len(spans) != 7 {
		t.Fatalf("ended spans = %d, want 6 DB spans and one parent", len(spans))
	}
	for _, span := range spans[:len(spans)-1] {
		if span.Status().Code != codes.Error || span.Status().Description != "" {
			t.Errorf("span %s has unsafe status: %+v", span.Name(), span.Status())
		}
		if len(span.Events()) != 0 {
			t.Errorf("span %s has unsafe events: %+v", span.Name(), span.Events())
		}
		if strings.Contains(span.Name(), "private-value") || strings.Contains(span.Name(), "123456") {
			t.Errorf("unsafe span name: %q", span.Name())
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(string(attr.Key), "private-value") || strings.Contains(attr.Value.Emit(), "private-value") || strings.Contains(attr.Value.Emit(), "123456") {
				t.Errorf("span %s leaks attribute %s=%s", span.Name(), attr.Key, attr.Value.Emit())
			}
		}
	}
}

func TestPGXTracerEndWithoutStart(t *testing.T) {
	tracer := NewPGXTracer()
	if tracer == nil {
		t.Fatal("nil pgx query tracer")
	}
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: errors.New("secret-message")})
}
