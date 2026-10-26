// Package container wires infrastructure to consumer-owned usecase interfaces.
// No business or transport package constructs a pool.
package container

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/UnusualNick/template/internal/config"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/observability"
	"github.com/UnusualNick/template/internal/storage/postgres"
	"github.com/UnusualNick/template/internal/transport/httpapi"
	tripcreate "github.com/UnusualNick/template/internal/usecase/trip/create"
	tripfinish "github.com/UnusualNick/template/internal/usecase/trip/finish"
	tripget "github.com/UnusualNick/template/internal/usecase/trip/get"
)

// App owns the server and resources created by New.
type App struct {
	Server            *http.Server
	pool              *pgxpool.Pool
	telemetryShutdown func(context.Context) error
	logger            *slog.Logger
}

// tripActions adapts the three independently tested actions to the generated
// transport's stable three-method contract.
type tripActions struct {
	create *tripcreate.UseCase
	get    *tripget.UseCase
	finish *tripfinish.UseCase
}

func (a tripActions) Create(ctx context.Context, input domain.CreateTrip) (domain.Trip, error) {
	return a.create.Execute(ctx, input)
}

func (a tripActions) Get(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	return a.get.Execute(ctx, id)
}

func (a tripActions) Finish(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	return a.finish.Execute(ctx, id)
}

func logTripEvent(ctx context.Context, logger *slog.Logger, event string) {
	attrs := []any{}
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
			_ = telemetryShutdown(ctx)
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
	poolConfig.ConnConfig.Tracer = observability.NewPGXTracer()

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

	txManager := postgres.NewTxManager(pool)
	repository := postgres.NewTripRepository(txManager, cfg.DatabaseQueryTimeout)
	tripActions := tripActions{
		create: tripcreate.New(repository, txManager, nil, nil, func(ctx context.Context) {
			observability.TripCreated(ctx)
			logTripEvent(ctx, logger, "trip created")
		}),
		get: tripget.New(repository),
		finish: tripfinish.New(repository, txManager, func(ctx context.Context) {
			observability.TripCompleted(ctx)
			logTripEvent(ctx, logger, "trip completed")
		}),
	}
	handler := httpapi.NewHandler(tripActions, pool, cfg.DatabaseQueryTimeout, logger)
	// chi installs these outside the routes, preserving a stable route template.
	// Outer to inner: Recover → Tracing → Logging → Metrics → routes.
	routes := httpapi.NewRouter(handler,
		observability.Recover(logger), observability.Tracing(),
		observability.Logging(logger), observability.HTTPMiddleware(logger),
	)

	return &App{pool: pool, telemetryShutdown: telemetryShutdown, logger: logger, Server: &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           routes,
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}}, nil
}

// Close releases resources after the HTTP server has stopped.
func (a *App) Close() {
	if a == nil {
		return
	}
	if a.pool != nil {
		a.pool.Close()
	}
	if a.telemetryShutdown != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.telemetryShutdown(ctx); err != nil && a.logger != nil {
			a.logger.Warn("telemetry shutdown failed", "error.type", "exporter")
		}
	}
}
