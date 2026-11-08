package get

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/transport/httpapi/shared"
)

// Handler serves GET requests for a trip.
type Handler struct {
	usecase usecase
}

func New(usecase usecase) *Handler {
	return &Handler{usecase: usecase}
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, tripID api.TripId) {
	if tripID == uuid.Nil {
		shared.WriteInvalid(w, r, "tripId must be a non-zero UUID")
		return
	}

	trip, err := h.usecase.Get(r.Context(), tripID)
	if err != nil {
		shared.WriteError(w, r, err)
		return
	}
	shared.WriteJSON(w, http.StatusOK, shared.TripToAPI(trip))
}
