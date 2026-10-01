package service

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type txManager interface {
	Do(ctx context.Context, fn func(context.Context) error) error
}

type tripRepository interface {
	Insert(ctx context.Context, trip domain.Trip) error
	InsertStatusHistory(
		ctx context.Context,
		tripID uuid.UUID,
		fromStatus *domain.TripStatus,
		toStatus domain.TripStatus,
		reason string,
	) error
	GetByID(ctx context.Context, id uuid.UUID) (domain.Trip, error)
	Finish(ctx context.Context, id uuid.UUID, finishedAt time.Time) (domain.Trip, error)
}

type TripService struct {
	repository tripRepository
	txManager  txManager
	now        func() time.Time
	newID      func() uuid.UUID
}

func NewTripService(repository tripRepository, txManager txManager) *TripService {
	return &TripService{
		repository: repository,
		txManager:  txManager,
		now:        time.Now,
		newID:      uuid.New,
	}
}

func (s *TripService) Create(ctx context.Context, input domain.CreateTrip) (domain.Trip, error) {
	if err := validateCreate(input); err != nil {
		return domain.Trip{}, err
	}

	now := s.now().UTC()
	trip := domain.Trip{
		ID:         s.newID(),
		UserID:     input.UserID,
		DriverID:   input.DriverID,
		StartPoint: input.StartPoint,
		EndPoint:   input.EndPoint,
		Price:      input.Price,
		Status:     domain.TripStatusActive,
		StartedAt:  now,
	}

	if err := s.txManager.Do(ctx, func(txCtx context.Context) error {
		if err := s.repository.Insert(txCtx, trip); err != nil {
			return err
		}
		if err := s.repository.InsertStatusHistory(
			txCtx,
			trip.ID,
			nil,
			domain.TripStatusActive,
			"trip created",
		); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return domain.Trip{}, fmt.Errorf("create trip: %w", err)
	}

	return trip, nil
}

func (s *TripService) Get(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	return s.repository.GetByID(ctx, id)
}

func (s *TripService) Finish(ctx context.Context, id uuid.UUID) (domain.Trip, error) {
	var trip domain.Trip
	if err := s.txManager.Do(ctx, func(txCtx context.Context) error {
		var err error
		trip, err = s.repository.Finish(txCtx, id, s.now().UTC())
		if err != nil {
			return err
		}
		fromStatus := domain.TripStatusActive
		return s.repository.InsertStatusHistory(
			txCtx,
			trip.ID,
			&fromStatus,
			domain.TripStatusCompleted,
			"trip finished",
		)
	}); err != nil {
		return domain.Trip{}, fmt.Errorf("finish trip: %w", err)
	}
	return trip, nil
}

func validateCreate(input domain.CreateTrip) error {
	if input.UserID == uuid.Nil {
		return fmt.Errorf("%w: user_id must be a non-zero UUID", domain.ErrInvalidInput)
	}
	if input.DriverID == uuid.Nil {
		return fmt.Errorf("%w: driver_id must be a non-zero UUID", domain.ErrInvalidInput)
	}
	if err := validateCoordinates("start_point", input.StartPoint); err != nil {
		return err
	}
	if err := validateCoordinates("end_point", input.EndPoint); err != nil {
		return err
	}
	if input.Price < 0 {
		return fmt.Errorf("%w: price must not be negative", domain.ErrInvalidInput)
	}
	return nil
}

func validateCoordinates(name string, point domain.Coordinates) error {
	if math.IsNaN(point.Latitude) || math.IsInf(point.Latitude, 0) || point.Latitude < -90 || point.Latitude > 90 {
		return fmt.Errorf("%w: %s.latitude must be between -90 and 90", domain.ErrInvalidInput, name)
	}
	if math.IsNaN(point.Longitude) || math.IsInf(point.Longitude, 0) || point.Longitude < -180 || point.Longitude > 180 {
		return fmt.Errorf("%w: %s.longitude must be between -180 and 180", domain.ErrInvalidInput, name)
	}
	return nil
}
