package save

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=contract.go -destination=mocks_test.go -package=save

// positionRepository locks and advances the active trip before inserting a
// position in the same transaction. UpdateLastPositionAt distinguishes missing
// trips from completed trips; it must not lower the existing timestamp.
type positionRepository interface {
	UpdateLastPositionAt(ctx context.Context, tripID uuid.UUID, recordedAt time.Time) error
	InsertPosition(ctx context.Context, position domain.TripPosition) (domain.TripPosition, error)
}

type txManager interface {
	Do(ctx context.Context, fn func(context.Context) error) error
}
