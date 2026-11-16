package positions

import (
	"context"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type saveUsecase interface {
	Execute(ctx context.Context, tripID uuid.UUID, input domain.CreatePosition) (domain.TripPosition, error)
}

type listUsecase interface {
	Execute(ctx context.Context, tripID uuid.UUID) ([]domain.TripPosition, error)
}
