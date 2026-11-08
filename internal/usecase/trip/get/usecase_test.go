package get

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/internal/domain"
)

type callerContextKey struct{}

func TestUseCaseExecute(t *testing.T) {
	id := uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111")
	finishedAt := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	trip := domain.Trip{
		ID: id, UserID: uuid.New(), DriverID: uuid.New(),
		StartPoint: domain.Coordinates{Latitude: 55.75, Longitude: 37.62},
		EndPoint:   domain.Coordinates{Latitude: 59.93, Longitude: 30.31},
		Price:      500, Status: domain.TripStatusCompleted,
		StartedAt: finishedAt.Add(-time.Hour), FinishedAt: &finishedAt,
	}
	storageErr := errors.New("storage unavailable")

	for _, tt := range []struct {
		name    string
		trip    domain.Trip
		repoErr error
	}{
		{name: "success", trip: trip},
		{name: "trip not found", repoErr: domain.ErrTripNotFound},
		{name: "repository failure", repoErr: storageErr},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), callerContextKey{}, "caller context")
			repo := NewMockrepository(gomock.NewController(t))
			repo.EXPECT().GetByID(ctx, id).Return(tt.trip, tt.repoErr)

			got, err := New(repo).Execute(ctx, id)
			if tt.repoErr == nil {
				if err != nil || !reflect.DeepEqual(got, tt.trip) {
					t.Fatalf("Execute() = (%+v, %v), want (%+v, nil)", got, err, tt.trip)
				}
				return
			}
			if !reflect.DeepEqual(got, domain.Trip{}) || !errors.Is(err, tt.repoErr) || !strings.Contains(err.Error(), "get trip: ") {
				t.Fatalf("Execute() = (%+v, %v), want zero trip and wrapped %v", got, err, tt.repoErr)
			}
		})
	}
}
