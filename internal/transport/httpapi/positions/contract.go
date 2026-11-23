package positions

import (
	"context"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=positions -mock_names=saveUsecase=MockSaveUsecase,listUsecase=MockListUsecase

type saveUsecase interface {
	Execute(ctx context.Context, tripID uuid.UUID, input domain.CreatePosition) (domain.TripPosition, error)
}

type listUsecase interface {
	Execute(ctx context.Context, tripID uuid.UUID) ([]domain.TripPosition, error)
}
