package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/UnusualNick/template/internal/domain"
)

const tripColumns = `id, user_id, driver_id,
start_latitude, start_longitude, end_latitude, end_longitude,
price, status, started_at, finished_at, NULL::timestamptz AS last_position_at`

type executorProvider interface {
	Executor(ctx context.Context) DBTX
}

type TripRepository struct {
	executors    executorProvider
	queryTimeout time.Duration
}

func NewTripRepository(executors executorProvider, queryTimeout time.Duration) *TripRepository {
	return &TripRepository{executors: executors, queryTimeout: queryTimeout}
}

func (r *TripRepository) Insert(ctx context.Context, trip domain.Trip) error {
	query, args, err := sq.Insert("trips").
		Columns(
			"id", "user_id", "driver_id",
			"start_latitude", "start_longitude", "end_latitude", "end_longitude",
			"price", "status", "started_at", "finished_at",
		).
		Values(
			trip.ID, trip.UserID, trip.DriverID,
			trip.StartPoint.Latitude, trip.StartPoint.Longitude,
			trip.EndPoint.Latitude, trip.EndPoint.Longitude,
			trip.Price, trip.Status, trip.StartedAt, trip.FinishedAt,
		).
		PlaceholderFormat(sq.Dollar).
		ToSql()
	if err != nil {
		return fmt.Errorf("build insert trip query: %w", err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	if _, err := r.executors.Executor(ctx).Exec(queryCtx, query, args...); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "trips_one_active_per_driver_uidx" {
			return domain.ErrDriverBusy
		}
		return fmt.Errorf("insert trip: %w", err)
	}
	return nil
}

func (r *TripRepository) InsertStatusHistory(
	ctx context.Context,
	tripID uuid.UUID,
	fromStatus *domain.TripStatus,
	toStatus domain.TripStatus,
	reason string,
) error {
	query, args, err := sq.Insert("trip_status_history").
		Columns("trip_id", "from_status", "to_status", "reason").
		Values(tripID, fromStatus, toStatus, reason).
		PlaceholderFormat(sq.Dollar).
		ToSql()
	if err != nil {
		return fmt.Errorf("build insert trip status query: %w", err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	if _, err := r.executors.Executor(ctx).Exec(queryCtx, query, args...); err != nil {
		return fmt.Errorf("insert trip status history: %w", err)
	}
	return nil
}

func (r *TripRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	query, args, err := sq.Select(tripColumns).
		From("trips").
		Where(sq.Eq{"id": id}).
		PlaceholderFormat(sq.Dollar).
		ToSql()
	if err != nil {
		return domain.Trip{}, fmt.Errorf("build get trip query: %w", err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	trip, err := scanTrip(r.executors.Executor(ctx).QueryRow(queryCtx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Trip{}, domain.ErrTripNotFound
	}
	if err != nil {
		return domain.Trip{}, fmt.Errorf("get trip: %w", err)
	}
	return trip, nil
}

func (r *TripRepository) Finish(ctx context.Context, id uuid.UUID, finishedAt time.Time) (domain.Trip, error) {
	query, args, err := sq.Update("trips").
		Set("status", domain.TripStatusCompleted).
		Set("finished_at", finishedAt).
		Set("updated_at", finishedAt).
		Where(sq.Eq{"id": id, "status": domain.TripStatusActive}).
		Suffix("RETURNING " + tripColumns).
		PlaceholderFormat(sq.Dollar).
		ToSql()
	if err != nil {
		return domain.Trip{}, fmt.Errorf("build finish trip query: %w", err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	trip, err := scanTrip(r.executors.Executor(ctx).QueryRow(queryCtx, query, args...))
	if err == nil {
		return trip, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Trip{}, fmt.Errorf("finish trip: %w", err)
	}

	current, getErr := r.GetByID(ctx, id)
	if errors.Is(getErr, domain.ErrTripNotFound) {
		return domain.Trip{}, domain.ErrTripNotFound
	}
	if getErr != nil {
		return domain.Trip{}, fmt.Errorf("check trip after unsuccessful finish: %w", getErr)
	}
	if current.Status == domain.TripStatusCompleted {
		return domain.Trip{}, domain.ErrTripCompleted
	}
	return domain.Trip{}, fmt.Errorf("trip %s was not finished", id)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTrip(row rowScanner) (domain.Trip, error) {
	var trip domain.Trip
	if err := row.Scan(
		&trip.ID,
		&trip.UserID,
		&trip.DriverID,
		&trip.StartPoint.Latitude,
		&trip.StartPoint.Longitude,
		&trip.EndPoint.Latitude,
		&trip.EndPoint.Longitude,
		&trip.Price,
		&trip.Status,
		&trip.StartedAt,
		&trip.FinishedAt,
		&trip.LastPositionAt,
	); err != nil {
		return domain.Trip{}, err
	}
	return trip, nil
}
