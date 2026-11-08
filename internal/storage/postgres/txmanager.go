package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type beginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

type txContextKey struct{}

const rollbackTimeout = 5 * time.Second

type TxManager struct {
	pool      DBTX
	beginner  beginner
	txOptions pgx.TxOptions
	logger    *slog.Logger
}

func NewTxManager(pool interface {
	DBTX
	beginner
},
) *TxManager {
	return &TxManager{
		pool:     pool,
		beginner: pool,
		logger:   slog.Default(),
		txOptions: pgx.TxOptions{
			IsoLevel: pgx.ReadCommitted,
		},
	}
}

func (m *TxManager) Do(ctx context.Context, fn func(context.Context) error) (err error) {
	if _, ok := txFromContext(ctx); ok {
		return fn(ctx)
	}

	tx, err := m.beginner.BeginTx(ctx, m.txOptions)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	txCtx := context.WithValue(ctx, txContextKey{}, tx)
	defer func() {
		if recovered := recover(); recovered != nil {
			if rollbackErr := rollbackTransaction(ctx, tx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
				m.logger.Warn("rollback transaction after panic failed", "error", rollbackErr)
			}
			panic(recovered)
		}
	}()

	if err := fn(txCtx); err != nil {
		if rollbackErr := rollbackTransaction(ctx, tx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("rollback transaction: %w", rollbackErr))
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func rollbackTransaction(ctx context.Context, tx pgx.Tx) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	return tx.Rollback(rollbackCtx)
}

func (m *TxManager) Executor(ctx context.Context) DBTX {
	if tx, ok := txFromContext(ctx); ok {
		return tx
	}
	return m.pool
}

func txFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txContextKey{}).(pgx.Tx)
	return tx, ok
}
