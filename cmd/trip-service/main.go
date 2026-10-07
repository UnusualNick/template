package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UnusualNick/template/internal/config"
	"github.com/UnusualNick/template/internal/service"
	"github.com/UnusualNick/template/internal/storage/postgres"
	"github.com/UnusualNick/template/internal/transport/httpapi"
)

func main() {
	if err := run(); err != nil {
		slog.Error("trip-service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).With("service", "trip-service")
	slog.SetDefault(logger)

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	poolConfig.MaxConns = cfg.DatabaseMaxConns
	poolConfig.MinConns = cfg.DatabaseMinConns
	poolConfig.MaxConnLifetime = cfg.DatabaseMaxConnLifetime
	poolConfig.ConnConfig.ConnectTimeout = cfg.DatabaseConnectTimeout

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), cfg.DatabaseConnectTimeout)
	defer cancelStartup()
	pool, err := pgxpool.NewWithConfig(startupCtx, poolConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pool.Ping(startupCtx); err != nil {
		return err
	}

	txManager := postgres.NewTxManager(pool)
	repository := postgres.NewTripRepository(txManager, cfg.DatabaseQueryTimeout)
	tripService := service.NewTripService(repository, txManager)
	handler := httpapi.NewHandler(tripService, pool, cfg.DatabaseQueryTimeout, logger)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(handler),
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server started", "address", cfg.HTTPAddr)
		serverErrors <- server.ListenAndServe()
	}()

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	select {
	case <-signalCtx.Done():
		logger.Info("shutdown signal received")
	case serverErr := <-serverErrors:
		if !errors.Is(serverErr, http.ErrServerClosed) {
			return serverErr
		}
		return nil
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown timed out", "error", err)
		if closeErr := server.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}
	logger.Info("shutdown complete")
	return nil
}
