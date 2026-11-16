//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/UnusualNick/template/internal/domain"
)

func positionTestDB(t *testing.T) (*pgxpool.Pool, *TxManager, *TripRepository) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	manager := NewTxManager(pool)
	return pool, manager, NewTripRepository(manager, 5*time.Second)
}

// The migration must expose the official insertion timestamp independently of
// the client-supplied recorded_at; repository inserts leave it to PostgreSQL.
func TestPositionCreatedAtSchema(t *testing.T) {
	pool, _, _ := positionTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dataType, nullable, defaultValue string
	err := pool.QueryRow(ctx, `SELECT data_type, is_nullable, column_default
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'trip_positions' AND column_name = 'created_at'`).
		Scan(&dataType, &nullable, &defaultValue)
	if err != nil || dataType != "timestamp with time zone" || nullable != "NO" || defaultValue != "now()" {
		t.Fatalf("created_at schema = (%q, %q, %q), error = %v", dataType, nullable, defaultValue, err)
	}
}

func positionTestTrip(startedAt time.Time) domain.Trip {
	return domain.Trip{
		ID: uuid.New(), UserID: uuid.New(), DriverID: uuid.New(),
		StartPoint: domain.Coordinates{Latitude: 59.9, Longitude: 30.3},
		EndPoint:   domain.Coordinates{Latitude: 59.8, Longitude: 30.4},
		Status:     domain.TripStatusActive, StartedAt: startedAt,
	}
}

