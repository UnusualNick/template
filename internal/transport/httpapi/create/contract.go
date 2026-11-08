package create

import (
	"context"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=create

type usecase interface {
	Create(ctx context.Context, input domain.CreateTrip) (domain.Trip, error)
}
