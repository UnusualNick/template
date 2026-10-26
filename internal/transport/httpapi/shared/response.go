// Package shared contains response conversion for the three HTTP actions.
// It knows about OpenAPI, but neither domain nor usecase imports it.
package shared

import (
	"encoding/json"
	"net/http"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/transport/domainerror"
)

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteProblem(w http.ResponseWriter, r *http.Request, status int, problem api.Problem) {
	instance := r.URL.Path
	problem.Instance = &instance
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	status, problem := domainerror.Problem(err)
	WriteProblem(w, r, status, problem)
}

func WriteInvalid(w http.ResponseWriter, r *http.Request, detail string) {
	status, problem := domainerror.NewProblem(http.StatusBadRequest, "invalid_request", "Invalid request", detail)
	WriteProblem(w, r, status, problem)
}

func TripToAPI(trip domain.Trip) api.Trip {
	return api.Trip{
		Id: trip.ID, UserId: trip.UserID, DriverId: trip.DriverID,
		StartPoint: api.Coordinates{Latitude: trip.StartPoint.Latitude, Longitude: trip.StartPoint.Longitude},
		EndPoint:   api.Coordinates{Latitude: trip.EndPoint.Latitude, Longitude: trip.EndPoint.Longitude},
		Price:      trip.Price, Status: api.TripStatus(trip.Status), StartedAt: trip.StartedAt,
		FinishedAt: trip.FinishedAt, LastPositionAt: trip.LastPositionAt,
	}
}
