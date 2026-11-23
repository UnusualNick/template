// Package positions periodically scans for trips needing a driver position and
// submits bounded, deduplicated push requests without spawning a goroutine per trip.
package positions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=worker.go -destination=mocks_test.go -package=positions -mock_names=StaleTrips=MockStaleTrips,PositionRequester=MockPositionRequester

// StaleTrips is owned by this consumer; storage returns only active trips whose
// most recent position (or start time) precedes before.
type StaleTrips interface {
	ListStale(ctx context.Context, before time.Time) ([]domain.Trip, error)
}

// PositionRequester is owned by this consumer; the gateway must honor ctx.
type PositionRequester interface {
	RequestPosition(ctx context.Context, tripID, driverID uuid.UUID) error
}

// Result values are fixed, low-cardinality metric labels.
const (
	ResultQueued    = "queued"
	ResultDuplicate = "duplicate"
	ResultDropped   = "dropped"
	ResultSuccess   = "success"
	ResultError     = "error"
)

// Hooks are optional, synchronous observations. Callbacks must not block or
// call Worker methods (gauge callbacks run under locks for ordered updates).
// Queue depth counts physical queued entries, including canceled entries that
// have not yet been consumed. Workers active counts executing requests.
type Hooks struct {
	OnStaleCheck    func(result string) // queued, duplicate, dropped
	OnQueueDepth    func(int)
	OnWorkersActive func(int)
	OnRequest       func(result string, duration time.Duration) // success, error, dropped
	OnScanError     func(error)
}

// Config controls the scanner, capacity and per-request HTTP timeout. Tick and
// Now are optional clock overrides intended for deterministic tests.
type Config struct {
	Interval       time.Duration
	StaleAfter     time.Duration
	RequestTimeout time.Duration
	Workers        int
	QueueCapacity  int
	Logger         *slog.Logger
	Hooks          Hooks
	Tick           <-chan time.Time
	Now            func() time.Time
}

var (
	ErrNotStarted     = errors.New("positions worker not started")
	ErrAlreadyStarted = errors.New("positions worker already started")
)

const tracerName = "github.com/UnusualNick/template/internal/worker/positions"

type entry struct {
	trip     domain.Trip
	scanSpan trace.SpanContext  // a link, never a parent of the async worker span
	cancel   context.CancelFunc // set while executing; guarded by Worker.mu
}

// Worker owns one scanner and a fixed number of request goroutines. Start may
// only be called once. Stop is safe for concurrent callers and drains queued
// work unless its context expires, at which point it cancels running requests.
type Worker struct {
	source    StaleTrips
	requester PositionRequester
	cfg       Config
	logger    *slog.Logger
	queue     chan *entry

	mu       sync.Mutex
	pending  map[uuid.UUID]*entry
	started  bool
	scanStop context.CancelFunc
	workStop context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
	activeMu sync.Mutex // serialize gauge callbacks from concurrent workers
	active   int
}

func New(source StaleTrips, requester PositionRequester, cfg Config) (*Worker, error) {
	if source == nil || requester == nil {
		return nil, errors.New("positions source and requester are required")
	}
	if cfg.Interval <= 0 || cfg.StaleAfter <= 0 || cfg.RequestTimeout <= 0 || cfg.Workers <= 0 || cfg.QueueCapacity <= 0 {
		return nil, fmt.Errorf("positions interval, stale age, request timeout, worker count and queue capacity must be positive")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		source:    source,
		requester: requester,
		cfg:       cfg,
		logger:    logger,
		queue:     make(chan *entry, cfg.QueueCapacity),
		pending:   make(map[uuid.UUID]*entry),
	}, nil
}

// Start launches one scanner and exactly Config.Workers request goroutines.
// Cancellation of ctx stops scanning and drains the queue; call Stop with a
// shutdown budget to bound the drain and cancel requests if that budget expires.
func (w *Worker) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return ErrAlreadyStarted
	}
	w.started = true
	scanCtx, scanStop := context.WithCancel(ctx)
	workCtx, workStop := context.WithCancel(context.WithoutCancel(ctx)) // drain after scanner cancellation; preserve context values
	w.scanStop, w.workStop = scanStop, workStop
	w.done = make(chan struct{})

	var workers sync.WaitGroup
	workers.Add(w.cfg.Workers)
	for range w.cfg.Workers {
		go func() {
			defer workers.Done()
			w.runWorker(workCtx)
		}()
	}
	go w.scan(scanCtx)
	go func() {
		workers.Wait()
		workStop()
		close(w.done)
	}()
	return nil
}

// Stop stops scanning first, then waits for the queue and active requests to
// drain. If ctx expires, active requests are canceled and ctx.Err is returned;
// request implementations must respect ctx to guarantee prompt goroutine exit.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return ErrNotStarted
	}
	done, scanStop, workStop := w.done, w.scanStop, w.workStop
	w.mu.Unlock()
	w.stopOnce.Do(scanStop)
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		workStop()
		return ctx.Err()
	}
}

