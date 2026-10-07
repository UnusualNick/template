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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
)

const maxRequestBodyBytes = 1 << 20

type tripService interface {
	Create(ctx context.Context, input domain.CreateTrip) (domain.Trip, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Trip, error)
	Finish(ctx context.Context, id uuid.UUID) (domain.Trip, error)
}

type databasePinger interface {
	Ping(ctx context.Context) error
}

type Handler struct {
	service      tripService
	database     databasePinger
	queryTimeout time.Duration
	logger       *slog.Logger
}

func NewHandler(
	service tripService,
	database databasePinger,
	queryTimeout time.Duration,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		service:      service,
		database:     database,
		queryTimeout: queryTimeout,
		logger:       logger,
	}
}

func NewRouter(handler *Handler) http.Handler {
	router := chi.NewRouter()
	router.Use(handler.recoverer)

	return api.HandlerWithOptions(handler, api.ChiServerOptions{
		BaseRouter: router,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			handler.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error())
		},
	})
}

func (h *Handler) CreateTrip(w http.ResponseWriter, r *http.Request, _ api.CreateTripParams) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	request, err := decodeCreateTripRequest(r.Body)
	if err != nil {
		h.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", "Request body is not valid JSON")
		return
	}

	trip, err := h.service.Create(r.Context(), domain.CreateTrip{
		UserID:   request.UserId,
		DriverID: request.DriverId,
		StartPoint: domain.Coordinates{
			Latitude:  request.StartPoint.Latitude,
			Longitude: request.StartPoint.Longitude,
		},
		EndPoint: domain.Coordinates{
			Latitude:  request.EndPoint.Latitude,
			Longitude: request.EndPoint.Longitude,
		},
		Price: request.Price,
	})
	if err != nil {
		h.handleServiceError(w, r, err)
		return
	}

	w.Header().Set("Location", "/api/v1/trips/"+trip.ID.String())
	h.writeJSON(w, http.StatusCreated, tripToAPI(trip))
}

func decodeCreateTripRequest(reader io.Reader) (api.TripData, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return api.TripData{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return api.TripData{}, fmt.Errorf("request body must be a JSON object")
	}
	for _, name := range []string{"user_id", "driver_id", "start_point", "end_point", "price"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return api.TripData{}, fmt.Errorf("required field %q is missing", name)
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request api.TripData
	if err := decoder.Decode(&request); err != nil {
		return api.TripData{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return api.TripData{}, err
	}
	return request, nil
}

func (h *Handler) GetTrip(w http.ResponseWriter, r *http.Request, tripID api.TripId) {
	if tripID == uuid.Nil {
		h.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", "tripId must be a non-zero UUID")
		return
	}
	trip, err := h.service.Get(r.Context(), tripID)
	if err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, tripToAPI(trip))
}

func (h *Handler) FinishTrip(w http.ResponseWriter, r *http.Request, tripID api.TripId) {
	if tripID == uuid.Nil {
		h.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", "tripId must be a non-zero UUID")
		return
	}
	trip, err := h.service.Finish(r.Context(), tripID)
	if err != nil {
		h.handleServiceError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, tripToAPI(trip))
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, api.HealthResponse{Status: api.Ok})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.queryTimeout)
	defer cancel()
	if err := h.database.Ping(ctx); err != nil {
		h.logger.WarnContext(r.Context(), "readiness check failed", "error", err)
		h.writeJSON(w, http.StatusServiceUnavailable, api.HealthResponse{Status: api.Unavailable})
		return
	}
	h.writeJSON(w, http.StatusOK, api.HealthResponse{Status: api.Ok})
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain exactly one JSON object")
	}
	return nil
}

func (h *Handler) handleServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrDriverBusy):
		h.writeProblem(w, r, http.StatusConflict, "driver_busy", "Driver busy", "Driver already has an active trip")
	case errors.Is(err, domain.ErrTripNotFound):
		h.writeProblem(w, r, http.StatusNotFound, "trip_not_found", "Trip not found", "Trip was not found")
	case errors.Is(err, domain.ErrTripCompleted):
		h.writeProblem(w, r, http.StatusConflict, "trip_completed", "Trip completed", "Operation is not allowed for a completed trip")
	case errors.Is(err, domain.ErrInvalidInput):
		h.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "request failed", "error", err, "path", r.URL.Path)
		h.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "Internal Server Error", "An internal error occurred")
	}
}

func (h *Handler) writeProblem(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	code string,
	title string,
	detail string,
) {
	problemType := "https://tripgo.example/problems/" + problemSlug(code)
	instance := r.URL.Path
	w.Header().Set("Content-Type", "application/problem+json")
	h.writeEncoded(w, status, api.Problem{
		Type:     problemType,
		Title:    title,
		Status:   int32(status),
		Detail:   &detail,
		Instance: &instance,
		Code:     code,
	})
}

func problemSlug(code string) string {
	result := []byte(code)
	for i := range result {
		if result[i] == '_' {
			result[i] = '-'
		}
	}
	return string(result)
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	h.writeEncoded(w, status, value)
}

func (h *Handler) writeEncoded(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.logger.Error("encode response", "error", err)
	}
}

func (h *Handler) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				h.logger.ErrorContext(r.Context(), "panic recovered", "panic", recovered, "path", r.URL.Path)
				h.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "Internal Server Error", "An internal error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func tripToAPI(trip domain.Trip) api.Trip {
	return api.Trip{
		Id:       trip.ID,
		UserId:   trip.UserID,
		DriverId: trip.DriverID,
		StartPoint: api.Coordinates{
			Latitude:  trip.StartPoint.Latitude,
			Longitude: trip.StartPoint.Longitude,
		},
		EndPoint: api.Coordinates{
			Latitude:  trip.EndPoint.Latitude,
			Longitude: trip.EndPoint.Longitude,
		},
		Price:          trip.Price,
		Status:         api.TripStatus(trip.Status),
		StartedAt:      trip.StartedAt,
		FinishedAt:     trip.FinishedAt,
		LastPositionAt: trip.LastPositionAt,
	}
}
