//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UnusualNick/template/internal/domain"
)

// TestRepositoryRollsBackBothTables uses a real PostgreSQL database with the
// existing migrations applied. All rows use test-generated IDs; no shared row
// is changed and the test can be repeated against the same database.
func TestRepositoryRollsBackBothTables(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	manager := NewTxManager(pool)
	repo := NewTripRepository(manager, 5*time.Second)
	trip := domain.Trip{
		ID: uuid.New(), UserID: uuid.New(), DriverID: uuid.New(),
		StartPoint: domain.Coordinates{Latitude: 10, Longitude: 20},
		EndPoint:   domain.Coordinates{Latitude: 11, Longitude: 21},
		Price:      100, Status: domain.TripStatusActive, StartedAt: time.Now().UTC(),
	}
	// Safety cleanup runs even if an implementation unexpectedly commits.
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, cleanupErr := pool.Exec(cleanupCtx, "DELETE FROM trips WHERE id = $1", trip.ID); cleanupErr != nil {
			t.Errorf("clean up test row: %v", cleanupErr)
		}
	}()

	err = manager.Do(ctx, func(txCtx context.Context) error {
		if err := repo.Insert(txCtx, trip); err != nil {
			return err
		}
		// A history row referring to a different, nonexistent trip must fail
		// its foreign key and roll back the successful insert above.
		return repo.InsertStatusHistory(txCtx, uuid.New(), nil, domain.TripStatusActive, "rollback test")
	})
	if err == nil {
		t.Fatal("transaction succeeded despite invalid history foreign key")
	}
	if _, err := repo.GetByID(ctx, trip.ID); !errors.Is(err, domain.ErrTripNotFound) {
		t.Fatalf("trip after rollback: got %v, want ErrTripNotFound", err)
	}
}

func TestRepositoryCreateAndFinishWithHistory(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	manager := NewTxManager(pool)
	repo := NewTripRepository(manager, 5*time.Second)
	trip := domain.Trip{
		ID: uuid.New(), UserID: uuid.New(), DriverID: uuid.New(),
		StartPoint: domain.Coordinates{Latitude: 10, Longitude: 20},
		EndPoint:   domain.Coordinates{Latitude: 11, Longitude: 21},
		Price:      100, Status: domain.TripStatusActive, StartedAt: time.Now().UTC(),
	}
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if _, cleanupErr := pool.Exec(cleanupCtx, "DELETE FROM trips WHERE id = $1", trip.ID); cleanupErr != nil {
			t.Errorf("clean up test row: %v", cleanupErr)
		}
	}()

	if err := manager.Do(ctx, func(txCtx context.Context) error {
		if err := repo.Insert(txCtx, trip); err != nil {
			return err
		}
		return repo.InsertStatusHistory(txCtx, trip.ID, nil, domain.TripStatusActive, "trip created")
	}); err != nil {
		t.Fatalf("create transaction: %v", err)
	}
	if got, err := repo.GetByID(ctx, trip.ID); err != nil || got.Status != domain.TripStatusActive {
		t.Fatalf("read created trip: status %q, error %v", got.Status, err)
	}

	finishedAt := time.Now().UTC()
	if err := manager.Do(ctx, func(txCtx context.Context) error {
		finished, err := repo.Finish(txCtx, trip.ID, finishedAt)
		if err != nil {
			return err
		}
		previous := domain.TripStatusActive
		return repo.InsertStatusHistory(txCtx, finished.ID, &previous, domain.TripStatusCompleted, "trip finished")
	}); err != nil {
		t.Fatalf("finish transaction: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM trip_status_history WHERE trip_id = $1", trip.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("history rows = %d, want 2", count)
	}
	if _, err := repo.Finish(ctx, trip.ID, time.Now().UTC()); !errors.Is(err, domain.ErrTripCompleted) {
		t.Fatalf("repeated finish: got %v, want ErrTripCompleted", err)
	}
}
