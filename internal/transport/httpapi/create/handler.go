package create

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/transport/httpapi/shared"
)

const maxRequestBodyBytes = 1 << 20

type Handler struct {
	usecase usecase
}

func New(usecase usecase) *Handler {
	return &Handler{usecase: usecase}
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	request, err := decodeCreateTripRequest(r.Body)
	if err != nil {
		shared.WriteInvalid(w, r, "Request body is not valid JSON")
		return
	}

	trip, err := h.usecase.Create(r.Context(), domain.CreateTrip{
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
		shared.WriteError(w, r, err)
		return
	}

	w.Header().Set("Location", "/api/v1/trips/"+trip.ID.String())
	shared.WriteJSON(w, http.StatusCreated, shared.TripToAPI(trip))
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
	for _, name := range []string{"start_point", "end_point"} {
		var coordinates map[string]json.RawMessage
		if err := json.Unmarshal(fields[name], &coordinates); err != nil || coordinates == nil {
			return api.TripData{}, fmt.Errorf("%s must be a JSON object", name)
		}
		for _, axis := range []string{"latitude", "longitude"} {
			value, ok := coordinates[axis]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return api.TripData{}, fmt.Errorf("required field %q is missing", name+"."+axis)
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

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request body must contain exactly one JSON object")
	}
	return nil
}
