package domain

import (
	"time"

	"github.com/google/uuid"
)

// TripPosition is a recorded point of a trip's route.
type TripPosition struct {
	ID          int64
	TripID      uuid.UUID
	Coordinates Coordinates
	RecordedAt  time.Time
}

// CreatePosition is the client-provided location and its device timestamp.
type CreatePosition struct {
	Coordinates Coordinates
	RecordedAt  time.Time
}

// Validate accepts historical samples, but rejects a missing timestamp or one
// more than one minute ahead of the server clock.
func (input CreatePosition) Validate(now time.Time) error {
	if err := input.Coordinates.validate("coordinates"); err != nil {
		return err
	}
	if input.RecordedAt.IsZero() {
		return &FieldError{Field: "recorded_at", Reason: "must be provided"}
	}
	if input.RecordedAt.After(now.Add(time.Minute)) {
		return &FieldError{Field: "recorded_at", Reason: "must not be more than one minute in the future"}
	}
	return nil
}
