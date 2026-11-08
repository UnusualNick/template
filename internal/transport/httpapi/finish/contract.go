package finish

import (
	"context"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=finish

type finishUsecase interface {
	Finish(ctx context.Context, id uuid.UUID) (domain.Trip, error)
}
