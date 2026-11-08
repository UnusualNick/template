package save

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type UseCase struct {
	repository positionRepository
	txManager  txManager
	clock      func() time.Time
	onSaved    func(context.Context, uuid.UUID)
}

// New builds the position-saving action. Nil clock uses the system clock;
// onSaved, if provided, runs only after the transaction commits successfully.
func New(repository positionRepository, txManager txManager, clock func() time.Time, onSaved func(context.Context, uuid.UUID)) *UseCase {
	if clock == nil {
		clock = time.Now
	}
	return &UseCase{repository: repository, txManager: txManager, clock: clock, onSaved: onSaved}
}

func (u *UseCase) Execute(ctx context.Context, tripID uuid.UUID, input domain.CreatePosition) (domain.TripPosition, error) {
	if tripID == uuid.Nil {
		return domain.TripPosition{}, fmt.Errorf("save trip position: %w", &domain.FieldError{Field: "trip_id", Reason: "must be a non-zero UUID"})
	}
	if err := input.Validate(u.clock()); err != nil {
		return domain.TripPosition{}, fmt.Errorf("save trip position: %w", err)
	}

	position := domain.TripPosition{
		TripID:      tripID,
		Coordinates: input.Coordinates,
		RecordedAt:  input.RecordedAt.UTC().Truncate(time.Microsecond),
	}
	var saved domain.TripPosition
	if err := u.txManager.Do(ctx, func(txCtx context.Context) error {
		// The active-only update locks the trip row and serializes against Finish.
		// An older sample must never move last_position_at backwards.
		if err := u.repository.UpdateLastPositionAt(txCtx, tripID, position.RecordedAt); err != nil {
			return err
		}
		var err error
		saved, err = u.repository.InsertPosition(txCtx, position)
		return err
	}); err != nil {
		return domain.TripPosition{}, fmt.Errorf("save trip position: %w", err)
	}
	if u.onSaved != nil {
		u.onSaved(ctx, tripID)
	}
	return saved, nil
}
