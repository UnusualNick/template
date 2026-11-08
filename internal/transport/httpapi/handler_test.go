package httpapi

import (
	"bytes"
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
	created     domain.Trip
	got         domain.Trip
	finished    domain.Trip
	err         error
	createCalls int
	createInput domain.CreateTrip
}

func (s *fakeService) Create(_ context.Context, input domain.CreateTrip) (domain.Trip, error) {
	s.createCalls++
	s.createInput = input
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
			service := &fakeService{}
			router := testRouter(service, fakePinger{})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
			if problem.Detail == nil || *problem.Detail == "Request body is not valid JSON" {
				t.Fatalf("unhelpful detail: %v", problem.Detail)
			}
			if service.createCalls != 0 {
				t.Fatalf("service Create called for invalid JSON")
			}
		})
	}
}

func TestCreateTripDecodeErrorsHaveSafeDetails(t *testing.T) {
	valid := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":1,"longitude":2},"end_point":{"latitude":3,"longitude":4},"price":10}`
	for _, test := range []struct{ name, body, detail string }{
		{"missing price", strings.TrimSuffix(valid, `,"price":10}`) + `}`, `required field "price" is missing`},
		{"unknown field", strings.TrimSuffix(valid, `}`) + `,"surprise":true}`, "request body contains an unknown field"},
		{"malformed JSON", `{`, "request body must be valid JSON"},
		{"JSON array", `[]`, "request body must be a JSON object"},
		{"trailing JSON", valid + `{}`, "request body must contain exactly one JSON object"},
		{"trailing object", valid + ` {}`, "request body must contain exactly one JSON object"},
		{"wrong type", strings.Replace(valid, `"price":10`, `"price":"secret"`, 1), "request body contains a field with the wrong type"},
		{"invalid UUID", strings.Replace(valid, `5cb72c04-7650-45c9-a79b-bcdba0631e0c`, `secret-id`, 1), "request body contains an invalid field value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{created: sampleTrip()}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			testRouter(service, fakePinger{}).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
			if problem.Detail == nil || *problem.Detail != test.detail {
				t.Fatalf("detail = %v; expected %q", problem.Detail, test.detail)
			}
			if strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("request value leaked: %s", recorder.Body.String())
			}
			if service.createCalls != 0 {
				t.Fatal("service Create called for invalid body")
			}
		})
	}
}

func TestCreateTripRequiresNestedCoordinates(t *testing.T) {
	for _, test := range []struct {
		name, start, end, missing string
	}{
		{"start latitude missing", `{"longitude":2}`, `{"latitude":3,"longitude":4}`, "start_point.latitude"},
		{"start longitude missing", `{"latitude":1}`, `{"latitude":3,"longitude":4}`, "start_point.longitude"},
		{"end latitude missing", `{"latitude":1,"longitude":2}`, `{"longitude":4}`, "end_point.latitude"},
		{"end longitude missing", `{"latitude":1,"longitude":2}`, `{"latitude":3}`, "end_point.longitude"},
		{"start latitude null", `{"latitude":null,"longitude":2}`, `{"latitude":3,"longitude":4}`, "start_point.latitude"},
		{"start longitude null", `{"latitude":1,"longitude":null}`, `{"latitude":3,"longitude":4}`, "start_point.longitude"},
		{"end latitude null", `{"latitude":1,"longitude":2}`, `{"latitude":null,"longitude":4}`, "end_point.latitude"},
		{"end longitude null", `{"latitude":1,"longitude":2}`, `{"latitude":3,"longitude":null}`, "end_point.longitude"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeService{created: sampleTrip()}
			body := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":` + test.start + `,"end_point":` + test.end + `,"price":10}`
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			testRouter(service, fakePinger{}).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
			if problem.Detail == nil || !strings.Contains(*problem.Detail, test.missing) {
				t.Fatalf("detail = %v, want field %q", problem.Detail, test.missing)
			}
			if service.createCalls != 0 {
				t.Fatal("service Create called for missing or null coordinate")
			}
		})
	}
}

func TestCreateTripRejectsNonObjectCoordinatesAndOversizedBody(t *testing.T) {
	for _, point := range []string{"start_point", "end_point"} {
		t.Run(point+" null", func(t *testing.T) {
			body := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":1,"longitude":2},"end_point":{"latitude":3,"longitude":4},"price":10}`
			if point == "start_point" {
				body = strings.Replace(body, `"start_point":{"latitude":1,"longitude":2}`, `"start_point":null`, 1)
			} else {
				body = strings.Replace(body, `"end_point":{"latitude":3,"longitude":4}`, `"end_point":null`, 1)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			testRouter(&fakeService{}, fakePinger{}).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
			if problem.Detail == nil || !strings.Contains(*problem.Detail, point) {
				t.Fatalf("detail = %v", problem.Detail)
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(strings.Repeat(" ", maxRequestBodyBytes+1)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	testRouter(&fakeService{}, fakePinger{}).ServeHTTP(recorder, request)
	problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
	if problem.Detail == nil || *problem.Detail != "request body exceeds the size limit" {
		t.Fatalf("detail = %v", problem.Detail)
	}
}

func TestCreateTripAcceptsZeroCoordinatesAndJSONCharset(t *testing.T) {
	service := &fakeService{created: sampleTrip()}
	body := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":0,"longitude":0},"end_point":{"latitude":0,"longitude":0},"price":0}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	recorder := httptest.NewRecorder()
	testRouter(service, fakePinger{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || service.createCalls != 1 || service.createInput.StartPoint != (domain.Coordinates{}) || service.createInput.EndPoint != (domain.Coordinates{}) {
		t.Fatalf("status = %d, createCalls = %d, input = %#v", recorder.Code, service.createCalls, service.createInput)
	}
}

func TestCreateTripRejectsUnsupportedContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/problem+json", "application/json; charset=bogus"} {
		t.Run(contentType, func(t *testing.T) {
			service := &fakeService{created: sampleTrip()}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", contentType)
			recorder := httptest.NewRecorder()
			testRouter(service, fakePinger{}).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusUnsupportedMediaType, "invalid_request")
			if problem.Detail == nil || *problem.Detail != "Content-Type must be application/json" {
				t.Fatalf("detail = %v", problem.Detail)
			}
			if service.createCalls != 0 {
				t.Fatal("service Create called for unsupported Content-Type")
			}
		})
	}
}

func TestGeneratedRouterRejectsInvalidTripID(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	router := NewRouter(NewHandler(&fakeService{}, fakePinger{}, time.Second, logger))
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/trips/not-a-uuid"},
		{http.MethodPost, "/api/v1/trips/not-a-uuid/finish"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
		if problem.Detail == nil || *problem.Detail != "tripId must be a UUID" {
			t.Fatalf("detail = %v", problem.Detail)
		}
		if strings.Contains(recorder.Body.String(), "unmarshaling") || strings.Contains(recorder.Body.String(), "*uuid.UUID") {
			t.Fatalf("generated parser leaked into response: %s", recorder.Body.String())
		}
	}
	if !strings.Contains(logs.String(), "Invalid format for parameter tripId") {
		t.Fatalf("original parser error absent from debug logs: %s", logs.String())
	}
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
	if problem.Code != code {
		t.Fatalf("problem code = %q", problem.Code)
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
