package create

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/transport/domainerror"
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
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		status, problem := domainerror.NewProblem(http.StatusUnsupportedMediaType, "invalid_request", "Invalid request", "Content-Type must be application/json")
		shared.WriteProblem(w, r, status, problem)
		return
	}

	request, err := decodeCreateTripRequest(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		shared.WriteInvalid(w, r, createTripDecodeDetail(err))
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
	for _, name := range []string{"start_point", "end_point"} {
		var coordinates map[string]json.RawMessage
		if err := json.Unmarshal(fields[name], &coordinates); err != nil || coordinates == nil {
			return api.TripData{}, requestValidationError(fmt.Sprintf("field %q must be a JSON object", name))
		}
		for _, axis := range []string{"latitude", "longitude"} {
			value, ok := coordinates[axis]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return api.TripData{}, requestValidationError(fmt.Sprintf("required field %q is missing", name+"."+axis))
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
		return requestValidationError("request body must contain exactly one JSON object")
	}
	return nil
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
