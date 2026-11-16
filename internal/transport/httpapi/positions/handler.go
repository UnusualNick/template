// Package positions adapts the create and list position usecases to HTTP.
package positions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
	"github.com/UnusualNick/template/internal/transport/domainerror"
	"github.com/UnusualNick/template/internal/transport/httpapi/shared"
)

const maxRequestBodyBytes = 1 << 20

type CreateHandler struct{ usecase saveUsecase }

type ListHandler struct{ usecase listUsecase }

func NewCreate(usecase saveUsecase) *CreateHandler { return &CreateHandler{usecase: usecase} }

func NewList(usecase listUsecase) *ListHandler { return &ListHandler{usecase: usecase} }

func (h *CreateHandler) Handle(w http.ResponseWriter, r *http.Request, tripID api.TripId) {
	if tripID == uuid.Nil {
		shared.WriteInvalid(w, r, "tripId must be a non-zero UUID")
		return
	}

	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || (parameters["charset"] != "" && !strings.EqualFold(parameters["charset"], "utf-8")) {
		status, problem := domainerror.NewProblem(http.StatusUnsupportedMediaType, "invalid_request", "Invalid request", "Content-Type must be application/json")
		shared.WriteProblem(w, r, status, problem)
		return
	}

	request, err := decodePositionData(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		shared.WriteInvalid(w, r, decodeDetail(err))
		return
	}

	position, err := h.usecase.Execute(r.Context(), tripID, domain.CreatePosition{
		Coordinates: domain.Coordinates{Latitude: request.Coordinates.Latitude, Longitude: request.Coordinates.Longitude},
		RecordedAt:  request.RecordedAt,
	})
	if err != nil {
		shared.WriteError(w, r, err)
		return
	}
	shared.WriteJSON(w, http.StatusCreated, toAPI(position))
}

func (h *ListHandler) Handle(w http.ResponseWriter, r *http.Request, tripID api.TripId) {
	if tripID == uuid.Nil {
		shared.WriteInvalid(w, r, "tripId must be a non-zero UUID")
		return
	}

	positions, err := h.usecase.Execute(r.Context(), tripID)
	if err != nil {
		shared.WriteError(w, r, err)
		return
	}
	result := make([]api.TripPosition, 0, len(positions))
	for _, position := range positions {
		result = append(result, toAPI(position))
	}
	// The usecase supplies positions in recorded_at order; do not reorder equal timestamps.
	shared.WriteJSON(w, http.StatusOK, api.TripPositionsResponse{Positions: result})
}

func toAPI(position domain.TripPosition) api.TripPosition {
	return api.TripPosition{
		Id: position.ID, TripId: position.TripID,
		Coordinates: api.Coordinates{Latitude: position.Coordinates.Latitude, Longitude: position.Coordinates.Longitude},
		RecordedAt:  position.RecordedAt,
	}
}

func decodePositionData(reader io.Reader) (api.PositionData, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return api.PositionData{}, err
	}
	var fields map[string]json.RawMessage
	fieldsDecoder := json.NewDecoder(bytes.NewReader(body))
	if err := fieldsDecoder.Decode(&fields); err != nil {
		return api.PositionData{}, err
	}
	if err := ensureJSONEOF(fieldsDecoder); err != nil {
		return api.PositionData{}, err
	}
	if fields == nil {
		return api.PositionData{}, requestValidationError("request body must be a JSON object")
	}
	for _, name := range []string{"coordinates", "recorded_at"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return api.PositionData{}, requestValidationError(fmt.Sprintf("required field %q is missing", name))
		}
	}
	var coordinates map[string]json.RawMessage
	if err := json.Unmarshal(fields["coordinates"], &coordinates); err != nil || coordinates == nil {
		return api.PositionData{}, requestValidationError(`field "coordinates" must be a JSON object`)
	}
	for _, axis := range []string{"latitude", "longitude"} {
		value, ok := coordinates[axis]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return api.PositionData{}, requestValidationError(fmt.Sprintf("required field %q is missing", "coordinates."+axis))
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request api.PositionData
	if err := decoder.Decode(&request); err != nil {
		return api.PositionData{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return api.PositionData{}, err
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

// Never reflect parsing errors (which can contain request values) to clients.
func decodeDetail(err error) string {
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
