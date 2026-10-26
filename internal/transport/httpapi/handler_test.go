package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/observability"
)

type fakeService struct {
	created     domain.Trip
	got         domain.Trip
	finished    domain.Trip
	err         error
	createCalls int
}

func (s *fakeService) Create(_ context.Context, _ domain.CreateTrip) (domain.Trip, error) {
	s.createCalls++
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

func TestCreateTripBodyValidation(t *testing.T) {
	valid := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":1,"longitude":2},"end_point":{"latitude":3,"longitude":4},"price":10}`
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"valid zero coordinates", strings.ReplaceAll(strings.ReplaceAll(valid, `"latitude":1,"longitude":2`, `"latitude":0,"longitude":0`), `"latitude":3,"longitude":4`, `"latitude":0,"longitude":0`), http.StatusCreated},
		{"unknown top-level field", strings.Replace(valid, `"price":10`, `"price":10,"surprise":true`, 1), http.StatusBadRequest},
		{"missing price", strings.Replace(valid, `,"price":10`, ``, 1), http.StatusBadRequest},
		{"missing start latitude", strings.Replace(valid, `"latitude":1,`, ``, 1), http.StatusBadRequest},
		{"missing start longitude", strings.Replace(valid, `,"longitude":2`, ``, 1), http.StatusBadRequest},
		{"missing end latitude", strings.Replace(valid, `"latitude":3,`, ``, 1), http.StatusBadRequest},
		{"missing end longitude", strings.Replace(valid, `,"longitude":4`, ``, 1), http.StatusBadRequest},
		{"null start latitude", strings.Replace(valid, `"latitude":1`, `"latitude":null`, 1), http.StatusBadRequest},
		{"null end longitude", strings.Replace(valid, `"longitude":4`, `"longitude":null`, 1), http.StatusBadRequest},
		{"null start point", strings.Replace(valid, `"start_point":{"latitude":1,"longitude":2}`, `"start_point":null`, 1), http.StatusBadRequest},
		{"wrong end point type", strings.Replace(valid, `"end_point":{"latitude":3,"longitude":4}`, `"end_point":[]`, 1), http.StatusBadRequest},
		{"unknown nested field", strings.Replace(valid, `"latitude":1`, `"latitude":1,"altitude":10`, 1), http.StatusBadRequest},
		{"malformed JSON", `{`, http.StatusBadRequest},
		{"non-object JSON", `[]`, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{created: sampleTrip()}
			recorder := httptest.NewRecorder()
			testRouter(service, fakePinger{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(test.body)))
			if test.status == http.StatusCreated {
				if recorder.Code != test.status || service.createCalls != 1 {
					t.Fatalf("status = %d, create calls = %d, body = %s", recorder.Code, service.createCalls, recorder.Body.String())
				}
				return
			}
			assertProblem(t, recorder, test.status, "invalid_request")
			if service.createCalls != 0 {
				t.Fatalf("invalid body called service %d times", service.createCalls)
			}
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
		name   string
		err    error
		status int
		code   string
		detail string
	}{
		{"driver busy", fmt.Errorf("wrapped: %w", domain.ErrDriverBusy), http.StatusConflict, "driver_busy", "Driver already has an active trip"},
		{"trip not found", fmt.Errorf("wrapped: %w", domain.ErrTripNotFound), http.StatusNotFound, "trip_not_found", "Trip was not found"},
		{"trip completed", fmt.Errorf("wrapped: %w", domain.ErrTripCompleted), http.StatusConflict, "trip_completed", "Operation is not allowed for a completed trip"},
		{"invalid input", fmt.Errorf("secret data: %w", domain.ErrInvalidInput), http.StatusBadRequest, "invalid_request", "Invalid trip input"},
		{"field error", fmt.Errorf("secret data: %w", &domain.FieldError{Field: "price", Reason: "must not be negative"}), http.StatusBadRequest, "invalid_request", "Invalid trip input"},
		{"unknown failure", errors.New("secret database credentials"), http.StatusInternalServerError, "internal_error", "An internal error occurred"},
	}
	paths := []struct {
		name, method, path string
	}{
		{"create", http.MethodPost, "/api/v1/trips"},
		{"get", http.MethodGet, "/api/v1/trips/1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111"},
		{"finish", http.MethodPost, "/api/v1/trips/1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111/finish"},
	}
	body := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":1,"longitude":2},"end_point":{"latitude":3,"longitude":4},"price":10}`
	for _, test := range tests {
		for _, endpoint := range paths {
			t.Run(test.name+"/"+endpoint.name, func(t *testing.T) {
				router := testRouter(&fakeService{err: test.err}, fakePinger{})
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(body)))
				problem := assertProblem(t, recorder, test.status, test.code)
				if problem.Detail == nil || *problem.Detail != test.detail {
					t.Fatalf("detail = %v, want %q", problem.Detail, test.detail)
				}
				if problem.Instance == nil || *problem.Instance != endpoint.path {
					t.Fatalf("instance = %v, want %q", problem.Instance, endpoint.path)
				}
				if strings.Contains(recorder.Body.String(), "secret") {
					t.Fatalf("response leaked private error: %s", recorder.Body.String())
				}
			})
		}
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

func TestGeneratedRoutesGiveActiveMetricTemplate(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(context.Background()) })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouter(NewHandler(&fakeService{got: sampleTrip()}, fakePinger{}, time.Second, logger), observability.HTTPMiddleware(logger))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/trips/1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, scope := range metrics.ScopeMetrics {
		for _, value := range scope.Metrics {
			if value.Name != "http.server.active_requests" {
				continue
			}
			for _, point := range value.Data.(metricdata.Sum[int64]).DataPoints {
				for _, attr := range point.Attributes.ToSlice() {
					if attr.Key == "http.route" && attr.Value.AsString() == "/api/v1/trips/{tripId}" && point.Value == 0 {
						return
					}
				}
			}
		}
	}
	t.Fatal("no balanced active-request metric with generated route template")
}

func testRouter(service *fakeService, pinger fakePinger) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRouter(NewHandler(service, pinger, time.Second, logger))
}

func assertProblem(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) api.Problem {
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
	if problem.Code != code || problem.Status != int32(status) {
		t.Fatalf("problem = %#v, want code %q and status %d", problem, code, status)
	}
	return problem
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
