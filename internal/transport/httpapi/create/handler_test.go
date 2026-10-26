package create

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
)

const validBody = `{"user_id":"5cb72c04-7650-45c9-a79b-bcdba0631e0c","driver_id":"8860b315-ec86-42eb-a17c-7c163d721ff5","start_point":{"latitude":1,"longitude":2},"end_point":{"latitude":3,"longitude":4},"price":10}`

func sampleTrip() domain.Trip {
	return domain.Trip{
		ID:         uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111"),
		UserID:     uuid.MustParse("5cb72c04-7650-45c9-a79b-bcdba0631e0c"),
		DriverID:   uuid.MustParse("8860b315-ec86-42eb-a17c-7c163d721ff5"),
		StartPoint: domain.Coordinates{Latitude: 1, Longitude: 2},
		EndPoint:   domain.Coordinates{Latitude: 3, Longitude: 4},
		Price:      10,
		Status:     domain.TripStatusActive,
		StartedAt:  time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
	}
}

func serve(t *testing.T, mock *Mockusecase, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/trips", strings.NewReader(body))
	w := httptest.NewRecorder()
	New(mock).Handle(w, r)
	return w
}

func assertProblem(t *testing.T, w *httptest.ResponseRecorder, status int, code, detail string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var problem api.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != int32(status) || problem.Code != code || problem.Detail == nil || *problem.Detail != detail || problem.Instance == nil || *problem.Instance != "/api/v1/trips" {
		t.Fatalf("problem = %#v", problem)
	}
	if w.Header().Get("Location") != "" {
		t.Fatalf("error response Location = %q", w.Header().Get("Location"))
	}
}

func TestHandleSuccess(t *testing.T) {
	trip := sampleTrip()
	cases := []struct {
		name  string
		body  string
		input domain.CreateTrip
	}{
		{"coordinates", validBody, domain.CreateTrip{UserID: trip.UserID, DriverID: trip.DriverID, StartPoint: trip.StartPoint, EndPoint: trip.EndPoint, Price: trip.Price}},
		{"zero coordinates", strings.ReplaceAll(strings.ReplaceAll(validBody, `"latitude":1,"longitude":2`, `"latitude":0,"longitude":0`), `"latitude":3,"longitude":4`, `"latitude":0,"longitude":0`), domain.CreateTrip{UserID: trip.UserID, DriverID: trip.DriverID, Price: trip.Price}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := NewMockusecase(gomock.NewController(t))
			mock.EXPECT().Create(gomock.Any(), tc.input).DoAndReturn(func(ctx context.Context, input domain.CreateTrip) (domain.Trip, error) {
				if ctx == nil || !reflect.DeepEqual(input, tc.input) {
					t.Fatalf("input = %#v", input)
				}
				return trip, nil
			})
			w := serve(t, mock, tc.body)
			if w.Code != http.StatusCreated || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Location") != "/api/v1/trips/"+trip.ID.String() {
				t.Fatalf("status = %d, headers = %v, body = %s", w.Code, w.Header(), w.Body.String())
			}
			var response api.Trip
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Id != trip.ID || response.UserId != trip.UserID || response.DriverId != trip.DriverID || response.Price != trip.Price || response.StartPoint.Latitude != trip.StartPoint.Latitude || response.EndPoint.Longitude != trip.EndPoint.Longitude || response.Status != api.TripStatus(trip.Status) {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestHandleInvalidBody(t *testing.T) {
	cases := []struct{ name, body string }{
		{"missing start latitude", strings.Replace(validBody, `"latitude":1,`, "", 1)},
		{"missing start longitude", strings.Replace(validBody, `,"longitude":2`, "", 1)},
		{"missing end latitude", strings.Replace(validBody, `"latitude":3,`, "", 1)},
		{"missing end longitude", strings.Replace(validBody, `,"longitude":4`, "", 1)},
		{"null coordinate", strings.Replace(validBody, `"latitude":1`, `"latitude":null`, 1)},
		{"missing top-level field", strings.Replace(validBody, `,"price":10`, "", 1)},
		{"null point", strings.Replace(validBody, `"start_point":{"latitude":1,"longitude":2}`, `"start_point":null`, 1)},
		{"unknown top-level field", strings.Replace(validBody, `"price":10`, `"price":10,"extra":true`, 1)},
		{"unknown nested field", strings.Replace(validBody, `"latitude":1`, `"latitude":1,"altitude":10`, 1)},
		{"malformed JSON", "{"},
		{"non-object JSON", "[]"},
		{"trailing JSON", validBody + " {}"},
		{"body over 1 MiB", validBody + strings.Repeat(" ", 1<<20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := NewMockusecase(gomock.NewController(t))
			assertProblem(t, serve(t, mock, tc.body), http.StatusBadRequest, "invalid_request", "Request body is not valid JSON")
		})
	}
}

func TestHandleServiceErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		detail string
	}{
		{"driver busy", fmt.Errorf("wrapped: %w", domain.ErrDriverBusy), http.StatusConflict, "driver_busy", "Driver already has an active trip"},
		{"not found", domain.ErrTripNotFound, http.StatusNotFound, "trip_not_found", "Trip was not found"},
		{"completed", domain.ErrTripCompleted, http.StatusConflict, "trip_completed", "Operation is not allowed for a completed trip"},
		{"invalid input", domain.ErrInvalidInput, http.StatusBadRequest, "invalid_request", "Invalid trip input"},
		{"field error", &domain.FieldError{Field: "price", Reason: "private"}, http.StatusBadRequest, "invalid_request", "Invalid trip input"},
		{"unknown error", errors.New("private credentials"), http.StatusInternalServerError, "internal_error", "An internal error occurred"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := NewMockusecase(gomock.NewController(t))
			mock.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domain.Trip{}, tc.err)
			w := serve(t, mock, validBody)
			assertProblem(t, w, tc.status, tc.code, tc.detail)
			if strings.Contains(w.Body.String(), "private") {
				t.Fatalf("private error leaked: %s", w.Body.String())
			}
		})
	}
}
