package create

import (
	"context"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=create

type tripRepository interface {
	Insert(ctx context.Context, trip domain.Trip) error
	InsertStatusHistory(ctx context.Context, tripID uuid.UUID, fromStatus *domain.TripStatus, toStatus domain.TripStatus, reason string) error
}

type txManager interface {
	Do(ctx context.Context, fn func(context.Context) error) error
}
