package list

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type UseCase struct {
	repository positionRepository
}

func New(repository positionRepository) *UseCase {
	return &UseCase{repository: repository}
}

// Execute returns the complete route, including for finished trips. A missing
// trip is distinct from an existing trip whose route is empty.
func (u *UseCase) Execute(ctx context.Context, tripID uuid.UUID) ([]domain.TripPosition, error) {
	if tripID == uuid.Nil {
		return nil, fmt.Errorf("list trip positions: %w", &domain.FieldError{Field: "trip_id", Reason: "must be a non-zero UUID"})
	}
	if _, err := u.repository.GetByID(ctx, tripID); err != nil {
		return nil, fmt.Errorf("list trip positions: %w", err)
	}
	positions, err := u.repository.ListPositions(ctx, tripID)
	if err != nil {
		return nil, fmt.Errorf("list trip positions: %w", err)
	}
	if positions == nil {
		return []domain.TripPosition{}, nil
	}
	return positions, nil
}
