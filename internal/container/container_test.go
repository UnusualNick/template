package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/internal/domain"
	positionworker "github.com/UnusualNick/template/internal/worker/positions"
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

func TestCloseDrainsWorkerBeforeFlushingTelemetry(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	ticks := make(chan time.Time, 1)
	trip := domain.Trip{ID: uuid.New(), DriverID: uuid.New(), Status: domain.TripStatusActive}
	contexts := make(chan context.Context, 1)
	source := NewMockStaleTrips(gomock.NewController(t))
	source.EXPECT().ListStale(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ time.Time) ([]domain.Trip, error) {
		contexts <- ctx
		return []domain.Trip{trip}, nil
	})
	entered := make(chan struct{})
	released := make(chan struct{})
	requester := NewMockPositionRequester(gomock.NewController(t))
	requester.EXPECT().RequestPosition(gomock.Any(), trip.ID, trip.DriverID).DoAndReturn(func(ctx context.Context, _, _ uuid.UUID) error {
		close(entered)
		select {
		case <-released:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	worker, err := positionworker.New(source, requester, positionworker.Config{
		Interval: time.Minute, StaleAfter: time.Minute, RequestTimeout: 5 * time.Second,
		Workers: 1, QueueCapacity: 1, Tick: ticks, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	scanCtx := <-contexts
	<-entered
	telemetry := make(chan struct{})
	app := &App{worker: worker, logger: logger, telemetryShutdown: func(context.Context) error {
		close(telemetry)
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- app.Close(ctx) }()
	select { // Stop has canceled the scanner, but the Push call is still active.
	case <-scanCtx.Done():
	case <-ctx.Done():
		t.Fatal("scanner did not stop within the shutdown deadline")
	}
	select {
	case <-telemetry:
		t.Fatal("telemetry closed before worker drained")
	default:
	}
	close(released)
	if err := <-closed; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-telemetry:
	default:
		t.Fatal("telemetry was not flushed after worker shutdown")
	}
	if err := app.Close(ctx); err != nil {
		t.Fatalf("repeated Close() error = %v", err)
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
