package domainerror

import (
	"errors"
	"net/http"
	"testing"

	"github.com/UnusualNick/template/internal/domain"
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
