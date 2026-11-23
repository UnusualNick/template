package positions

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
)

const validBody = `{"coordinates":{"latitude":59.9343,"longitude":30.3351},"recorded_at":"2026-09-01T10:05:00Z"}`

var tripID = uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111")

func failSave(t *testing.T) *MockSaveUsecase {
	t.Helper()
	return NewMockSaveUsecase(gomock.NewController(t)) // no calls expected
}

func failList(t *testing.T) *MockListUsecase {
	t.Helper()
	return NewMockListUsecase(gomock.NewController(t)) // no calls expected
}

func post(body, contentType string, id uuid.UUID, usecase saveUsecase) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/trips/"+id.String()+"/positions", strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	NewCreate(usecase).Handle(w, r, id)
	return w
}

func get(id uuid.UUID, usecase listUsecase) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/trips/"+id.String()+"/positions", nil)
	w := httptest.NewRecorder()
	NewList(usecase).Handle(w, r, id)
	return w
}

func assertProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code, detail string, id uuid.UUID) {
	t.Helper()
	if w.Code != status || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status = %d, headers = %v, body = %s", w.Code, w.Header(), w.Body.String())
	}
	var problem api.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/trips/" + id.String() + "/positions"
	if problem.Status != int32(status) || problem.Code != code || problem.Detail == nil || *problem.Detail != detail || problem.Instance == nil || *problem.Instance != path {
		t.Fatalf("problem = %#v, expected status %d code %q detail %q path %q", problem, status, code, detail, path)
	}
}

func TestCreateSuccess(t *testing.T) {
	recorded := time.Date(2026, time.September, 1, 10, 5, 0, 0, time.UTC)
	position := domain.TripPosition{ID: 7, TripID: tripID, Coordinates: domain.Coordinates{Latitude: 59.9343, Longitude: 30.3351}, RecordedAt: recorded}
	for _, body := range []string{validBody, `{"coordinates":{"latitude":0,"longitude":0},"recorded_at":"2026-09-01T10:05:00Z"}`} {
		t.Run(body, func(t *testing.T) {
			input := domain.CreatePosition{Coordinates: position.Coordinates, RecordedAt: recorded}
			if strings.Contains(body, `"latitude":0`) {
				input.Coordinates = domain.Coordinates{}
			}
			usecase := NewMockSaveUsecase(gomock.NewController(t))
			usecase.EXPECT().Execute(gomock.Any(), tripID, input).Return(position, nil)
			w := post(body, "application/json; charset=UTF-8", tripID, usecase)
			if w.Code != http.StatusCreated || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status = %d, headers = %v, body = %s", w.Code, w.Header(), w.Body.String())
			}
			var got api.TripPosition
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != toAPI(position) {
				t.Fatalf("response = %#v, want %#v", got, toAPI(position))
			}
		})
	}
}

func TestCreateInvalidBody(t *testing.T) {
	cases := []struct{ name, body, detail string }{
		{"missing coordinates", `{"recorded_at":"2026-09-01T10:05:00Z"}`, `required field "coordinates" is missing`},
		{"null coordinates", strings.Replace(validBody, `"coordinates":{"latitude":59.9343,"longitude":30.3351}`, `"coordinates":null`, 1), `required field "coordinates" is missing`},
		{"wrong coordinates", strings.Replace(validBody, `"coordinates":{"latitude":59.9343,"longitude":30.3351}`, `"coordinates":[]`, 1), `field "coordinates" must be a JSON object`},
		{"missing latitude", strings.Replace(validBody, `"latitude":59.9343,`, "", 1), `required field "coordinates.latitude" is missing`},
		{"missing longitude", strings.Replace(validBody, `,"longitude":30.3351`, "", 1), `required field "coordinates.longitude" is missing`},
		{"null latitude", strings.Replace(validBody, `"latitude":59.9343`, `"latitude":null`, 1), `required field "coordinates.latitude" is missing`},
		{"missing timestamp", strings.Replace(validBody, `,"recorded_at":"2026-09-01T10:05:00Z"`, "", 1), `required field "recorded_at" is missing`},
		{"null timestamp", strings.Replace(validBody, `"recorded_at":"2026-09-01T10:05:00Z"`, `"recorded_at":null`, 1), `required field "recorded_at" is missing`},
		{"invalid timestamp", strings.Replace(validBody, "2026-09-01T10:05:00Z", "private-not-a-time", 1), "request body contains an invalid field value"},
		{"numeric timestamp", strings.Replace(validBody, `"recorded_at":"2026-09-01T10:05:00Z"`, `"recorded_at":123`, 1), "request body contains an invalid field value"},
		{"boolean timestamp", strings.Replace(validBody, `"recorded_at":"2026-09-01T10:05:00Z"`, `"recorded_at":true`, 1), "request body contains an invalid field value"},
		{"array timestamp", strings.Replace(validBody, `"recorded_at":"2026-09-01T10:05:00Z"`, `"recorded_at":[]`, 1), "request body contains an invalid field value"},
		{"object timestamp", strings.Replace(validBody, `"recorded_at":"2026-09-01T10:05:00Z"`, `"recorded_at":{}`, 1), "request body contains an invalid field value"},
		{"unknown top field", strings.Replace(validBody, `"recorded_at":`, `"private_secret":true,"recorded_at":`, 1), "request body contains an unknown field"},
		{"unknown nested field", strings.Replace(validBody, `"latitude":59.9343`, `"private_secret":true,"latitude":59.9343`, 1), "request body contains an unknown field"},
		{"wrong latitude type", strings.Replace(validBody, `"latitude":59.9343`, `"latitude":"private"`, 1), "request body contains a field with the wrong type"},
		{"malformed", "{", "request body must be valid JSON"},
		{"array", "[]", "request body must be a JSON object"},
		{"null", "null", "request body must be a JSON object"},
		{"trailing JSON", validBody + " {}", "request body must contain exactly one JSON object"},
		{"oversize", validBody + strings.Repeat(" ", 1<<20), "request body exceeds the size limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := post(tc.body, "application/json", tripID, failSave(t))
			assertProblem(t, w, http.StatusBadRequest, "invalid_request", tc.detail, tripID)
			if strings.Contains(w.Body.String(), "private") {
				t.Fatalf("request content leaked: %s", w.Body.String())
			}
		})
	}
}

