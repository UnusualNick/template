package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
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
			handler.logger.DebugContext(r.Context(), "invalid route parameter", "error", err)
			var invalidFormat *api.InvalidParamFormatError
			if errors.As(err, &invalidFormat) && invalidFormat.ParamName == "tripId" {
				handler.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", "tripId must be a UUID")
				return
			}
			handler.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", "Invalid request parameter")
		},
	})
}

func (h *Handler) CreateTrip(w http.ResponseWriter, r *http.Request, _ api.CreateTripParams) {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		h.writeProblem(w, r, http.StatusUnsupportedMediaType, "invalid_request", "Invalid request", "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	request, err := decodeCreateTripRequest(r.Body)
	if err != nil {
		h.logger.DebugContext(r.Context(), "invalid create trip body", "error", err)
		h.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid request", createTripDecodeDetail(err))
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
	fieldsDecoder := json.NewDecoder(bytes.NewReader(body))
	if err := fieldsDecoder.Decode(&fields); err != nil {
		return api.TripData{}, err
	}
	if err := ensureJSONEOF(fieldsDecoder); err != nil {
		return api.TripData{}, err
	}
	if fields == nil {
		return api.TripData{}, requestValidationError("request body must be a JSON object")
	}
	for _, name := range []string{"user_id", "driver_id", "start_point", "end_point", "price"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return api.TripData{}, requestValidationError(fmt.Sprintf("required field %q is missing", name))
		}
	}
	for _, point := range []string{"start_point", "end_point"} {
		var coordinates map[string]json.RawMessage
		if err := json.Unmarshal(fields[point], &coordinates); err != nil || coordinates == nil {
			return api.TripData{}, requestValidationError(fmt.Sprintf("field %q must be a JSON object", point))
		}
		for _, coordinate := range []string{"latitude", "longitude"} {
			value, ok := coordinates[coordinate]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return api.TripData{}, requestValidationError(fmt.Sprintf("required field %q is missing", point+"."+coordinate))
			}
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

type requestValidationError string

func (e requestValidationError) Error() string { return string(e) }

func createTripDecodeDetail(err error) string {
	var validationErr requestValidationError
	if errors.As(err, &validationErr) {
		return validationErr.Error()
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return "request body exceeds the size limit"
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "request body must be valid JSON"
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		if typeErr.Field == "" {
			return "request body must be a JSON object"
		}
		return "request body contains a field with the wrong type"
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		return "request body contains an unknown field"
	}
	return "request body contains an invalid field value"
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
		return requestValidationError("request body must contain exactly one JSON object")
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
