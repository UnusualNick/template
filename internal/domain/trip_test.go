package domain

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validInput() CreateTrip {
	return CreateTrip{
		UserID: uuid.New(), DriverID: uuid.New(),
		StartPoint: Coordinates{Latitude: -90, Longitude: 180},
		EndPoint:   Coordinates{Latitude: 90, Longitude: -180},
		Price:      0,
	}
}

func TestCreateTripValidate(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*CreateTrip)
		field string
	}{
		{"valid boundary coordinates and zero price", func(*CreateTrip) {}, ""},
		{"missing user", func(i *CreateTrip) { i.UserID = uuid.Nil }, "user_id"},
		{"missing driver", func(i *CreateTrip) { i.DriverID = uuid.Nil }, "driver_id"},
		{"start latitude too small", func(i *CreateTrip) { i.StartPoint.Latitude = -90.01 }, "start_point.latitude"},
		{"start latitude NaN", func(i *CreateTrip) { i.StartPoint.Latitude = math.NaN() }, "start_point.latitude"},
		{"start longitude infinite", func(i *CreateTrip) { i.StartPoint.Longitude = math.Inf(1) }, "start_point.longitude"},
		{"end latitude infinite", func(i *CreateTrip) { i.EndPoint.Latitude = math.Inf(-1) }, "end_point.latitude"},
		{"end latitude too large", func(i *CreateTrip) { i.EndPoint.Latitude = 90.01 }, "end_point.latitude"},
		{"end longitude too small", func(i *CreateTrip) { i.EndPoint.Longitude = -180.01 }, "end_point.longitude"},
		{"end longitude NaN", func(i *CreateTrip) { i.EndPoint.Longitude = math.NaN() }, "end_point.longitude"},
		{"negative price", func(i *CreateTrip) { i.Price = -1 }, "price"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validInput()
			tt.alter(&input)
			err := input.Validate()
			if tt.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			var fieldErr *FieldError
			if !errors.Is(err, ErrInvalidInput) || !errors.As(err, &fieldErr) || fieldErr.Field != tt.field {
				t.Fatalf("Validate() = %v; want field %q and ErrInvalidInput", err, tt.field)
			}
		})
	}
}

func TestTripFinish(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	old := at.Add(-time.Hour)
	for _, tt := range []struct {
		name    string
		status  TripStatus
		wantErr error
	}{
		{"active", TripStatusActive, nil},
		{"completed", TripStatusCompleted, ErrTripCompleted},
		{"unknown", TripStatus("unknown"), ErrInvalidInput},
	} {
		t.Run(tt.name, func(t *testing.T) {
			trip := Trip{Status: tt.status, FinishedAt: &old}
			err := trip.Finish(at)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Finish() = %v; want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if trip.Status != tt.status || trip.FinishedAt != &old {
					t.Fatalf("rejected finish changed trip: %+v", trip)
				}
				return
			}
			if trip.Status != TripStatusCompleted || trip.FinishedAt == nil || !trip.FinishedAt.Equal(at) {
				t.Fatalf("finished trip = %+v", trip)
			}
			if err := trip.Finish(at); !errors.Is(err, ErrTripCompleted) {
				t.Fatalf("repeated Finish() = %v", err)
			}
		})
	}
}
