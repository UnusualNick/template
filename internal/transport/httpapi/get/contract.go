package get

import (
	"context"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=get

type usecase interface {
	Get(ctx context.Context, id uuid.UUID) (domain.Trip, error)
}
