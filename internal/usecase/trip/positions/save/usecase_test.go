package save

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/internal/domain"
)

type txKey struct{}

const updateFailure = "update"

func TestExecute(t *testing.T) {
	tripID := uuid.New()
	now := time.Date(2026, 1, 2, 3, 4, 5, 987654321, time.UTC)
	recordedAt := now.Add(-time.Hour).In(time.FixedZone("east", 3*3600))
	input := domain.CreatePosition{
		Coordinates: domain.Coordinates{Latitude: 59.9, Longitude: 30.3},
		RecordedAt:  recordedAt,
	}
	position := domain.TripPosition{TripID: tripID, Coordinates: input.Coordinates, RecordedAt: recordedAt.UTC().Truncate(time.Microsecond)}
	saved := position
	saved.ID = 42
	updateErr := domain.ErrTripCompleted
	insertErr := errors.New("insert failed")
	commitErr := errors.New("commit failed")
	for _, tc := range []struct {
		name      string
		tripID    uuid.UUID
		input     domain.CreatePosition
		failure   string
		wantErr   error
		wantField string
		wantSaved bool
		wantClock int
	}{
		{name: "saved", tripID: tripID, input: input, wantSaved: true, wantClock: 1},
		{name: "zero trip ID", input: input, wantErr: domain.ErrInvalidInput, wantField: "trip_id"},
		{name: "future sample", tripID: tripID, input: domain.CreatePosition{Coordinates: input.Coordinates, RecordedAt: now.Add(time.Minute + time.Nanosecond)}, wantErr: domain.ErrInvalidInput, wantField: "recorded_at", wantClock: 1},
		{name: "completed trip", tripID: tripID, input: input, failure: updateFailure, wantErr: updateErr, wantClock: 1},
		{name: "insert failure", tripID: tripID, input: input, failure: "insert", wantErr: insertErr, wantClock: 1},
		{name: "commit failure", tripID: tripID, input: input, failure: "commit", wantErr: commitErr, wantClock: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := NewMockpositionRepository(ctrl)
			txs := NewMocktxManager(ctrl)
			ctx := context.WithValue(context.Background(), txKey{}, "request")
			txCtx := context.WithValue(ctx, txKey{}, "transaction")
			clockCalls, hookCalls := 0, 0
			committed := false
			uc := New(repository, txs, func() time.Time {
				clockCalls++
				return now
			}, func(hookCtx context.Context, hookTripID uuid.UUID) {
				if hookCtx != ctx || hookTripID != tripID || !committed {
					t.Errorf("hook(ctx=%v,id=%v,committed=%t) should run after commit with request context", hookCtx, hookTripID, committed)
				}
				hookCalls++
			})
			if tc.wantField == "" {
				txs.EXPECT().Do(ctx, gomock.Any()).DoAndReturn(func(_ context.Context, fn func(context.Context) error) error {
					if err := fn(txCtx); err != nil {
						return err
					}
					if tc.failure == "commit" {
						return commitErr
					}
					committed = true
					return nil
				})
				update := repository.EXPECT().UpdateLastPositionAt(txCtx, tripID, position.RecordedAt).Return(func() error {
					if tc.failure == updateFailure {
						return updateErr
					}
					return nil
				}())
				if tc.failure != updateFailure {
					repository.EXPECT().InsertPosition(txCtx, position).Return(func() (domain.TripPosition, error) {
						if tc.failure == "insert" {
							return domain.TripPosition{}, insertErr
						}
						return saved, nil
					}()).After(update)
				}
			}
			got, err := uc.Execute(ctx, tc.tripID, tc.input)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Execute() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got != saved {
				t.Errorf("Execute() = %+v, want %+v", got, saved)
			} else if tc.wantErr != nil && got != (domain.TripPosition{}) {
				t.Errorf("failed Execute() = %+v, want zero value", got)
			}
			if tc.wantField != "" {
				var fieldErr *domain.FieldError
				if !errors.As(err, &fieldErr) || fieldErr.Field != tc.wantField {
					t.Errorf("Execute() error = %v, want field %q", err, tc.wantField)
				}
			}
			if clockCalls != tc.wantClock {
				t.Errorf("clock called %d times, want %d", clockCalls, tc.wantClock)
			}
			if hookCalls != boolInt(tc.wantSaved) {
				t.Errorf("hook called %d times, want %t", hookCalls, tc.wantSaved)
			}
		})
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestNewDefaultClock(t *testing.T) {
	uc := New(nil, nil, nil, nil)
	if uc.clock == nil || uc.clock().IsZero() {
		t.Fatal("nil or invalid default clock")
	}
}
