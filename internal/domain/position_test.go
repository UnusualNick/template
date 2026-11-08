package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestCreatePositionValidate(t *testing.T) {
	const (
		latitudeField  = "coordinates.latitude"
		longitudeField = "coordinates.longitude"
	)
	now := time.Date(2026, 11, 9, 10, 11, 12, 0, time.UTC)
	valid := CreatePosition{Coordinates: Coordinates{Latitude: -90, Longitude: 180}, RecordedAt: now}
	cases := []struct {
		name  string
		alter func(*CreatePosition)
		field string
	}{
		{"valid boundaries", func(*CreatePosition) {}, ""},
		{"historic sample", func(p *CreatePosition) { p.RecordedAt = now.Add(-100 * 365 * 24 * time.Hour) }, ""},
		{"exactly one minute ahead", func(p *CreatePosition) { p.RecordedAt = now.Add(time.Minute) }, ""},
		{"one nanosecond too far ahead", func(p *CreatePosition) { p.RecordedAt = now.Add(time.Minute + time.Nanosecond) }, "recorded_at"},
		{"missing time", func(p *CreatePosition) { p.RecordedAt = time.Time{} }, "recorded_at"},
		{"latitude under limit", func(p *CreatePosition) { p.Coordinates.Latitude = -90.00001 }, latitudeField},
		{"latitude above limit", func(p *CreatePosition) { p.Coordinates.Latitude = 90.00001 }, latitudeField},
		{"latitude NaN", func(p *CreatePosition) { p.Coordinates.Latitude = math.NaN() }, latitudeField},
		{"latitude infinite", func(p *CreatePosition) { p.Coordinates.Latitude = math.Inf(-1) }, latitudeField},
		{"longitude below limit", func(p *CreatePosition) { p.Coordinates.Longitude = -180.00001 }, longitudeField},
		{"longitude above limit", func(p *CreatePosition) { p.Coordinates.Longitude = 180.00001 }, longitudeField},
		{"longitude NaN", func(p *CreatePosition) { p.Coordinates.Longitude = math.NaN() }, longitudeField},
		{"longitude infinite", func(p *CreatePosition) { p.Coordinates.Longitude = math.Inf(1) }, longitudeField},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.alter(&input)
			err := input.Validate(now)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var fieldErr *FieldError
			if !errors.Is(err, ErrInvalidInput) || !errors.As(err, &fieldErr) || fieldErr.Field != tc.field {
				t.Fatalf("Validate() = %v, want ErrInvalidInput field %q", err, tc.field)
			}
		})
	}
}
