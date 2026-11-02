package domainerror

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/UnusualNick/template/internal/domain"
)

const (
	validationPriceField = "price"
	genericInputDetail   = "Invalid trip input"
)

func TestProblemMapsDomainErrorsWithoutLeakingWrappedDetails(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid", domain.ErrInvalidInput, http.StatusBadRequest, "invalid_request"},
		{"missing", domain.ErrTripNotFound, http.StatusNotFound, "trip_not_found"},
		{"completed", domain.ErrTripCompleted, http.StatusConflict, "trip_completed"},
		{"driver busy", domain.ErrDriverBusy, http.StatusConflict, "driver_busy"},
		{"internal", errors.New("postgres://secret-db-password"), http.StatusInternalServerError, "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, problem := Problem(test.err)
			if status != test.status || int(problem.Status) != status || problem.Code != test.code {
				t.Fatalf("Problem(%v) = %d, %+v", test.err, status, problem)
			}
			if problem.Detail == nil || *problem.Detail == "postgres://secret-db-password" {
				t.Fatalf("unsafe problem detail: %+v", problem.Detail)
			}
		})
	}
}

func TestProblemShowsOnlyKnownFieldValidation(t *testing.T) {
	for _, tc := range []struct {
		name, field, reason, want string
	}{
		{validationPriceField, validationPriceField, "must not be negative", "price: must not be negative"},
		{"coordinates", "start_point.latitude", "must be between -90 and 90", "start_point.latitude: must be between -90 and 90"},
		{"status", "status", "must be active", "status: must be active"},
		{"unknown field", "database_password", "must not be negative", genericInputDetail},
		{"unknown field empty reason", "database_password", "", genericInputDetail},
		{"unknown reason", validationPriceField, "private credentials", genericInputDetail},
		{"empty reason", validationPriceField, "", genericInputDetail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("wrapped private credentials: %w", &domain.FieldError{Field: tc.field, Reason: tc.reason})
			status, problem := Problem(err)
			if status != http.StatusBadRequest || problem.Code != "invalid_request" || problem.Detail == nil || *problem.Detail != tc.want {
				t.Fatalf("Problem(%v) = %d, %+v; want detail %q", err, status, problem, tc.want)
			}
		})
	}
}
