package postgres

import (
	"context"
	"errors"
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
	commits   int
	rollbacks int
}

func (t *fakeTx) Begin(context.Context) (pgx.Tx, error) { return t, nil }
func (t *fakeTx) Commit(context.Context) error {
	t.commits++
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	t.rollbacks++
	return nil
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
