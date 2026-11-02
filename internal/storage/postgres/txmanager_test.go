package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakePool struct {
	tx         *fakeTx
	beginCalls int
}

func (p *fakePool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	p.beginCalls++
	return p.tx, nil
}

func (*fakePool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (*fakePool) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (*fakePool) QueryRow(context.Context, string, ...any) pgx.Row        { return fakeRow{} }

type fakeTx struct {
	commits            int
	rollbacks          int
	rollbackErr        error
	rollbackContextErr error
	rollbackDeadline   bool
}

func (t *fakeTx) Begin(context.Context) (pgx.Tx, error) { return t, nil }
func (t *fakeTx) Commit(context.Context) error {
	t.commits++
	return nil
}

func (t *fakeTx) Rollback(ctx context.Context) error {
	t.rollbacks++
	t.rollbackContextErr = ctx.Err()
	_, t.rollbackDeadline = ctx.Deadline()
	return t.rollbackErr
}

func (*fakeTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (*fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (*fakeTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (*fakeTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, nil
}

func (*fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (*fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (*fakeTx) QueryRow(context.Context, string, ...any) pgx.Row        { return fakeRow{} }
func (*fakeTx) Conn() *pgx.Conn                                         { return nil }

type fakeRow struct{}

func (fakeRow) Scan(...any) error { return pgx.ErrNoRows }

func TestTxManagerReusesTransactionForNestedDo(t *testing.T) {
	tx := &fakeTx{}
	pool := &fakePool{tx: tx}
	manager := NewTxManager(pool)

	err := manager.Do(context.Background(), func(ctx context.Context) error {
		if manager.Executor(ctx) != tx {
			t.Fatal("repository executor is not current transaction")
		}
		return manager.Do(ctx, func(nestedCtx context.Context) error {
			if manager.Executor(nestedCtx) != tx {
				t.Fatal("nested executor is not current transaction")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if pool.beginCalls != 1 || tx.commits != 1 || tx.rollbacks != 0 {
		t.Fatalf("begin/commit/rollback = %d/%d/%d", pool.beginCalls, tx.commits, tx.rollbacks)
	}
}

func TestTxManagerRollsBackOnError(t *testing.T) {
	tx := &fakeTx{}
	manager := NewTxManager(&fakePool{tx: tx})
	want := errors.New("boom")

	err := manager.Do(context.Background(), func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Do() error = %v", err)
	}
	if tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("commit/rollback = %d/%d", tx.commits, tx.rollbacks)
	}
}

func TestTxManagerRollsBackAndRepanics(t *testing.T) {
	tx := &fakeTx{}
	manager := NewTxManager(&fakePool{tx: tx})

	defer func() {
		if recover() == nil {
			t.Fatal("Do() did not propagate panic")
		}
		if tx.rollbacks != 1 {
			t.Fatalf("rollbacks = %d", tx.rollbacks)
		}
	}()
	_ = manager.Do(context.Background(), func(context.Context) error { panic("boom") })
}

func TestTxManagerRollsBackAfterRequestCancellation(t *testing.T) {
	tx := &fakeTx{}
	manager := NewTxManager(&fakePool{tx: tx})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("operation failed")

	err := manager.Do(ctx, func(context.Context) error {
		cancel()
		return want
	})
	if !errors.Is(err, want) || tx.rollbacks != 1 {
		t.Fatalf("error = %v, rollbacks = %d", err, tx.rollbacks)
	}
	if tx.rollbackContextErr != nil || !tx.rollbackDeadline {
		t.Fatalf("rollback context: error = %v, has deadline = %t", tx.rollbackContextErr, tx.rollbackDeadline)
	}
}

func TestTxManagerRollsBackCanceledPanic(t *testing.T) {
	tx := &fakeTx{}
	manager := NewTxManager(&fakePool{tx: tx})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	func() {
		defer func() {
			if got := recover(); got != "boom" {
				t.Fatalf("panic = %v", got)
			}
		}()
		_ = manager.Do(ctx, func(context.Context) error {
			cancel()
			panic("boom")
		})
	}()
	if tx.rollbacks != 1 || tx.rollbackContextErr != nil || !tx.rollbackDeadline {
		t.Fatalf("rollback count = %d, context error = %v, has deadline = %t",
			tx.rollbacks, tx.rollbackContextErr, tx.rollbackDeadline)
	}
}

func TestTxManagerPreservesOperationAndRollbackErrors(t *testing.T) {
	operationErr := errors.New("operation failed")
	rollbackErr := errors.New("rollback failed")
	tx := &fakeTx{rollbackErr: rollbackErr}
	manager := NewTxManager(&fakePool{tx: tx})

	err := manager.Do(context.Background(), func(context.Context) error { return operationErr })
	if !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("error does not preserve both causes: %v", err)
	}
}

func TestTxManagerIgnoresWrappedClosedTransaction(t *testing.T) {
	operationErr := errors.New("operation failed")
	tx := &fakeTx{rollbackErr: fmt.Errorf("already closed: %w", pgx.ErrTxClosed)}
	manager := NewTxManager(&fakePool{tx: tx})

	err := manager.Do(context.Background(), func(context.Context) error { return operationErr })
	if err != operationErr {
		t.Fatalf("error = %v, want original operation error", err)
	}
}

func TestTxManagerLogsFailedRollbackOnPanic(t *testing.T) {
	var output bytes.Buffer
	rollbackErr := errors.New("rollback unavailable")
	tx := &fakeTx{rollbackErr: rollbackErr}
	manager := NewTxManager(&fakePool{tx: tx})
	manager.logger = slog.New(slog.NewJSONHandler(&output, nil))
	want := errors.New("panic cause")

	func() {
		defer func() {
			if got := recover(); got != want {
				t.Fatalf("panic = %v, want original %v", got, want)
			}
		}()
		_ = manager.Do(context.Background(), func(context.Context) error { panic(want) })
	}()

	if tx.rollbacks != 1 || !strings.Contains(output.String(), "rollback transaction after panic failed") ||
		!strings.Contains(output.String(), rollbackErr.Error()) {
		t.Fatalf("rollback count = %d, log = %q", tx.rollbacks, output.String())
	}
}
