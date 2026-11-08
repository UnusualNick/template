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
	"go.uber.org/mock/gomock"
)

//go:generate sh -c "go run go.uber.org/mock/mockgen -source=txmanager_test.go -destination=mocks_test.go -package=postgres -mock_names=poolMockContract=MockPool,txMockContract=MockTx && sed -n '/^..go:generate /p' txmanager_test.go >> mocks_test.go"

// poolMockContract combines the interfaces NewTxManager expects.
type poolMockContract interface {
	DBTX
	beginner
}

// txMockContract exposes the complete pgx transaction to mockgen.
type txMockContract interface {
	pgx.Tx
}

var (
	_ poolMockContract = (*MockPool)(nil)
	_ txMockContract   = (*MockTx)(nil)
)

func newTestTxManager(t *testing.T) (*TxManager, *MockTx) {
	t.Helper()
	ctrl := gomock.NewController(t)
	tx := NewMockTx(ctrl)
	pool := NewMockPool(ctrl)
	pool.EXPECT().BeginTx(gomock.Any(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted}).Return(tx, nil)
	return NewTxManager(pool), tx
}

func TestTxManagerReusesTransactionForNestedDo(t *testing.T) {
	ctrl := gomock.NewController(t)
	tx := NewMockTx(ctrl)
	pool := NewMockPool(ctrl)
	pool.EXPECT().BeginTx(gomock.Any(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted}).Return(tx, nil)
	tx.EXPECT().Commit(gomock.Any()).Return(nil)
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
}

func TestTxManagerRollsBackOnError(t *testing.T) {
	ctrl := gomock.NewController(t)
	tx := NewMockTx(ctrl)
	pool := NewMockPool(ctrl)
	pool.EXPECT().BeginTx(gomock.Any(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted}).Return(tx, nil)
	tx.EXPECT().Rollback(gomock.Any()).Return(nil)
	manager := NewTxManager(pool)
	want := errors.New("boom")

	err := manager.Do(context.Background(), func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestTxManagerRollsBackAndRepanics(t *testing.T) {
	ctrl := gomock.NewController(t)
	tx := NewMockTx(ctrl)
	pool := NewMockPool(ctrl)
	pool.EXPECT().BeginTx(gomock.Any(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted}).Return(tx, nil)
	tx.EXPECT().Rollback(gomock.Any()).Return(nil)
	manager := NewTxManager(pool)

	defer func() {
		if recover() == nil {
			t.Fatal("Do() did not propagate panic")
		}
	}()
	_ = manager.Do(context.Background(), func(context.Context) error { panic("boom") })
}

func TestTxManagerRollsBackAfterRequestCancellation(t *testing.T) {
	manager, tx := newTestTxManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("operation failed")

	tx.EXPECT().Rollback(gomock.Any()).DoAndReturn(func(rollbackCtx context.Context) error {
		if rollbackCtx.Err() != nil {
			t.Errorf("rollback context canceled: %v", rollbackCtx.Err())
		}
		if _, ok := rollbackCtx.Deadline(); !ok {
			t.Error("rollback context has no deadline")
		}
		return nil
	})
	err := manager.Do(ctx, func(context.Context) error {
		cancel()
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestTxManagerRollsBackCanceledPanic(t *testing.T) {
	manager, tx := newTestTxManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tx.EXPECT().Rollback(gomock.Any()).DoAndReturn(func(rollbackCtx context.Context) error {
		if rollbackCtx.Err() != nil {
			t.Errorf("rollback context canceled: %v", rollbackCtx.Err())
		}
		if _, ok := rollbackCtx.Deadline(); !ok {
			t.Error("rollback context has no deadline")
		}
		return nil
	})
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
}

func TestTxManagerPreservesOperationAndRollbackErrors(t *testing.T) {
	operationErr := errors.New("operation failed")
	rollbackErr := errors.New("rollback failed")
	manager, tx := newTestTxManager(t)
	tx.EXPECT().Rollback(gomock.Any()).Return(rollbackErr)

	err := manager.Do(context.Background(), func(context.Context) error { return operationErr })
	if !errors.Is(err, operationErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("error does not preserve both causes: %v", err)
	}
}

func TestTxManagerIgnoresWrappedClosedTransaction(t *testing.T) {
	operationErr := errors.New("operation failed")
	manager, tx := newTestTxManager(t)
	tx.EXPECT().Rollback(gomock.Any()).Return(fmt.Errorf("already closed: %w", pgx.ErrTxClosed))

	err := manager.Do(context.Background(), func(context.Context) error { return operationErr })
	if err != operationErr {
		t.Fatalf("error = %v, want original operation error", err)
	}
}

func TestTxManagerLogsFailedRollbackOnPanic(t *testing.T) {
	var output bytes.Buffer
	rollbackErr := errors.New("rollback unavailable")
	manager, tx := newTestTxManager(t)
	tx.EXPECT().Rollback(gomock.Any()).Return(rollbackErr)
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

	if !strings.Contains(output.String(), "rollback transaction after panic failed") ||
		!strings.Contains(output.String(), rollbackErr.Error()) {
		t.Fatalf("log = %q", output.String())
	}
}
