package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/internal/domain"
)

type fakeTxManager struct {
	calls int
}

func (m *fakeTxManager) Do(ctx context.Context, fn func(context.Context) error) error {
	m.calls++
	return fn(ctx)
}

type fakeTripRepository struct {
	insertedTrip *domain.Trip
	historyCalls int
	historyError error
	finishResult domain.Trip
	finishError  error
	finishAt     time.Time
	getResult    domain.Trip
	getError     error
}

func (r *fakeTripRepository) Insert(_ context.Context, trip domain.Trip) error {
	r.insertedTrip = &trip
	return nil
}

func (r *fakeTripRepository) InsertStatusHistory(
	_ context.Context,
	_ uuid.UUID,
	_ *domain.TripStatus,
	_ domain.TripStatus,
	_ string,
) error {
	r.historyCalls++
	return r.historyError
}

func (r *fakeTripRepository) GetByID(_ context.Context, _ uuid.UUID) (domain.Trip, error) {
	return r.getResult, r.getError
}

func (r *fakeTripRepository) Finish(_ context.Context, _ uuid.UUID, finishedAt time.Time) (domain.Trip, error) {
	r.finishAt = finishedAt
	return r.finishResult, r.finishError
}

func TestCreateWritesTripAndHistoryInOneTransaction(t *testing.T) {
	repository := &fakeTripRepository{}
	txManager := &fakeTxManager{}
	service := NewTripService(repository, txManager)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	id := uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111")
	service.now = func() time.Time { return now }
	service.newID = func() uuid.UUID { return id }

	trip, err := service.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if txManager.calls != 1 {
		t.Fatalf("transaction calls = %d", txManager.calls)
	}
	if repository.insertedTrip == nil || repository.insertedTrip.ID != id {
		t.Fatalf("inserted trip = %#v", repository.insertedTrip)
	}
	if repository.historyCalls != 1 {
		t.Fatalf("history calls = %d", repository.historyCalls)
	}
	if trip.Status != domain.TripStatusActive || !trip.StartedAt.Equal(now) {
		t.Fatalf("created trip = %#v", trip)
	}
}

func TestTripTimestampsUseDatabasePrecisionAndUTC(t *testing.T) {
	clock := time.Date(2026, 11, 1, 18, 12, 22, 768139851, time.FixedZone("MSK", 3*60*60))
	want := clock.UTC().Truncate(time.Microsecond)
	repository := &fakeTripRepository{finishResult: domain.Trip{ID: uuid.New()}}
	service := NewTripService(repository, &fakeTxManager{})
	service.now = func() time.Time { return clock }

	created, err := service.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !created.StartedAt.Equal(want) || created.StartedAt.Location() != time.UTC ||
		repository.insertedTrip == nil || !repository.insertedTrip.StartedAt.Equal(want) {
		t.Fatalf("created timestamp = %s, want %s", created.StartedAt, want)
	}

	if _, err := service.Finish(context.Background(), repository.finishResult.ID); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if !repository.finishAt.Equal(want) || repository.finishAt.Location() != time.UTC {
		t.Fatalf("finished timestamp = %s, want %s", repository.finishAt, want)
	}
}

func TestCreatePropagatesHistoryFailure(t *testing.T) {
	repository := &fakeTripRepository{historyError: errors.New("history unavailable")}
	service := NewTripService(repository, &fakeTxManager{})

	if _, err := service.Create(context.Background(), validCreateInput()); err == nil {
		t.Fatal("Create() succeeded when history insert failed")
	}
}

func TestCreateValidatesInputBeforeTransaction(t *testing.T) {
	txManager := &fakeTxManager{}
	service := NewTripService(&fakeTripRepository{}, txManager)
	input := validCreateInput()
	input.StartPoint.Latitude = 91

	_, err := service.Create(context.Background(), input)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("Create() error = %v", err)
	}
	if txManager.calls != 0 {
		t.Fatalf("transaction calls = %d", txManager.calls)
	}
}

func validCreateInput() domain.CreateTrip {
	return domain.CreateTrip{
		UserID:   uuid.MustParse("5cb72c04-7650-45c9-a79b-bcdba0631e0c"),
		DriverID: uuid.MustParse("8860b315-ec86-42eb-a17c-7c163d721ff5"),
		StartPoint: domain.Coordinates{
			Latitude:  59.9398,
			Longitude: 30.3146,
		},
		EndPoint: domain.Coordinates{
			Latitude:  59.929,
			Longitude: 30.3626,
		},
		Price: 1450,
	}
}
