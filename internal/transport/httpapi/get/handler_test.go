package get

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

func TestHandlerHandle(t *testing.T) {
	id := uuid.MustParse("1f0a9c62-4a1c-4f2e-9d33-2a4bb0f0b111")
	finished := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	trip := domain.Trip{
		ID: id, UserID: uuid.New(), DriverID: uuid.New(),
		StartPoint: domain.Coordinates{Latitude: 59.9398, Longitude: 30.3146},
		EndPoint:   domain.Coordinates{Latitude: 59.929, Longitude: 30.3626},
		Price:      1450, Status: domain.TripStatusCompleted,
		StartedAt: finished.Add(-time.Hour), FinishedAt: &finished, LastPositionAt: &finished,
	}

	tests := []struct {
		name        string
		id          uuid.UUID
		trip        domain.Trip
		err         error
		wantStatus  int
		wantType    string
		wantCode    string
		wantDetail  string
		callUsecase bool
	}{
		{name: "success", id: id, trip: trip, wantStatus: http.StatusOK, wantType: "application/json", callUsecase: true},
		{name: "nil ID", id: uuid.Nil, wantStatus: http.StatusBadRequest, wantType: "application/problem+json", wantCode: "invalid_request", wantDetail: "tripId must be a non-zero UUID"},
		{name: "not found", id: id, err: fmt.Errorf("lookup: %w", domain.ErrTripNotFound), wantStatus: http.StatusNotFound, wantType: "application/problem+json", wantCode: "trip_not_found", wantDetail: "Trip was not found", callUsecase: true},
		{name: "backend error", id: id, err: errors.New("private backend credentials"), wantStatus: http.StatusInternalServerError, wantType: "application/problem+json", wantCode: "internal_error", wantDetail: "An internal error occurred", callUsecase: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := NewMockusecase(gomock.NewController(t))
			request := httptest.NewRequest(http.MethodGet, "/api/v1/trips/"+tt.id.String(), nil)
			ctx := context.WithValue(request.Context(), contextKey{}, "request-context")
			request = request.WithContext(ctx)
			if tt.callUsecase {
				mock.EXPECT().Get(ctx, tt.id).Return(tt.trip, tt.err)
			}
			response := httptest.NewRecorder()
			New(mock).Handle(response, request, api.TripId(tt.id))

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != tt.wantType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if tt.wantStatus == http.StatusOK {
				var got api.Trip
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatalf("decode trip: %v", err)
				}
				if want := shared.TripToAPI(trip); !reflect.DeepEqual(got, want) {
					t.Fatalf("trip = %#v, want %#v", got, want)
				}
				return
			}

			var problem api.Problem
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem: %v", err)
			}
			if problem.Status != int32(tt.wantStatus) || problem.Code != tt.wantCode || problem.Detail == nil || *problem.Detail != tt.wantDetail {
				t.Fatalf("problem = %#v, want status %d code %q detail %q", problem, tt.wantStatus, tt.wantCode, tt.wantDetail)
			}
			if problem.Instance == nil || *problem.Instance != request.URL.Path {
				t.Fatalf("problem instance = %v, want %q", problem.Instance, request.URL.Path)
			}
			if strings.Contains(response.Body.String(), "private backend credentials") {
				t.Fatal("backend error leaked into response")
			}
		})
	}
}

type contextKey struct{}
