// Package httpapi adapts the generated OpenAPI server to action-specific HTTP
// handlers. Business rules live in the three independent usecase packages.
//
//go:generate sh -c "go run go.uber.org/mock/mockgen -source=handler.go -destination=mocks_test.go -package=httpapi -mock_names=tripService=MockTripService,databasePinger=MockDatabasePinger && sed -n '/^..go:generate /p' handler.go >> mocks_test.go"
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/transport/domainerror"
	httpcreate "github.com/UnusualNick/template/internal/transport/httpapi/create"
	httpfinish "github.com/UnusualNick/template/internal/transport/httpapi/finish"
	httpget "github.com/UnusualNick/template/internal/transport/httpapi/get"
)

// The generated server needs one adapter, while each action handler owns a
// narrower interface in its contract.go file.
type tripService interface {
	Create(ctx context.Context, input domain.CreateTrip) (domain.Trip, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Trip, error)
	Finish(ctx context.Context, id uuid.UUID) (domain.Trip, error)
}

type databasePinger interface {
	Ping(ctx context.Context) error
}

type Handler struct {
	create       *httpcreate.Handler
	get          *httpget.Handler
	finish       *httpfinish.Handler
	database     databasePinger
	queryTimeout time.Duration
	logger       *slog.Logger
}

func NewHandler(service tripService, database databasePinger, queryTimeout time.Duration, logger *slog.Logger) *Handler {
	return &Handler{
		create: httpcreate.New(service), get: httpget.New(service), finish: httpfinish.New(service),
		database: database, queryTimeout: queryTimeout, logger: logger,
	}
}

// NewRouter registers route-aware middleware inside chi so instrumentation
// observes matched route templates rather than high-cardinality URLs.
func NewRouter(handler *Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	router := chi.NewRouter()
	if len(middlewares) == 0 {
		// Preserve standalone recovery for tests and simple consumers.
		router.Use(handler.recoverer)
	} else {
		router.Use(middlewares...)
	}
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
	h.create.Handle(w, r)
}

func (h *Handler) GetTrip(w http.ResponseWriter, r *http.Request, tripID api.TripId) {
	h.get.Handle(w, r, tripID)
}

func (h *Handler) FinishTrip(w http.ResponseWriter, r *http.Request, tripID api.TripId) {
	h.finish.Handle(w, r, tripID)
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, api.HealthResponse{Status: api.Ok})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.queryTimeout)
	defer cancel()
	if err := h.database.Ping(ctx); err != nil {
		h.writeJSON(w, http.StatusServiceUnavailable, api.HealthResponse{Status: api.Unavailable})
		return
	}
	h.writeJSON(w, http.StatusOK, api.HealthResponse{Status: api.Ok})
}

func (h *Handler) writeProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	_, problem := domainerror.NewProblem(status, code, title, detail)
	instance := r.URL.Path
	problem.Instance = &instance
	w.Header().Set("Content-Type", "application/problem+json")
	h.writeEncoded(w, status, problem)
}

// WriteInternalError renders a panic response using the same problem format as
// other internal failures, without exposing the panic to the client.
func (h *Handler) WriteInternalError(w http.ResponseWriter, r *http.Request) {
	h.writeProblem(w, r, http.StatusInternalServerError, "internal_error", "Internal Server Error", "An internal error occurred")
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	h.writeEncoded(w, status, value)
}

func (h *Handler) writeEncoded(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		h.logger.Error("encode response", "error.type", "write_failed")
	}
}

func (h *Handler) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				h.logger.ErrorContext(r.Context(), "panic recovered", "error.type", "panic")
				h.WriteInternalError(w, r)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
