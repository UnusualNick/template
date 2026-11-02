package postgres

import (
	"context"
	"errors"
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
