package finish

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
	"github.com/UnusualNick/template/internal/transport/httpapi/shared"
)

func TestHandle(t *testing.T) {
	id := uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111")
	finishedAt := time.Date(2025, time.March, 2, 15, 4, 5, 0, time.UTC)
	lastPositionAt := finishedAt.Add(-time.Minute)
	trip := domain.Trip{
		ID: id, UserID: uuid.MustParse("5cb72c04-7650-45c9-a79b-bcdba0631e0c"),
		DriverID:   uuid.MustParse("8860b315-ec86-42eb-a17c-7c163d721ff5"),
		StartPoint: domain.Coordinates{Latitude: 59.9398, Longitude: 30.3146},
		EndPoint:   domain.Coordinates{Latitude: 59.929, Longitude: 30.3626},
		Price:      1450, Status: domain.TripStatusCompleted,
		StartedAt: finishedAt.Add(-time.Hour), FinishedAt: &finishedAt, LastPositionAt: &lastPositionAt,
	}
	const path = "/api/v1/trips/1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111/finish"
	tests := []struct {
		name        string
		id          uuid.UUID
		result      domain.Trip
		failure     error
		status      int
		contentType string
		code        string
		detail      string
	}{
		{name: "success", id: id, result: trip, status: http.StatusOK, contentType: "application/json"},
		{name: "nil ID", id: uuid.Nil, status: http.StatusBadRequest, contentType: "application/problem+json", code: "invalid_request", detail: "tripId must be a non-zero UUID"},
		{name: "completed conflict", id: id, failure: fmt.Errorf("wrapped: %w", domain.ErrTripCompleted), status: http.StatusConflict, contentType: "application/problem+json", code: "trip_completed", detail: "Operation is not allowed for a completed trip"},
		{name: "backend error", id: id, failure: errors.New("secret database credentials"), status: http.StatusInternalServerError, contentType: "application/problem+json", code: "internal_error", detail: "An internal error occurred"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usecase := NewMockfinishUsecase(gomock.NewController(t))
			request := httptest.NewRequest(http.MethodPost, path, nil)
			ctx := context.WithValue(request.Context(), contextKey{}, "request context")
			request = request.WithContext(ctx)
			if test.id != uuid.Nil {
				usecase.EXPECT().Finish(ctx, test.id).Return(test.result, test.failure)
			}
			recorder := httptest.NewRecorder()

			New(usecase).Handle(recorder, request, test.id)

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			if got := recorder.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if test.code == "" {
				var response api.Trip
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode trip: %v", err)
				}
				if want := shared.TripToAPI(trip); !reflect.DeepEqual(response, want) {
					t.Fatalf("trip = %+v, want %+v", response, want)
				}
				return
			}
			var problem api.Problem
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Code != test.code || problem.Status != int32(test.status) || problem.Detail == nil || *problem.Detail != test.detail {
				t.Fatalf("problem = %+v, want code %q, status %d, detail %q", problem, test.code, test.status, test.detail)
			}
			if problem.Instance == nil || *problem.Instance != path {
				t.Fatalf("instance = %v, want %q", problem.Instance, path)
			}
			if strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("problem leaked backend error: %s", recorder.Body.String())
			}
		})
	}
}

type contextKey struct{}
