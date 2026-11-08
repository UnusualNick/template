package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type TripStatus string

const (
	TripStatusActive    TripStatus = "active"
	TripStatusCompleted TripStatus = "completed"
)

var (
	ErrInvalidInput  = errors.New("invalid input")
	ErrDriverBusy    = errors.New("driver already has an active trip")
	ErrTripNotFound  = errors.New("trip not found")
	ErrTripCompleted = errors.New("trip already completed")
)

type Coordinates struct {
	Latitude  float64
	Longitude float64
}

type Trip struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	DriverID       uuid.UUID
	StartPoint     Coordinates
	EndPoint       Coordinates
	Price          int64
	Status         TripStatus
	StartedAt      time.Time
	FinishedAt     *time.Time
	LastPositionAt *time.Time
}

type CreateTrip struct {
	UserID     uuid.UUID
	DriverID   uuid.UUID
	StartPoint Coordinates
	EndPoint   Coordinates
	Price      int64
}