// PositionArrived invalidates queued requests for a trip and cancels an active
// request. A later scan may enqueue the same UUID again. Identity-checked queue
// entries ensure that an earlier canceled entry cannot consume that new task.
func (w *Worker) PositionArrived(tripID uuid.UUID) {
	w.mu.Lock()
	item := w.pending[tripID]
	delete(w.pending, tripID)
	var cancel context.CancelFunc
	if item != nil {
		cancel = item.cancel
	}
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (w *Worker) scan(ctx context.Context) {
	defer close(w.queue) // scanner is the sole producer
	var ticks <-chan time.Time
	if w.cfg.Tick != nil {
		ticks = w.cfg.Tick
	} else {
		ticker := time.NewTicker(w.cfg.Interval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		w.scanOnce(ctx)
	}
}

func (w *Worker) scanOnce(ctx context.Context) {
	scanCtx, span := otel.Tracer(tracerName).Start(ctx, "positions.scan")
	defer span.End()
	trips, err := w.source.ListStale(scanCtx, w.cfg.Now().Add(-w.cfg.StaleAfter))
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		category := errorType(err, "database_error")
		span.SetAttributes(attribute.String("error.type", category))
		span.SetStatus(codes.Error, category)
		w.logger.WarnContext(scanCtx, "positions scan failed", "error.type", category)
		if w.cfg.Hooks.OnScanError != nil {
			w.cfg.Hooks.OnScanError(err)
		}
		return
	}
	for _, trip := range trips {
		if ctx.Err() != nil {
			return
		}
		w.enqueue(trip, span.SpanContext())
	}
}

func errorType(err error, fallback string) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return fallback
}

func (w *Worker) enqueue(trip domain.Trip, scanSpan trace.SpanContext) {
	w.mu.Lock()
	if _, exists := w.pending[trip.ID]; exists {
		w.mu.Unlock()
		if w.cfg.Hooks.OnStaleCheck != nil {
			w.cfg.Hooks.OnStaleCheck(ResultDuplicate)
		}
		return
	}
	item := &entry{trip: trip, scanSpan: scanSpan}
	var dropped bool
	select {
	case w.queue <- item:
		w.pending[trip.ID] = item
	default:
		dropped = true
	}
	if !dropped && w.cfg.Hooks.OnQueueDepth != nil {
		w.cfg.Hooks.OnQueueDepth(len(w.queue))
	}
	w.mu.Unlock()
	if dropped {
		w.logger.Warn("positions queue full; dropping request", "trip_id", trip.ID)
		if w.cfg.Hooks.OnStaleCheck != nil {
			w.cfg.Hooks.OnStaleCheck(ResultDropped)
		}
		if w.cfg.Hooks.OnRequest != nil {
			w.cfg.Hooks.OnRequest(ResultDropped, 0)
		}
		return
	}
	if w.cfg.Hooks.OnStaleCheck != nil {
		w.cfg.Hooks.OnStaleCheck(ResultQueued)
	}
}

func (w *Worker) observeActive(delta int) {
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	w.active += delta
	if w.cfg.Hooks.OnWorkersActive != nil {
		w.cfg.Hooks.OnWorkersActive(w.active)
	}
}

func (w *Worker) runWorker(ctx context.Context) {
	for item := range w.queue {
		w.mu.Lock()
		if w.cfg.Hooks.OnQueueDepth != nil {
			w.cfg.Hooks.OnQueueDepth(len(w.queue))
		}
		if w.pending[item.trip.ID] != item {
			w.mu.Unlock()
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, w.cfg.RequestTimeout)
		item.cancel = cancel
		w.mu.Unlock()
		started := time.Now()
		options := []trace.SpanStartOption{trace.WithNewRoot()}
		if item.scanSpan.IsValid() {
			options = append(options, trace.WithLinks(trace.Link{SpanContext: item.scanSpan}))
		}
		taskCtx, taskSpan := otel.Tracer(tracerName).Start(requestCtx, "positions.request", options...)
		w.observeActive(1)
		var err error
		if requestCtx.Err() == nil {
			err = w.requester.RequestPosition(taskCtx, item.trip.ID, item.trip.DriverID)
		} else {
			err = requestCtx.Err()
		}
		elapsed := time.Since(started)
		if err == nil && requestCtx.Err() != nil {
			err = requestCtx.Err()
		}
		if err != nil {
			category := errorType(err, "push_error")
			taskSpan.SetAttributes(attribute.String("error.type", category))
			taskSpan.SetStatus(codes.Error, category)
		}
		taskSpan.End()
		cancel()
		w.observeActive(-1)
		w.mu.Lock()
		if w.pending[item.trip.ID] == item {
			delete(w.pending, item.trip.ID)
		}
		w.mu.Unlock()
		if err != nil {
			w.logger.Warn("positions request failed", "trip_id", item.trip.ID, "error.type", errorType(err, "push_error"))
		}
		if w.cfg.Hooks.OnRequest != nil {
			result := ResultSuccess
			if err != nil {
				result = ResultError
			}
			w.cfg.Hooks.OnRequest(result, elapsed)
		}
	}
}
