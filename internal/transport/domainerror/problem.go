// Package domainerror maps domain failures to safe HTTP problem responses.
package domainerror

import (
	"errors"
	"net/http"
	"strings"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
)

// Problem translates a service error into an HTTP status and a public problem.
// Wrapped error strings are never included: they may contain private data.
func Problem(err error) (int, api.Problem) {
	switch {
	case errors.Is(err, domain.ErrDriverBusy):
		return NewProblem(http.StatusConflict, "driver_busy", "Driver busy", "Driver already has an active trip")
	case errors.Is(err, domain.ErrTripNotFound):
		return NewProblem(http.StatusNotFound, "trip_not_found", "Trip not found", "Trip was not found")
	case errors.Is(err, domain.ErrTripCompleted):
		return NewProblem(http.StatusConflict, "trip_completed", "Trip completed", "Operation is not allowed for a completed trip")
	case errors.Is(err, domain.ErrInvalidInput):
		detail := "Invalid trip input"
		var fieldErr *domain.FieldError
		if errors.As(err, &fieldErr) && fieldErr != nil {
			if reason, known := safeFieldReasons[fieldErr.Field]; known && reason == fieldErr.Reason {
				detail = fieldErr.Field + ": " + fieldErr.Reason
			}
		}
		return NewProblem(http.StatusBadRequest, "invalid_request", "Invalid request", detail)
	default:
		return NewProblem(http.StatusInternalServerError, "internal_error", "Internal Server Error", "An internal error occurred")
	}
}

// Only validation messages emitted by domain rules may be exposed. A FieldError
// from another source must not turn arbitrary field names or reasons into output.
var safeFieldReasons = map[string]string{
	"user_id":               "must be a non-zero UUID",
	"driver_id":             "must be a non-zero UUID",
	"start_point.latitude":  "must be between -90 and 90",
	"start_point.longitude": "must be between -180 and 180",
	"end_point.latitude":    "must be between -90 and 90",
	"end_point.longitude":   "must be between -180 and 180",
	"price":                 "must not be negative",
	"status":                "must be active",
}

// NewProblem builds a problem with a public, caller-supplied detail.
func NewProblem(status int, code, title, detail string) (int, api.Problem) {
	if status < 100 || status > 599 {
		status = http.StatusInternalServerError
	}
	return status, api.Problem{
		Type:   "https://tripgo.example/problems/" + strings.ReplaceAll(code, "_", "-"),
		Title:  title,
		Status: int32(status), // #nosec G115 -- validated HTTP status is in [100, 599].
		Detail: &detail,
		Code:   code,
	}
}