func TestPositionRepositoryTransactionsAndStaleness(t *testing.T) {
	pool, manager, repo := positionTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	trip := positionTestTrip(started)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM trips WHERE id = $1`, trip.ID); err != nil {
			t.Errorf("clean up trip: %v", err)
		}
	}()
	if err := repo.Insert(ctx, trip); err != nil {
		t.Fatal(err)
	}
	cutoff := started.Add(time.Hour)
	stale, err := repo.ListStale(ctx, cutoff)
	if err != nil || !containsTrip(stale, trip.ID) {
		t.Fatalf("trip with no positions should be stale: found=%t err=%v", containsTrip(stale, trip.ID), err)
	}

	firstAt := started.Add(90 * time.Minute)
	first := domain.TripPosition{TripID: trip.ID, Coordinates: domain.Coordinates{Latitude: 60, Longitude: 30}, RecordedAt: firstAt}
	assertPositionRollback(t, ctx, manager, repo, first)

	assertOlderPositionAndStaleness(t, ctx, manager, repo, first, started, cutoff)
	assertFinishedPosition(t, ctx, repo, trip.ID)
}

func assertPositionRollback(t *testing.T, ctx context.Context, manager *TxManager, repo *TripRepository, position domain.TripPosition) {
	t.Helper()
	rollback := errors.New("force rollback")
	if err := manager.Do(ctx, func(txCtx context.Context) error {
		if err := repo.UpdateLastPositionAt(txCtx, position.TripID, position.RecordedAt); err != nil {
			return err
		}
		if _, err := repo.InsertPosition(txCtx, position); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rolled-back save = %v, want marker", err)
	}
	got, err := repo.GetByID(ctx, position.TripID)
	if err != nil || got.LastPositionAt != nil {
		t.Fatalf("rollback must restore last_position_at: %v, %v", got.LastPositionAt, err)
	}
	positions, err := repo.ListPositions(ctx, position.TripID)
	if err != nil || len(positions) != 0 {
		t.Fatalf("rollback must remove position: %+v, %v", positions, err)
	}
}

func assertOlderPositionAndStaleness(t *testing.T, ctx context.Context, manager *TxManager, repo *TripRepository, first domain.TripPosition, started, cutoff time.Time) {
	t.Helper()
	if err := manager.Do(ctx, func(txCtx context.Context) error {
		if err := repo.UpdateLastPositionAt(txCtx, first.TripID, first.RecordedAt); err != nil {
			return err
		}
		var err error
		first, err = repo.InsertPosition(txCtx, first)
		return err
	}); err != nil || first.ID <= 0 {
		t.Fatalf("save first position: id=%d err=%v", first.ID, err)
	}
	olderAt := started.Add(15 * time.Minute)
	older := domain.TripPosition{TripID: first.TripID, Coordinates: domain.Coordinates{Latitude: 61, Longitude: 31}, RecordedAt: olderAt}
	if err := manager.Do(ctx, func(txCtx context.Context) error {
		if err := repo.UpdateLastPositionAt(txCtx, older.TripID, olderAt); err != nil {
			return err
		}
		_, err := repo.InsertPosition(txCtx, older)
		return err
	}); err != nil {
		t.Fatalf("save older position: %v", err)
	}
	got, err := repo.GetByID(ctx, first.TripID)
	if err != nil || got.LastPositionAt == nil || !got.LastPositionAt.Equal(first.RecordedAt) {
		t.Fatalf("last_position_at must stay at newest sample: %v, err=%v", got.LastPositionAt, err)
	}
	positions, err := repo.ListPositions(ctx, first.TripID)
	if err != nil || len(positions) != 2 || !positions[0].RecordedAt.Equal(olderAt) || !positions[1].RecordedAt.Equal(first.RecordedAt) {
		t.Fatalf("positions not ordered by recorded_at: %+v, err=%v", positions, err)
	}
	stale, err := repo.ListStale(ctx, cutoff)
	if err != nil || containsTrip(stale, first.TripID) {
		t.Fatalf("newest position should determine staleness: found=%t err=%v", containsTrip(stale, first.TripID), err)
	}
}

func assertFinishedPosition(t *testing.T, ctx context.Context, repo *TripRepository, tripID uuid.UUID) {
	t.Helper()
	if _, err := repo.Finish(ctx, tripID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateLastPositionAt(ctx, tripID, time.Now()); !errors.Is(err, domain.ErrTripCompleted) {
		t.Fatalf("save on finished trip = %v, want ErrTripCompleted", err)
	}
	if err := repo.UpdateLastPositionAt(ctx, uuid.New(), time.Now()); !errors.Is(err, domain.ErrTripNotFound) {
		t.Fatalf("save on absent trip = %v, want ErrTripNotFound", err)
	}
	stale, err := repo.ListStale(ctx, time.Now().Add(time.Hour))
	if err != nil || containsTrip(stale, tripID) {
		t.Fatalf("completed trip should not be stale: found=%t err=%v", containsTrip(stale, tripID), err)
	}
	positions, err := repo.ListPositions(ctx, tripID)
	if err != nil || len(positions) != 2 {
		t.Fatalf("completed trip route should remain readable: %+v err=%v", positions, err)
	}
}

func containsTrip(trips []domain.Trip, id uuid.UUID) bool {
	for _, trip := range trips {
		if trip.ID == id {
			return true
		}
	}
	return false
}

// EXPLAIN on thousands of real rows checks the expression index is actually
// usable for the worker's precise predicate and ordering, not merely present.
func TestPositionStaleIndexPlan(t *testing.T) {
	pool, _, _ := positionTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rollbackCancel()
		if err := tx.Rollback(rollbackCtx); err != nil {
			t.Errorf("rollback EXPLAIN fixtures: %v", err)
		}
	}()
	// Each trip needs a different driver because the active-driver invariant is
	// enforced in PostgreSQL. IDs are deterministic but contained by rollback.
	const fixtures = `INSERT INTO trips (id,user_id,driver_id,start_latitude,start_longitude,end_latitude,end_longitude,price,status,started_at,last_position_at)
SELECT md5('stale-trip-' || n)::uuid, md5('stale-user-' || n)::uuid, md5('stale-driver-' || n)::uuid,
0,0,0,0,0,'active',now() - interval '2 hours',
CASE WHEN n <= 30 THEN now() - interval '2 hours' ELSE now() END
FROM generate_series(1, 3000) AS n`
	if _, err := tx.Exec(ctx, fixtures); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE trips`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL enable_bitmapscan = off`); err != nil {
		t.Fatal(err)
	}
	const explain = `EXPLAIN (ANALYZE, BUFFERS)
SELECT id, user_id, driver_id, start_latitude, start_longitude, end_latitude, end_longitude,
price, status, started_at, finished_at, last_position_at
FROM trips WHERE status = 'active' AND COALESCE(last_position_at, started_at) < $1
ORDER BY COALESCE(last_position_at, started_at), id`
	rows, err := tx.Query(ctx, explain, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stale query plan over 3000 trips:\n%s", plan.String())
	if !strings.Contains(plan.String(), "trips_active_stale_at_idx") {
		t.Fatalf("stale query did not use partial expression index:\n%s", plan.String())
	}
}
