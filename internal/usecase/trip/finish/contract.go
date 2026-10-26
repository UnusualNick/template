package finish

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=finish

type txManager interface {
	Do(ctx context.Context, fn func(context.Context) error) error
}

type tripRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (domain.Trip, error)
	Finish(ctx context.Context, id uuid.UUID, finishedAt time.Time) (domain.Trip, error)
	InsertStatusHistory(ctx context.Context, tripID uuid.UUID, fromStatus *domain.TripStatus, toStatus domain.TripStatus, reason string) error
}
