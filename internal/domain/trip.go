package domain

import (
	"errors"
	"fmt"
	"math"
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

// FieldError identifies the first invalid input field while retaining the
// ErrInvalidInput classification used by callers.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	return fmt.Sprintf("%s: %s %s", ErrInvalidInput, e.Field, e.Reason)
}

func (e *FieldError) Unwrap() error { return ErrInvalidInput }

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

// Validate checks input in a stable field order and rejects non-finite coordinates.
func (input CreateTrip) Validate() error {
	if input.UserID == uuid.Nil {
		return &FieldError{Field: "user_id", Reason: "must be a non-zero UUID"}
	}
	if input.DriverID == uuid.Nil {
		return &FieldError{Field: "driver_id", Reason: "must be a non-zero UUID"}
	}
	if err := input.StartPoint.validate("start_point"); err != nil {
		return err
	}
	if err := input.EndPoint.validate("end_point"); err != nil {
		return err
	}
	if input.Price < 0 {
		return &FieldError{Field: "price", Reason: "must not be negative"}
	}
	return nil
}

func (point Coordinates) validate(name string) error {
	if !finiteInRange(point.Latitude, -90, 90) {
		return &FieldError{Field: name + ".latitude", Reason: "must be between -90 and 90"}
	}
	if !finiteInRange(point.Longitude, -180, 180) {
		return &FieldError{Field: name + ".longitude", Reason: "must be between -180 and 180"}
	}
	return nil
}

func finiteInRange(value, min, max float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= min && value <= max
}

// Finish moves an active trip to completed without changing a rejected trip.
func (trip *Trip) Finish(at time.Time) error {
	if trip.Status == TripStatusCompleted {
		return ErrTripCompleted
	}
	if trip.Status != TripStatusActive {
		return &FieldError{Field: "status", Reason: "must be active"}
	}
	trip.Status = TripStatusCompleted
	trip.FinishedAt = &at
	return nil
}
