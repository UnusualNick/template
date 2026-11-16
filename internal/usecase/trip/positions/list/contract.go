package list

import (
	"context"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=list

type positionRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (domain.Trip, error)
	// ListPositions returns positions ordered by recorded_at ascending, then id.
	ListPositions(ctx context.Context, tripID uuid.UUID) ([]domain.TripPosition, error)
}
