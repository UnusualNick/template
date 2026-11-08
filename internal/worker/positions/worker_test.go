package positions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/goleak"

	"github.com/UnusualNick/template/internal/domain"
)

type staleFunc func(context.Context, time.Time) ([]domain.Trip, error)

func (f staleFunc) ListStale(ctx context.Context, before time.Time) ([]domain.Trip, error) {
	return f(ctx, before)
}

type requestFunc func(context.Context, uuid.UUID, uuid.UUID) error

func (f requestFunc) RequestPosition(ctx context.Context, tripID, driverID uuid.UUID) error {
	return f(ctx, tripID, driverID)
}

func trip() domain.Trip { return domain.Trip{ID: uuid.New(), DriverID: uuid.New()} }

func testConfig(ticks <-chan time.Time) Config {
	return Config{
		Interval:       time.Minute,
		StaleAfter:     2 * time.Minute,
		RequestTimeout: time.Minute,
		Workers:        1,
		QueueCapacity:  1,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tick:           ticks,
		Now:            func() time.Time { return time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC) },
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worker event")
		var zero T
		return zero
	}
}

func stopWorker(t *testing.T, w *Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestScannerQueueDedupDropAndDrain(t *testing.T) {
	defer goleak.VerifyNone(t)
	ticks := make(chan time.Time)
	a, b, c := trip(), trip(), trip()
	entered := make(chan uuid.UUID, 4)
	releaseA := make(chan struct{})
	scanned := make(chan time.Time, 3)
	var calls atomic.Int32
	var mu sync.Mutex
	checks := map[string]int{}
	requests := map[string]int{}
	observed := make(chan struct{}, 7)
	dropped := make(chan struct{}, 2)
	cfg := testConfig(ticks)
	cfg.Hooks = Hooks{
		OnStaleCheck: func(result string) { mu.Lock(); checks[result]++; mu.Unlock(); observed <- struct{}{} },
		OnRequest: func(result string, _ time.Duration) {
			mu.Lock()
			requests[result]++
			mu.Unlock()
			if result == ResultDropped {
				dropped <- struct{}{}
			}
		},
	}
	w, err := New(staleFunc(func(_ context.Context, before time.Time) ([]domain.Trip, error) {
		scanned <- before
		if calls.Add(1) == 1 {
			return []domain.Trip{a}, nil
		}
		return []domain.Trip{b, b, c}, nil
	}), requestFunc(func(ctx context.Context, tripID, driverID uuid.UUID) error {
		entered <- tripID
		switch tripID {
		case a.ID:
			<-releaseA
		case b.ID:
			if driverID != b.DriverID {
				return errors.New("wrong driver")
			}
		default:
			return errors.New("unexpected request")
		}
		return nil
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Time{}
	if got := receive(t, scanned); !got.Equal(cfg.Now().Add(-cfg.StaleAfter)) {
		t.Fatalf("cutoff %v", got)
	}
	if got := receive(t, entered); got != a.ID {
		t.Fatalf("first request %v", got)
	}
	ticks <- time.Time{}
	receive(t, scanned)
	// A third scan round-trip proves the second scan's enqueue callbacks ran.
	ticks <- time.Time{}
	receive(t, scanned)
	for range 7 {
		receive(t, observed)
	}
	for range 2 {
		receive(t, dropped)
	}
	mu.Lock()
	if checks[ResultQueued] != 2 || checks[ResultDuplicate] != 3 || checks[ResultDropped] != 2 || requests[ResultDropped] != 2 {
		t.Errorf("checks=%v requests=%v", checks, requests)
	}
	mu.Unlock()
	close(releaseA)
	if got := receive(t, entered); got != b.ID {
		t.Errorf("second request %v", got)
	}
	stopWorker(t, w)
	mu.Lock()
	if requests[ResultSuccess] != 2 {
		t.Errorf("requests=%v", requests)
	}
	mu.Unlock()
}

func TestPositionArrivalCancelsActiveAndOldTokenCannotDeleteNew(t *testing.T) {
	defer goleak.VerifyNone(t)
	ticks := make(chan time.Time)
	b := trip()
	started := make(chan context.Context, 2)
	allowFirst := make(chan struct{})
	scanned := make(chan struct{}, 2)
	queued := make(chan struct{}, 2)
	var count atomic.Int32
	cfg := testConfig(ticks)
	cfg.QueueCapacity = 2
	cfg.Hooks.OnStaleCheck = func(result string) {
		if result == ResultQueued {
			queued <- struct{}{}
		}
	}
	w, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) {
		scanned <- struct{}{}
		return []domain.Trip{b}, nil
	}), requestFunc(func(ctx context.Context, _, _ uuid.UUID) error {
		started <- ctx
		if count.Add(1) == 1 {
			<-allowFirst // models a gateway returning after cancellation
		}
		return nil
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Time{}
	receive(t, scanned)
	receive(t, queued)
	firstCtx := receive(t, started)
	w.PositionArrived(b.ID)
	receive(t, firstCtx.Done())
	ticks <- time.Time{}
	receive(t, scanned)
	receive(t, queued)
	// The old request must not remove the newly queued entry on completion.
	close(allowFirst)
	receive(t, started)
	stopWorker(t, w)
	if count.Load() != 2 {
		t.Fatalf("requests=%d, want 2", count.Load())
	}
}

func TestPositionArrivalSkipsQueuedEntry(t *testing.T) {
	defer goleak.VerifyNone(t)
	ticks := make(chan time.Time)
	a, b := trip(), trip()
	started := make(chan uuid.UUID, 2)
	release := make(chan struct{})
	queued := make(chan struct{}, 2)
	var scans atomic.Int32
	cfg := testConfig(ticks)
	cfg.Hooks.OnStaleCheck = func(result string) {
		if result == ResultQueued {
			queued <- struct{}{}
		}
	}
	w, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) {
		if scans.Add(1) == 1 {
			return []domain.Trip{a}, nil
		}
		return []domain.Trip{b}, nil
	}), requestFunc(func(_ context.Context, tripID, _ uuid.UUID) error {
		started <- tripID
		if tripID == a.ID {
			<-release
		}
		return nil
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Time{}
	receive(t, queued) // A enqueued
	if got := receive(t, started); got != a.ID {
		t.Fatalf("first request %v", got)
	}
	ticks <- time.Time{}
	receive(t, queued) // B enqueued behind the blocked request
	w.PositionArrived(b.ID)
	close(release)
	stopWorker(t, w)
	select {
	case got := <-started:
		t.Fatalf("canceled queued trip requested: %v", got)
	default:
	}
}

func TestStopDeadlineCancelsRequestsAndNoGoroutinesRemain(t *testing.T) {
	defer goleak.VerifyNone(t)
	ticks := make(chan time.Time)
	started := make(chan context.Context, 1)
	scanned := make(chan struct{}, 1)
	cfg := testConfig(ticks)
	w, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) {
		scanned <- struct{}{}
		return []domain.Trip{trip()}, nil
	}), requestFunc(func(ctx context.Context, _, _ uuid.UUID) error {
		started <- ctx
		<-ctx.Done()
		return ctx.Err()
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Time{}
	receive(t, scanned)
	requestCtx := receive(t, started)
	deadline, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Stop(deadline); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop=%v", err)
	}
	receive(t, requestCtx.Done())
	receive(t, w.done)
	stopWorker(t, w)
}

func TestFixedWorkerCountAndRequestTimeout(t *testing.T) {
	defer goleak.VerifyNone(t)
	ticks := make(chan time.Time)
	trips := make([]domain.Trip, 8)
	for i := range trips {
		trips[i] = trip()
	}
	queued := make(chan struct{}, len(trips))
	entered := make(chan struct{}, len(trips))
	release := make(chan struct{})
	var running, peak, requests atomic.Int32
	cfg := testConfig(ticks)
	cfg.Workers, cfg.QueueCapacity = 3, len(trips)
	cfg.Hooks.OnStaleCheck = func(result string) {
		if result == ResultQueued {
			queued <- struct{}{}
		}
	}
	cfg.Hooks.OnWorkersActive = func(n int) {
		if n > cfg.Workers {
			t.Errorf("active worker gauge exceeds configured count: %d", n)
		}
	}
	w, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) {
		return trips, nil
	}), requestFunc(func(ctx context.Context, _, _ uuid.UUID) error {
		n := running.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		requests.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			running.Add(-1)
			return ctx.Err()
		}
		running.Add(-1)
		return nil
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Time{}
	for range len(trips) {
		receive(t, queued)
	}
	for range cfg.Workers {
		receive(t, entered)
	}
	if got := requests.Load(); int(got) != cfg.Workers {
		t.Fatalf("running requests = %d", got)
	}
	close(release)
	stopWorker(t, w)
	if int(peak.Load()) != cfg.Workers || int(requests.Load()) != len(trips) {
		t.Errorf("peak=%d calls=%d, want peak=%d calls=%d", peak.Load(), requests.Load(), cfg.Workers, len(trips))
	}

	// A task timeout belongs to the request, not to the scanner or shutdown.
	timeoutTicks := make(chan time.Time)
	cfg = testConfig(timeoutTicks)
	cfg.RequestTimeout = 20 * time.Millisecond
	completed := make(chan string, 1)
	cfg.Hooks.OnRequest = func(result string, _ time.Duration) { completed <- result }
	w, err = New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) {
		return []domain.Trip{trip()}, nil
	}), requestFunc(func(ctx context.Context, _, _ uuid.UUID) error {
		<-ctx.Done()
		return ctx.Err()
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	timeoutTicks <- time.Time{}
	if result := receive(t, completed); result != ResultError {
		t.Fatalf("timeout result = %s", result)
	}
	stopWorker(t, w)
}

func TestScannerLinkToAsyncRequestAndRedactedErrors(t *testing.T) {
	defer goleak.VerifyNone(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer func() {
		otel.SetTracerProvider(original)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()

	var logs bytes.Buffer
	ticks := make(chan time.Time)
	scanErrors := make(chan struct{}, 1)
	requested := make(chan struct{}, 1)
	cfg := testConfig(ticks)
	cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	cfg.Hooks.OnScanError = func(error) { scanErrors <- struct{}{} }
	cfg.Hooks.OnRequest = func(string, time.Duration) { requested <- struct{}{} }
	var scanCount atomic.Int32
	item := trip()
	w, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) {
		if scanCount.Add(1) == 1 {
			return nil, errors.New("postgres://user:secret@host/database")
		}
		return []domain.Trip{item}, nil
	}), requestFunc(func(ctx context.Context, _, _ uuid.UUID) error {
		_, pushSpan := otel.Tracer("test-gateway").Start(ctx, "gateway.push")
		pushSpan.End()
		return errors.New("secret outbound payload")
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Time{}
	receive(t, scanErrors)
	ticks <- time.Time{}
	receive(t, requested)
	stopWorker(t, w)

	got := map[string]tracetest.SpanStub{}
	for _, span := range exporter.GetSpans() {
		if span.Name == "positions.scan" && span.Status.Description == "database_error" {
			got["scan.failed"] = span
		} else {
			got[span.Name] = span
		}
	}
	if len(got) != 4 {
		t.Fatalf("spans=%v, want failed scan, scan, request, gateway push", got)
	}
	scan := got["positions.scan"]
	worker := got["positions.request"]
	push := got["gateway.push"]
	if len(worker.Links) != 1 || !worker.Links[0].SpanContext.Equal(scan.SpanContext) {
		t.Errorf("task link=%v, want scan context=%v", worker.Links, scan.SpanContext)
	}
	if worker.Parent.IsValid() || worker.SpanContext.TraceID() == scan.SpanContext.TraceID() {
		t.Error("worker must be an independent trace linked to scan, not its child")
	}
	if push.Parent.SpanID() != worker.SpanContext.SpanID() || push.SpanContext.TraceID() != worker.SpanContext.TraceID() {
		t.Error("gateway push must be a child of the worker task span")
	}
	if strings.Contains(logs.String(), "secret") || !strings.Contains(logs.String(), "error.type=database_error") || !strings.Contains(logs.String(), "error.type=push_error") {
		t.Errorf("logs must contain bounded error types but no DSN or payload: %s", logs.String())
	}
	for _, span := range got {
		if strings.Contains(span.Status.Description, "secret") {
			t.Errorf("raw error leaked into span %s", span.Name)
		}
		for _, attr := range span.Attributes {
			if attr.Key == "trip.id" || strings.Contains(attr.Value.Emit(), item.ID.String()) {
				t.Errorf("trip UUID leaked into span %s attribute %s", span.Name, attr.Key)
			}
		}
	}
}

func TestStartValidationAndSingleScanner(t *testing.T) {
	defer goleak.VerifyNone(t)
	cfg := testConfig(make(chan time.Time))
	if _, err := New(nil, requestFunc(func(context.Context, uuid.UUID, uuid.UUID) error { return nil }), cfg); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) { return nil, nil }), nil, cfg); err == nil {
		t.Fatal("missing requester accepted")
	}
	cfg.QueueCapacity = 0
	if _, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) { return nil, nil }), requestFunc(func(context.Context, uuid.UUID, uuid.UUID) error { return nil }), cfg); err == nil {
		t.Fatal("zero capacity accepted")
	}
	cfg.QueueCapacity = 1
	ticks := make(chan time.Time, 2)
	cfg.Tick = ticks
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active, max atomic.Int32
	w, err := New(staleFunc(func(context.Context, time.Time) ([]domain.Trip, error) {
		n := active.Add(1)
		if n > max.Load() {
			max.Store(n)
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		active.Add(-1)
		return nil, errors.New("scan failure")
	}), requestFunc(func(context.Context, uuid.UUID, uuid.UUID) error { return nil }), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Stop(context.Background()); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Stop before Start=%v", err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start=%v", err)
	}
	ticks <- time.Time{}
	receive(t, entered)
	ticks <- time.Time{}
	close(release)
	stopWorker(t, w)
	if max.Load() != 1 {
		t.Fatalf("concurrent scanners: %d", max.Load())
	}
}
