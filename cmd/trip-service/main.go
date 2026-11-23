package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/UnusualNick/template/internal/config"
	"github.com/UnusualNick/template/internal/container"
	"github.com/UnusualNick/template/internal/observability"
)

func main() {
	slog.SetDefault(newServiceLogger(slog.LevelInfo))
	if err := run(); err != nil {
		slog.Error("trip-service exited", "error", err)
		os.Exit(1)
	}
}

func newServiceLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})).With("service", observability.ServiceName())
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := newServiceLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	app, err := container.New(cfg, logger)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if closeErr := app.Close(cleanupCtx); closeErr != nil {
				logger.Warn("resource shutdown failed", "error", closeErr)
			}
		}
	}()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server started", "address", cfg.HTTPAddr)
		serverErrors <- app.Server.ListenAndServe()
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
	if err := app.Server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful HTTP shutdown failed", "error", err)
		serverCloseErr := app.Server.Close()
		resourceCloseErr := app.Close(shutdownCtx)
		closed = true
		return errors.Join(err, serverCloseErr, resourceCloseErr)
	}
	// The worker stops scanning and drains queued work before the Push client and
	// database are closed, all under the same shutdown deadline as HTTP.
	closeErr := app.Close(shutdownCtx)
	closed = true
	if closeErr != nil {
		return closeErr
	}
	logger.Info("shutdown complete")
	return nil
}
