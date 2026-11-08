package httpapi

import (
	"bytes"
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
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/observability"
)

func testService(t *testing.T) *MockTripService {
	t.Helper()
	return NewMockTripService(gomock.NewController(t))
}

func testPinger(t *testing.T) *MockDatabasePinger {
	t.Helper()
	return NewMockDatabasePinger(gomock.NewController(t))
}

func TestCreateTrip(t *testing.T) {
	trip := sampleTrip()
	service := testService(t)
	service.EXPECT().Create(gomock.Any(), gomock.Any()).Return(trip, nil)
	router := testRouter(service, testPinger(t))
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
			service := testService(t)
			if test.status == http.StatusCreated {
				service.EXPECT().Create(gomock.Any(), gomock.Any()).Return(sampleTrip(), nil).Times(1)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			testRouter(service, testPinger(t)).ServeHTTP(recorder, request)
			if test.status == http.StatusCreated {
				if recorder.Code != test.status {
					t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
				}
				return
			}
			assertProblem(t, recorder, test.status, "invalid_request")
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
			service := testService(t)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			testRouter(service, testPinger(t)).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
			if problem.Detail == nil || *problem.Detail != test.detail {
				t.Fatalf("detail = %v; expected %q", problem.Detail, test.detail)
			}
			if strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("request value leaked: %s", recorder.Body.String())
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
			service := testService(t)
			body := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":` + test.start + `,"end_point":` + test.end + `,"price":10}`
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			testRouter(service, testPinger(t)).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
			if problem.Detail == nil || !strings.Contains(*problem.Detail, test.missing) {
				t.Fatalf("detail = %v, want field %q", problem.Detail, test.missing)
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
			testRouter(testService(t), testPinger(t)).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
			if problem.Detail == nil || !strings.Contains(*problem.Detail, point) {
				t.Fatalf("detail = %v", problem.Detail)
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(strings.Repeat(" ", (1<<20)+1)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	testRouter(testService(t), testPinger(t)).ServeHTTP(recorder, request)
	problem := assertProblem(t, recorder, http.StatusBadRequest, "invalid_request")
	if problem.Detail == nil || *problem.Detail != "request body exceeds the size limit" {
		t.Fatalf("detail = %v", problem.Detail)
	}
}

func TestCreateTripAcceptsZeroCoordinatesAndJSONCharset(t *testing.T) {
	service := testService(t)
	service.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, input domain.CreateTrip) (domain.Trip, error) {
		if input.StartPoint != (domain.Coordinates{}) || input.EndPoint != (domain.Coordinates{}) {
			t.Errorf("input = %#v, want zero coordinates", input)
		}
		return sampleTrip(), nil
	})
	body := `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":0,"longitude":0},"end_point":{"latitude":0,"longitude":0},"price":0}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	recorder := httptest.NewRecorder()
	testRouter(service, testPinger(t)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateTripRejectsUnsupportedContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/problem+json", "application/json; charset=bogus"} {
		t.Run(contentType, func(t *testing.T) {
			service := testService(t)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(`{}`))
			request.Header.Set("Content-Type", contentType)
			recorder := httptest.NewRecorder()
			testRouter(service, testPinger(t)).ServeHTTP(recorder, request)
			problem := assertProblem(t, recorder, http.StatusUnsupportedMediaType, "invalid_request")
			if problem.Detail == nil || *problem.Detail != "Content-Type must be application/json" {
				t.Fatalf("detail = %v", problem.Detail)
			}

		})
	}
}

func TestGeneratedRouterRejectsInvalidTripID(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	router := NewRouter(NewHandler(testService(t), testPinger(t), time.Second, logger))
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
		{"field error", fmt.Errorf("secret data: %w", &domain.FieldError{Field: "price", Reason: "must not be negative"}), http.StatusBadRequest, "invalid_request", "price: must not be negative"},
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
				service := testService(t)
				switch endpoint.name {
				case "create":
					service.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Trip{}, test.err)
				case "get":
					service.EXPECT().Get(gomock.Any(), gomock.Any()).Return(domain.Trip{}, test.err)
				case "finish":
					service.EXPECT().Finish(gomock.Any(), gomock.Any()).Return(domain.Trip{}, test.err)
				}
				router := testRouter(service, testPinger(t))
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(body))
				if endpoint.name == "create" {
					request.Header.Set("Content-Type", "application/json")
				}
				router.ServeHTTP(recorder, request)
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

func TestInternalErrorWriterUsesSameProblemFormat(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler(testService(t), testPinger(t), time.Second, logger)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/trips/example", nil)
	recorder := httptest.NewRecorder()
	handler.WriteInternalError(recorder, request)
	problem := assertProblem(t, recorder, http.StatusInternalServerError, "internal_error")
	if problem.Type != "https://tripgo.example/problems/internal-error" || problem.Title != "Internal Server Error" {
		t.Fatalf("unexpected internal problem: %+v", problem)
	}
}

func TestHealthAndReady(t *testing.T) {
	pinger := testPinger(t)
	pinger.EXPECT().Ping(gomock.Any()).Return(errors.New("down"))
	router := testRouter(testService(t), pinger)

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
	service := testService(t)
	service.EXPECT().Get(gomock.Any(), gomock.Any()).Return(sampleTrip(), nil)
	router := NewRouter(NewHandler(service, testPinger(t), time.Second, logger), observability.HTTPMiddleware(logger))
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

func testRouter(service tripService, pinger databasePinger) http.Handler {
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
