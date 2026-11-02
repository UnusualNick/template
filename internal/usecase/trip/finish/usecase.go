package finish

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type UseCase struct {
	repository tripRepository
	txManager  txManager
	now        func() time.Time
	onFinished func(context.Context)
}

// New creates a finish use case. The optional hook runs only after commit.
func New(repository tripRepository, txManager txManager, onFinished ...func(context.Context)) *UseCase {
	uc := &UseCase{repository: repository, txManager: txManager, now: time.Now}
	if len(onFinished) > 0 {
		uc.onFinished = onFinished[0]
	}
	return uc
}

func (uc *UseCase) Execute(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	var trip domain.Trip
	err := uc.txManager.Do(ctx, func(txCtx context.Context) error {
		current, err := uc.repository.GetByID(txCtx, id)
		if err != nil {
			return err
		}
		at := uc.now().UTC()
		if err := current.Finish(at); err != nil {
			return err
		}
		// Finish conditionally updates only an active row. A concurrent finisher
		// can win after GetByID; its error must abort this transaction.
		trip, err = uc.repository.Finish(txCtx, id, at)
		if err != nil {
			return err
		}
		from := domain.TripStatusActive
		return uc.repository.InsertStatusHistory(txCtx, trip.ID, &from, domain.TripStatusCompleted, "trip finished")
	})
	if err != nil {
		return domain.Trip{}, fmt.Errorf("finish trip: %w", err)
	}
	if uc.onFinished != nil {
		uc.onFinished(ctx)
	}
	return trip, nil
}