func TestCreateInvalidContentTypeAndID(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/problem+json", "application/json; charset=bogus", "invalid"} {
		t.Run(contentType, func(t *testing.T) {
			w := post(validBody, contentType, tripID, failSave(t))
			assertProblem(t, w, http.StatusUnsupportedMediaType, "invalid_request", "Content-Type must be application/json", tripID)
		})
	}
	assertProblem(t, post(validBody, "application/json", uuid.Nil, failSave(t)), http.StatusBadRequest, "invalid_request", "tripId must be a non-zero UUID", uuid.Nil)
}

func TestCreateUsecaseErrors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		status       int
		code, detail string
	}{
		{"missing", fmt.Errorf("wrapped: %w", domain.ErrTripNotFound), http.StatusNotFound, "trip_not_found", "Trip was not found"},
		{"completed", domain.ErrTripCompleted, http.StatusConflict, "trip_completed", "Operation is not allowed for a completed trip"},
		{"invalid", &domain.FieldError{Field: "recorded_at", Reason: "private"}, http.StatusBadRequest, "invalid_request", "Invalid trip input"},
		{"backend", errors.New("private database credentials"), http.StatusInternalServerError, "internal_error", "An internal error occurred"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usecase := NewMockSaveUsecase(gomock.NewController(t))
			usecase.EXPECT().Execute(gomock.Any(), tripID, gomock.Any()).Return(domain.TripPosition{}, tc.err)
			w := post(validBody, "application/json", tripID, usecase)
			assertProblem(t, w, tc.status, tc.code, tc.detail, tripID)
			if strings.Contains(w.Body.String(), "private") {
				t.Fatalf("private error leaked: %s", w.Body.String())
			}
		})
	}
}

func TestListSuccessAndErrors(t *testing.T) {
	first := domain.TripPosition{ID: 3, TripID: tripID, Coordinates: domain.Coordinates{Latitude: 1, Longitude: 2}, RecordedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}
	last := domain.TripPosition{ID: 2, TripID: tripID, Coordinates: domain.Coordinates{Latitude: 3, Longitude: 4}, RecordedAt: first.RecordedAt.Add(time.Minute)}
	for _, positions := range [][]domain.TripPosition{nil, {}, {first, last}} {
		t.Run(fmt.Sprintf("%d positions", len(positions)), func(t *testing.T) {
			usecase := NewMockListUsecase(gomock.NewController(t))
			usecase.EXPECT().Execute(gomock.Any(), tripID).Return(positions, nil)
			w := get(tripID, usecase)
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var got api.TripPositionsResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Positions == nil || len(got.Positions) != len(positions) {
				t.Fatalf("positions = %#v, expected count = %d and non-nil", got.Positions, len(positions))
			}
			for i, item := range got.Positions {
				if item != toAPI(positions[i]) {
					t.Fatalf("position %d = %#v, want %#v", i, item, toAPI(positions[i]))
				}
			}
		})
	}
	assertProblem(t, get(uuid.Nil, failList(t)), http.StatusBadRequest, "invalid_request", "tripId must be a non-zero UUID", uuid.Nil)
	for _, tc := range []struct {
		err          error
		status       int
		code, detail string
	}{
		{domain.ErrTripNotFound, http.StatusNotFound, "trip_not_found", "Trip was not found"},
		{errors.New("private database credentials"), http.StatusInternalServerError, "internal_error", "An internal error occurred"},
	} {
		usecase := NewMockListUsecase(gomock.NewController(t))
		usecase.EXPECT().Execute(gomock.Any(), tripID).Return(nil, tc.err)
		w := get(tripID, usecase)
		assertProblem(t, w, tc.status, tc.code, tc.detail, tripID)
		if strings.Contains(w.Body.String(), "private") {
			t.Fatalf("private error leaked: %s", w.Body.String())
		}
	}
}
