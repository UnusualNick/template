// Package container wires infrastructure to consumer-owned usecase interfaces.
// No business or transport package constructs a pool.
package container

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/UnusualNick/template/internal/config"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/gateway/push"
	"github.com/UnusualNick/template/internal/observability"
	"github.com/UnusualNick/template/internal/storage/postgres"
	"github.com/UnusualNick/template/internal/transport/httpapi"
	httppositions "github.com/UnusualNick/template/internal/transport/httpapi/positions"
	tripcreate "github.com/UnusualNick/template/internal/usecase/trip/create"
	tripfinish "github.com/UnusualNick/template/internal/usecase/trip/finish"
	tripget "github.com/UnusualNick/template/internal/usecase/trip/get"
	positionlist "github.com/UnusualNick/template/internal/usecase/trip/positions/list"
	positionsave "github.com/UnusualNick/template/internal/usecase/trip/positions/save"
	positionworker "github.com/UnusualNick/template/internal/worker/positions"
)

// App owns the server and resources created by New.
type App struct {
	Server            *http.Server
	pool              *pgxpool.Pool
	worker            *positionworker.Worker
	push              *push.Client
	telemetryShutdown func(context.Context) error
	logger            *slog.Logger
	closeOnce         sync.Once
	closeErr          error
}

// tripActions adapts the three independently tested actions to the generated
// transport's stable three-method contract.
type tripActions struct {
	create *tripcreate.UseCase
	get    *tripget.UseCase
	finish *tripfinish.UseCase
	logger *slog.Logger
}

func (a tripActions) Create(ctx context.Context, input domain.CreateTrip) (domain.Trip, error) {
	trip, err := a.create.Execute(ctx, input)
	if err == nil {
		logTripEvent(ctx, a.logger, "trip created", trip.ID)
	}
	return trip, err
}

func (a tripActions) Get(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	return a.get.Execute(ctx, id)
}

func (a tripActions) Finish(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	trip, err := a.finish.Execute(ctx, id)
	if err == nil {
		logTripEvent(ctx, a.logger, "trip completed", trip.ID)
	}
	return trip, err
}

func logTripEvent(ctx context.Context, logger *slog.Logger, event string, tripID uuid.UUID) {
	attrs := []any{"trip_id", tripID.String()}
	if span := trace.SpanFromContext(ctx).SpanContext(); span.IsValid() {
		attrs = append(attrs, "trace_id", span.TraceID().String(), "span_id", span.SpanID().String())
	}
	logger.InfoContext(ctx, event, attrs...)
}

// New validates the database connection before accepting HTTP requests.
func New(cfg config.Config, logger *slog.Logger) (_ *App, err error) {
	telemetryShutdown := observability.Init(context.Background(), logger)
	defer func() {
		if err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if shutdownErr := telemetryShutdown(ctx); shutdownErr != nil && logger != nil {
				logger.Warn("telemetry shutdown failed", "error", shutdownErr)
			}
		}
	}()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	poolConfig.MaxConns = cfg.DatabaseMaxConns
	poolConfig.MinConns = cfg.DatabaseMinConns
	poolConfig.MaxConnLifetime = cfg.DatabaseMaxConnLifetime
	poolConfig.ConnConfig.ConnectTimeout = cfg.DatabaseConnectTimeout
	poolConfig.ConnConfig.Tracer = observability.NewPGXTracer(logger)

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), cfg.DatabaseConnectTimeout)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startupCtx, poolConfig)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(startupCtx); err != nil {
		pool.Close()
		return nil, err
	}

	counters, err := observability.NewTripCounters()
	if err != nil {
		pool.Close()
		return nil, err
	}
	positionMetrics, err := observability.NewPositionMetrics()
	if err != nil {
		pool.Close()
		return nil, err
	}
	pushClient, err := push.New(cfg.PushHTTPURL, cfg.PushRequestTimeout)
	if err != nil {
		pool.Close()
		return nil, err
	}
	txManager := postgres.NewTxManager(pool)
	repository := postgres.NewTripRepository(txManager, cfg.DatabaseQueryTimeout)
	worker, err := positionworker.New(repository, pushClient, positionworker.Config{
		Interval: cfg.PositionCheckInterval, StaleAfter: cfg.PositionStaleAfter,
		RequestTimeout: cfg.PushRequestTimeout, Workers: cfg.PositionWorkers,
		QueueCapacity: cfg.PositionQueueSize, Logger: logger,
		Hooks: positionworker.Hooks{
			OnStaleCheck: positionMetrics.StaleCheck, OnQueueDepth: positionMetrics.QueueDepth,
			OnWorkersActive: positionMetrics.WorkersActive, OnRequest: positionMetrics.Request,
		},
	})
	if err != nil {
		pushClient.CloseIdleConnections()
		pool.Close()
		return nil, err
	}
	positionMetrics.QueueDepth(0)
	positionMetrics.WorkersActive(0)
	positionSave := positionsave.New(repository, txManager, nil, func(ctx context.Context, tripID uuid.UUID) {
		worker.PositionArrived(tripID)
		positionMetrics.Saved(ctx)
	})
	positionList := positionlist.New(repository)
	tripActions := tripActions{
		create: tripcreate.New(repository, txManager, nil, nil, counters.Created),
		get:    tripget.New(repository),
		finish: tripfinish.New(repository, txManager, counters.Completed),
		logger: logger,
	}
	handler := httpapi.NewHandler(tripActions, pool, cfg.DatabaseQueryTimeout, logger, httpapi.PositionHandlers{
		Create: httppositions.NewCreate(positionSave), List: httppositions.NewList(positionList),
	})
	// chi installs these outside the routes, preserving a stable route template.
	// Outer to inner: Recover → Tracing → Logging → Metrics → routes.
	routes := httpapi.NewRouter(handler,
		observability.Recover(logger, handler.WriteInternalError), observability.Tracing(),
		observability.Logging(logger), observability.HTTPMiddleware(logger),
	)

	if err := worker.Start(context.Background()); err != nil {
		pushClient.CloseIdleConnections()
		pool.Close()
		return nil, err
	}
	logger.Info("positions worker started", "workers", cfg.PositionWorkers, "queue_capacity", cfg.PositionQueueSize)
	return &App{pool: pool, worker: worker, push: pushClient, telemetryShutdown: telemetryShutdown, logger: logger, Server: &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           routes,
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}}, nil
}

// Close runs after the HTTP server has stopped: stop new scans, drain workers
// within ctx, close the Push client and database pool, then flush telemetry.
// When called without a context by older consumers, use a bounded fallback.
func (a *App) Close(shutdownContexts ...context.Context) error {
	if a == nil {
		return nil
	}
	ctx := context.Background()
	if len(shutdownContexts) != 0 && shutdownContexts[0] != nil {
		ctx = shutdownContexts[0]
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	a.closeOnce.Do(func() {
		if a.worker != nil {
			if err := a.worker.Stop(ctx); err != nil {
				a.closeErr = errors.Join(a.closeErr, err)
				if a.logger != nil {
					a.logger.Warn("positions worker shutdown timed out", "error.type", "timeout")
				}
			} else if a.logger != nil {
				a.logger.Info("positions worker stopped")
			}
		}
		if a.push != nil {
			a.push.CloseIdleConnections()
		}
		if a.pool != nil {
			a.pool.Close()
		}
		if a.telemetryShutdown != nil {
			if err := a.telemetryShutdown(ctx); err != nil {
				a.closeErr = errors.Join(a.closeErr, err)
				if a.logger != nil {
					a.logger.Warn("telemetry shutdown failed", "error", err)
				}
			}
		}
	})
	return a.closeErr
}
