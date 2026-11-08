package shared

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/UnusualNick/template/api"
	"github.com/UnusualNick/template/internal/domain"
)

func TestWriteErrorLogsUnknownErrorOnceWithTrace(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/trips/test?token=private-query", nil)
	request = request.WithContext(trace.ContextWithSpanContext(request.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID,
	})))
	response := httptest.NewRecorder()
	underlying := fmt.Errorf("database operation: %w", errors.New("private-db-password"))

	WriteError(response, request, underlying)

	if response.Code != http.StatusInternalServerError || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response status/content type = %d/%q", response.Code, response.Header().Get("Content-Type"))
	}
	var problem api.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Detail == nil || *problem.Detail != "An internal error occurred" || problem.Instance == nil || *problem.Instance != "/api/v1/trips/test" {
		t.Fatalf("unexpected public problem: %+v", problem)
	}
	if strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "database operation") {
		t.Fatalf("response leaks underlying error: %s", response.Body.String())
	}
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected exactly one error log, got %d: %s", len(lines), logs.String())
	}
	var record map[string]any
	if err := json.Unmarshal(lines[0], &record); err != nil {
		t.Fatal(err)
	}
	if record["level"] != "ERROR" || record["error"] != underlying.Error() || record["trace_id"] != traceID.String() || record["http.request.method"] != http.MethodGet {
		t.Fatalf("unexpected error log: %+v", record)
	}
	if strings.Contains(logs.String(), "private-query") {
		t.Fatalf("log contains request query: %s", logs.String())
	}
}

func TestWriteErrorDoesNotLogExpectedDomainErrors(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	for _, err := range []error{
		domain.ErrInvalidInput,
		fmt.Errorf("wrapped: %w", &domain.FieldError{Field: "price", Reason: "must not be negative"}),
		domain.ErrDriverBusy,
		domain.ErrTripNotFound,
		domain.ErrTripCompleted,
	} {
		response := httptest.NewRecorder()
		WriteError(response, httptest.NewRequest(http.MethodGet, "/api/v1/trips", nil), err)
		if response.Code >= http.StatusInternalServerError {
			t.Fatalf("known error %v returned status %d", err, response.Code)
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("expected domain errors not to be logged: %s", logs.String())
	}
}
