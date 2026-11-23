package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/UnusualNick/template/internal/domain"
)

// UpdateLastPositionAt locks an active trip row until the enclosing transaction
// commits, serializing position insertion against trip completion. Older samples
// may be stored, but can never move the latest position timestamp backwards.
func (r *TripRepository) UpdateLastPositionAt(ctx context.Context, tripID uuid.UUID, recordedAt time.Time) error {
	const query = `UPDATE trips
SET last_position_at = GREATEST(COALESCE(last_position_at, $2), $2), updated_at = now()
WHERE id = $1 AND status = 'active'
RETURNING id`
	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	var id uuid.UUID
	err := r.executors.Executor(ctx).QueryRow(queryCtx, query, tripID, recordedAt).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("update last position: %w", err)
	}

	trip, getErr := r.GetByID(ctx, tripID)
	if errors.Is(getErr, domain.ErrTripNotFound) {
		return domain.ErrTripNotFound
	}
	if getErr != nil {
		return fmt.Errorf("check trip after unsuccessful position update: %w", getErr)
	}
	if trip.Status == domain.TripStatusCompleted {
		return domain.ErrTripCompleted
	}
	return fmt.Errorf("trip %s was not updated with its last position", tripID)
}

func (r *TripRepository) InsertPosition(ctx context.Context, position domain.TripPosition) (domain.TripPosition, error) {
	const query = `INSERT INTO trip_positions (trip_id, latitude, longitude, recorded_at)
VALUES ($1, $2, $3, $4) RETURNING id`
	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	if err := r.executors.Executor(ctx).QueryRow(queryCtx, query,
		position.TripID, position.Coordinates.Latitude, position.Coordinates.Longitude, position.RecordedAt,
	).Scan(&position.ID); err != nil {
		return domain.TripPosition{}, fmt.Errorf("insert trip position: %w", err)
	}
	position.RecordedAt = position.RecordedAt.UTC().Truncate(time.Microsecond)
	return position, nil
}

func (r *TripRepository) ListPositions(ctx context.Context, tripID uuid.UUID) ([]domain.TripPosition, error) {
	const query = `SELECT id, trip_id, latitude, longitude, recorded_at
FROM trip_positions WHERE trip_id = $1 ORDER BY recorded_at, id`
	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.executors.Executor(ctx).Query(queryCtx, query, tripID)
	if err != nil {
		return nil, fmt.Errorf("list trip positions: %w", err)
	}
	defer rows.Close()
	positions := make([]domain.TripPosition, 0)
	for rows.Next() {
		var position domain.TripPosition
		if err := rows.Scan(&position.ID, &position.TripID,
			&position.Coordinates.Latitude, &position.Coordinates.Longitude, &position.RecordedAt); err != nil {
			return nil, fmt.Errorf("scan trip position: %w", err)
		}
		position.RecordedAt = position.RecordedAt.UTC().Truncate(time.Microsecond)
		positions = append(positions, position)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trip positions: %w", err)
	}
	return positions, nil
}

// ListStale uses the partial expression index on active trips; completed trips
// are never returned. Trips without positions age from their start time.
func (r *TripRepository) ListStale(ctx context.Context, before time.Time) ([]domain.Trip, error) {
	const query = `SELECT ` + tripColumns + ` FROM trips
WHERE status = 'active' AND COALESCE(last_position_at, started_at) < $1
ORDER BY COALESCE(last_position_at, started_at), id`
	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.executors.Executor(ctx).Query(queryCtx, query, before)
	if err != nil {
		return nil, fmt.Errorf("list stale trips: %w", err)
	}
	defer rows.Close()
	trips := make([]domain.Trip, 0)
	for rows.Next() {
		trip, err := scanTrip(rows)
		if err != nil {
			return nil, fmt.Errorf("scan stale trip: %w", err)
		}
		trips = append(trips, trip)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale trips: %w", err)
	}
	return trips, nil
}
