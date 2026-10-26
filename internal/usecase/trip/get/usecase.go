package get

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type UseCase struct {
	repository repository
}

func New(repository repository) *UseCase {
	return &UseCase{repository: repository}
}

func (uc *UseCase) Execute(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	trip, err := uc.repository.GetByID(ctx, id)
	if err != nil {
		return domain.Trip{}, fmt.Errorf("get trip: %w", err)
	}
	return trip, nil
}
