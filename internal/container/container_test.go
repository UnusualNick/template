package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestLogTripEventIncludesIDOnlyInLog(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer func() { _ = provider.Shutdown(context.Background()) }()
	ctx, span := provider.Tracer("test").Start(context.Background(), "test")
	id := uuid.New()
	logTripEvent(ctx, logger, "trip created", id)
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["trip_id"] != id.String() || event["trace_id"] != span.SpanContext().TraceID().String() || event["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("missing correlation fields: %v", event)
	}
	span.End()
	if spans := recorder.Ended(); len(spans) != 1 || len(spans[0].Attributes()) != 0 {
		t.Fatalf("trip ID must not become a span attribute: %v", spans)
	}
}

func TestCloseLogsTelemetryShutdownError(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	app := &App{logger: logger, telemetryShutdown: func(context.Context) error { return errors.New("export failed") }}
	app.Close()
	if !strings.Contains(output.String(), `"error":"export failed"`) {
		t.Fatalf("missing shutdown error: %s", output.String())
	}
}
