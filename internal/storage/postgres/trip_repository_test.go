package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type timestampRow struct {
	started  time.Time
	finished *time.Time
	last     *time.Time
}

func (r timestampRow) Scan(dest ...any) error {
	*dest[0].(*uuid.UUID) = uuid.New()
	*dest[1].(*uuid.UUID) = uuid.New()
	*dest[2].(*uuid.UUID) = uuid.New()
	*dest[3].(*float64) = 59.9
	*dest[4].(*float64) = 30.3
	*dest[5].(*float64) = 59.8
	*dest[6].(*float64) = 30.4
	*dest[7].(*int64) = 1450
	*dest[8].(*domain.TripStatus) = domain.TripStatusActive
	*dest[9].(*time.Time) = r.started
	*dest[10].(**time.Time) = r.finished
	*dest[11].(**time.Time) = r.last
	return nil
}

func TestScanTripNormalizesTimestampsToUTCMicroseconds(t *testing.T) {
	started := time.Date(2026, 11, 1, 18, 12, 22, 768139851, time.FixedZone("MSK", 3*60*60))
	finished := started.Add(20 * time.Minute)
	last := started.Add(10 * time.Minute)
	trip, err := scanTrip(timestampRow{started: started, finished: &finished, last: &last})
	if err != nil {
		t.Fatalf("scanTrip() error = %v", err)
	}
	for _, tc := range []struct {
		name string
		got  *time.Time
		want time.Time
	}{
		{"started", &trip.StartedAt, started},
		{"finished", trip.FinishedAt, finished},
		{"last position", trip.LastPositionAt, last},
	} {
		if tc.got == nil || !tc.got.Equal(tc.want.UTC().Truncate(time.Microsecond)) || tc.got.Location() != time.UTC {
			t.Errorf("%s timestamp = %v, want UTC microseconds for %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestScanTripKeepsNullableTimestampsEmpty(t *testing.T) {
	trip, err := scanTrip(timestampRow{started: time.Now()})
	if err != nil {
		t.Fatalf("scanTrip() error = %v", err)
	}
	if trip.FinishedAt != nil || trip.LastPositionAt != nil {
		t.Fatalf("unexpected nullable timestamps: %#v", trip)
	}
}
