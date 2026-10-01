package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
)

type fakeService struct {
	created  domain.Trip
	got      domain.Trip
	finished domain.Trip
	err      error
}

func (s *fakeService) Create(_ context.Context, _ domain.CreateTrip) (domain.Trip, error) {
	return s.created, s.err
}

func (s *fakeService) Get(_ context.Context, _ uuid.UUID) (domain.Trip, error) {
	return s.got, s.err
}

func (s *fakeService) Finish(_ context.Context, _ uuid.UUID) (domain.Trip, error) {
	return s.finished, s.err
}

type fakePinger struct{ err error }

func (p fakePinger) Ping(context.Context) error { return p.err }

func TestCreateTrip(t *testing.T) {
	trip := sampleTrip()
	router := testRouter(&fakeService{created: trip}, fakePinger{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(`{
		"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c",
		"driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5",
		"start_point":{"latitude":59.9398,"longitude":30.3146},
		"end_point":{"latitude":59.929,"longitude":30.3626},
		"price":1450
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Location"); got != "/api/v1/trips/"+trip.ID.String() {
		t.Fatalf("Location = %q", got)
	}
}

func TestCreateTripRejectsUnknownAndMissingFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown": `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":1,"longitude":2},"end_point":{"latitude":3,"longitude":4},"price":10,"surprise":true}`,
		"missing": `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":1,"longitude":2},"end_point":{"latitude":3,"longitude":4}}`,
	} {
		t.Run(name, func(t *testing.T) {
			router := testRouter(&fakeService{}, fakePinger{})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body)))
			assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestGeneratedRouterRejectsInvalidTripID(t *testing.T) {
	router := testRouter(&fakeService{}, fakePinger{})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/trips/not-a-uuid", nil))
	assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
}

func TestDomainErrorsUseProblemJSON(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrDriverBusy, http.StatusConflict, "driver_busy"},
		{domain.ErrTripNotFound, http.StatusNotFound, "trip_not_found"},
		{domain.ErrTripCompleted, http.StatusConflict, "trip_completed"},
		{errors.New("database unavailable"), http.StatusInternalServerError, "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			router := testRouter(&fakeService{err: test.err}, fakePinger{})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/trips/1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111", nil))
			assertProblem(t, recorder, test.status, test.code)
		})
	}
}

func TestHealthAndReady(t *testing.T) {
	router := testRouter(&fakeService{}, fakePinger{err: errors.New("down")})

	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	ready := httptest.NewRecorder()
	router.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d", ready.Code)
	}
	var response api.HealthResponse
	if err := json.Unmarshal(ready.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != api.Unavailable {
		t.Fatalf("ready response = %#v", response)
	}
}

func testRouter(service *fakeService, pinger fakePinger) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(NewHandler(service, pinger, time.Second, logger))
}

func assertProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var problem api.Problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != code {
		t.Fatalf("problem code = %q", problem.Code)
	}
}

func sampleTrip() domain.Trip {
	return domain.Trip{
		ID:         uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111"),
		UserID:     uuid.MustParse("5cb72c04-7650-45c9-a79b-bcdba0631e0c"),
		DriverID:   uuid.MustParse("8860b315-ec86-42eb-a17c-7c163d721ff5"),
		StartPoint: domain.Coordinates{Latitude: 59.9398, Longitude: 30.3146},
		EndPoint:   domain.Coordinates{Latitude: 59.929, Longitude: 30.3626},
		Price:      1450,
		Status:     domain.TripStatusActive,
		StartedAt:  time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
	}
}
