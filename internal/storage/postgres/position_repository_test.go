package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/internal/domain"
)

type positionRow func(...any) error

func (row positionRow) Scan(dest ...any) error { return row(dest...) }

func TestUpdateLastPositionAtSQLAndErrors(t *testing.T) {
	tripID := uuid.New()
	at := time.Now().UTC()
	dbErr := errors.New("database unavailable")
	for _, tc := range []struct {
		name      string
		updateErr error
		lookupErr error
		status    domain.TripStatus
		wantErr   error
	}{
		{name: "active updated"},
		{name: "completed", updateErr: pgx.ErrNoRows, status: domain.TripStatusCompleted, wantErr: domain.ErrTripCompleted},
		{name: "absent", updateErr: pgx.ErrNoRows, lookupErr: pgx.ErrNoRows, wantErr: domain.ErrTripNotFound},
		{name: "database error", updateErr: dbErr, wantErr: dbErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			pool := NewMockPool(ctrl)
			repo := NewTripRepository(NewTxManager(pool), time.Second)
			pool.EXPECT().QueryRow(gomock.Any(), gomock.Any(), tripID, at).DoAndReturn(func(_ context.Context, sql string, _ ...any) pgx.Row {
				if !strings.Contains(sql, "status = 'active'") || !strings.Contains(sql, "GREATEST(COALESCE(last_position_at, $2), $2)") {
					t.Errorf("position update lacks active-only monotonic guard: %s", sql)
				}
				return positionRow(func(dest ...any) error {
					if tc.updateErr == nil {
						*dest[0].(*uuid.UUID) = tripID
					}
					return tc.updateErr
				})
			})
			if errors.Is(tc.updateErr, pgx.ErrNoRows) {
				pool.EXPECT().QueryRow(gomock.Any(), gomock.Any(), gomock.Any()).Return(positionRow(func(dest ...any) error {
					if tc.lookupErr != nil {
						return tc.lookupErr
					}
					if err := (timestampRow{started: at}).Scan(dest...); err != nil {
						return err
					}
					*dest[8].(*domain.TripStatus) = tc.status
					return nil
				}))
			}
			err := repo.UpdateLastPositionAt(context.Background(), tripID, at)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("UpdateLastPositionAt() = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("UpdateLastPositionAt() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestInsertPositionReturnsDatabaseID(t *testing.T) {
	ctrl := gomock.NewController(t)
	pool := NewMockPool(ctrl)
	repo := NewTripRepository(NewTxManager(pool), time.Second)
	position := domain.TripPosition{TripID: uuid.New(), Coordinates: domain.Coordinates{Latitude: 60, Longitude: 30}, RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 987654321, time.FixedZone("east", 3600))}
	pool.EXPECT().QueryRow(gomock.Any(), gomock.Any(), position.TripID, 60.0, 30.0, position.RecordedAt).Return(positionRow(func(dest ...any) error {
		*dest[0].(*int64) = 123
		return nil
	}))
	got, err := repo.InsertPosition(context.Background(), position)
	if err != nil || got.ID != 123 || got.TripID != position.TripID || got.Coordinates != position.Coordinates || !got.RecordedAt.Equal(position.RecordedAt.UTC().Truncate(time.Microsecond)) || got.RecordedAt.Location() != time.UTC {
		t.Fatalf("InsertPosition() = (%+v, %v)", got, err)
	}
}
