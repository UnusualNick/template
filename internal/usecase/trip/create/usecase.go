package create

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
	newID      func() uuid.UUID
	onCreated  func(context.Context)
}

// New creates the trip creation action. Nil clock, ID generator, and hook use
// production defaults (the hook is omitted).
func New(repository tripRepository, txManager txManager, now func() time.Time, newID func() uuid.UUID, onCreated func(context.Context)) *UseCase {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = uuid.New
	}
	return &UseCase{repository: repository, txManager: txManager, now: now, newID: newID, onCreated: onCreated}
}

// Execute validates the request and persists the trip and its initial status
// history together. The success hook runs only after Do commits successfully.
func (u *UseCase) Execute(ctx context.Context, input domain.CreateTrip) (domain.Trip, error) {
	if err := input.Validate(); err != nil {
		return domain.Trip{}, fmt.Errorf("create trip: %w", err)
	}

	trip := domain.Trip{
		ID:         u.newID(),
		UserID:     input.UserID,
		DriverID:   input.DriverID,
		StartPoint: input.StartPoint,
		EndPoint:   input.EndPoint,
		Price:      input.Price,
		Status:     domain.TripStatusActive,
		StartedAt:  u.now().UTC(),
	}

	if err := u.txManager.Do(ctx, func(txCtx context.Context) error {
		if err := u.repository.Insert(txCtx, trip); err != nil {
			return err
		}
		return u.repository.InsertStatusHistory(txCtx, trip.ID, nil, domain.TripStatusActive, "trip created")
	}); err != nil {
		return domain.Trip{}, fmt.Errorf("create trip: %w", err)
	}

	if u.onCreated != nil {
		u.onCreated(ctx)
	}
	return trip, nil
}
