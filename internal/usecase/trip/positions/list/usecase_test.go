package list

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/internal/domain"
)

func TestExecute(t *testing.T) {
	id := uuid.New()
	positions := []domain.TripPosition{{ID: 7, TripID: id, RecordedAt: time.Now()}}
	listErr := errors.New("list unavailable")
	for _, tc := range []struct {
		name      string
		id        uuid.UUID
		status    domain.TripStatus
		positions []domain.TripPosition
		getErr    error
		listErr   error
		wantErr   error
	}{
		{name: "active route", id: id, status: domain.TripStatusActive, positions: positions},
		{name: "completed route", id: id, status: domain.TripStatusCompleted, positions: positions},
		{name: "empty completed route", id: id, status: domain.TripStatusCompleted},
		{name: "zero ID", wantErr: domain.ErrInvalidInput},
		{name: "missing trip", id: id, getErr: domain.ErrTripNotFound, wantErr: domain.ErrTripNotFound},
		{name: "repository error", id: id, listErr: listErr, wantErr: listErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			repository := NewMockpositionRepository(ctrl)
			ctx := context.Background()
			if tc.id != uuid.Nil {
				get := repository.EXPECT().GetByID(ctx, id).Return(domain.Trip{ID: id, Status: tc.status}, tc.getErr)
				if tc.getErr == nil {
					repository.EXPECT().ListPositions(ctx, id).Return(tc.positions, tc.listErr).After(get)
				}
			}
			got, err := New(repository).Execute(ctx, tc.id)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Execute() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if got != nil {
					t.Errorf("failed Execute() = %+v, want nil", got)
				}
				return
			}
			if got == nil || len(got) != len(tc.positions) {
				t.Errorf("Execute() = %+v, want non-nil route length %d", got, len(tc.positions))
			}
			if len(got) == 1 && got[0] != positions[0] {
				t.Errorf("Execute() = %+v, want %+v", got, positions)
			}
		})
	}
}
