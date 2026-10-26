package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPMetricsUseRouteTemplateAndSeconds(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(context.Background()) })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := chi.NewRouter()
	router.Use(Recover(logger), Tracing(), Logging(logger), HTTPMiddleware(logger))
	router.Get("/trips/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/trips/private-id?secret=hidden", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status: %d", recorder.Code)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatal(err)
	}
	foundDuration, foundActive := false, false
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch m.Name {
			case "http.server.request.duration":
				foundDuration = true
				if m.Unit != "s" {
					t.Errorf("unit: %s", m.Unit)
				}
				histogram, ok := m.Data.(metricdata.Histogram[float64])
				if !ok || len(histogram.DataPoints) != 1 {
					t.Fatalf("duration: %#v", m.Data)
				}
				attrs := histogram.DataPoints[0].Attributes.ToSlice()
				if len(attrs) != 3 {
					t.Errorf("duration attrs: %v", attrs)
				}
				for _, attr := range attrs {
					if attr.Key == "http.route" && attr.Value.AsString() != "/trips/{id}" {
						t.Errorf("route: %v", attr)
					}
					if attr.Key == "http.response.status_code" && attr.Value.AsInt64() != http.StatusAccepted {
						t.Errorf("status: %v", attr)
					}
				}
			case "http.server.active_requests":
				foundActive = true
				active, ok := m.Data.(metricdata.Sum[int64])
				if !ok || len(active.DataPoints) != 1 {
					t.Fatalf("active requests: %#v", m.Data)
				}
				if active.DataPoints[0].Value != 0 {
					t.Errorf("active requests after completion = %d", active.DataPoints[0].Value)
				}
				matched := false
				for _, attr := range active.DataPoints[0].Attributes.ToSlice() {
					if attr.Key == "http.route" && attr.Value.AsString() == "/trips/{id}" {
						matched = true
					}
				}
				if !matched {
					t.Errorf("active request route is not a template: %+v", active.DataPoints[0].Attributes)
				}
			}
		}
	}
	if !foundDuration || !foundActive {
		t.Fatalf("missing metrics: duration=%t active=%t", foundDuration, foundActive)
	}
}

func TestTracingNeverExportsPrivateURLs(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		_ = provider.Shutdown(context.Background())
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := chi.NewRouter()
	router.Use(Recover(logger), Tracing(), Logging(logger), HTTPMiddleware(logger))
	router.Get("/trips/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	const privateID = "e6c0ee69-838d-492e-8c59-c756f26c0ed4"
	const privateSegment = "private-unknown-route-segment"
	const privateQuery = "private-query-value"
	for _, tc := range []struct {
		url, name string
		status    int
	}{
		{"/trips/" + privateID + "?token=" + privateQuery, "GET /trips/{id}", http.StatusAccepted},
		{"/missing/" + privateSegment + "?token=" + privateQuery, "GET", http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-1234567890abcdef-01")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, response.Code, tc.status)
		}
	}
	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans: %d, want 2", len(ended))
	}
	for i, span := range ended {
		want := []string{"GET /trips/{id}", "GET"}[i]
		if span.Name() != want || span.SpanKind() != trace.SpanKindServer || span.Parent().SpanID().String() != "1234567890abcdef" || span.SpanContext().TraceID().String() != "0123456789abcdef0123456789abcdef" || !span.SpanContext().SpanID().IsValid() {
			t.Errorf("span metadata: name=%q kind=%v parent=%v context=%v", span.Name(), span.SpanKind(), span.Parent(), span.SpanContext())
		}
		assertNoPrivateSpanData(t, span, privateID, privateSegment, privateQuery)
	}
}

func assertNoPrivateSpanData(t *testing.T, span sdktrace.ReadOnlySpan, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(span.Name(), secret) {
			t.Errorf("span name contains private data: %q", span.Name())
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(string(attr.Key), secret) || strings.Contains(attr.Value.Emit(), secret) || attr.Key == "url.path" || attr.Key == "url.query" || attr.Key == "http.target" {
				t.Errorf("unsafe span attribute: %v", attr)
			}
		}
		for _, event := range span.Events() {
			if strings.Contains(event.Name, secret) {
				t.Errorf("unsafe span event: %v", event)
			}
			for _, attr := range event.Attributes {
				if strings.Contains(string(attr.Key), secret) || strings.Contains(attr.Value.Emit(), secret) {
					t.Errorf("unsafe event attribute: %v", attr)
				}
			}
		}
	}
}

func TestRecoverProblemAndSingleRequestLog(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSpanProcessor(spans))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	router := chi.NewRouter()
	router.Use(Recover(logger), Tracing(), Logging(logger), HTTPMiddleware(logger))
	router.Get("/private/{id}", func(http.ResponseWriter, *http.Request) { panic("secret-panic-value") })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/private/secret-id?token=secret-token", nil))
	if recorder.Code != http.StatusInternalServerError || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("response: %d %v", recorder.Code, recorder.Header())
	}
	if strings.Contains(recorder.Body.String(), "secret") || strings.Contains(logs.String(), "secret-panic-value") || strings.Contains(logs.String(), "secret-id") || strings.Contains(logs.String(), "secret-token") {
		t.Fatalf("sensitive details leaked: body=%q logs=%q", recorder.Body.String(), logs.String())
	}
	var body struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Status != 500 || body.Type != "about:blank" || body.Title != "Internal Server Error" || body.Code != "internal_error" {
		t.Fatalf("invalid problem: %+v %v", body, err)
	}
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended panic spans: %d, want 1", len(ended))
	}
	span := ended[0]
	assertNoPrivateSpanData(t, span, "secret-id", "secret-token", "secret-panic-value")
	requestLogs, panicLogs := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		switch event["msg"] {
		case "http request":
			requestLogs++
			if event["level"] != "ERROR" || event["http.response.status_code"] != float64(500) || event["http.route"] != "/private/{id}" || event["duration_ms"] == nil || event["trace_id"] == nil || event["span_id"] == nil {
				t.Errorf("request log: %v", event)
			}
		case "request panic":
			panicLogs++
			if event["stack"] == nil || event["trace_id"] != span.SpanContext().TraceID().String() || event["span_id"] != span.SpanContext().SpanID().String() || event["http.route"] != "/private/{id}" {
				t.Errorf("uncorrelated panic log: %v", event)
			}
		}
	}
	if requestLogs != 1 || panicLogs != 1 {
		t.Errorf("request logs: %d, panic logs: %d", requestLogs, panicLogs)
	}
}

func TestRecoverAbortsCommittedPartialResponse(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := chi.NewRouter()
	router.Use(Recover(logger), Tracing(), Logging(logger), HTTPMiddleware(logger))
	router.Get("/partial", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("partial"))
		panic("not a complete response")
	})
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Errorf("recoverer must abort committed connection, got %v", recovered)
		}
	}()
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/partial", nil))
}

func TestDBMetricsAndTripCounters(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(previous); _ = provider.Shutdown(context.Background()) })
	ctx := context.Background()
	TripCreated(ctx)
	TripCompleted(ctx)
	RecordDBOperation(ctx, "INSERT", "trips", 250*time.Millisecond, errors.New("do not export secret"))
	var data metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &data); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			seen[m.Name] = true
			switch m.Name {
			case "db.client.operation.duration":
				if m.Unit != "s" {
					t.Errorf("db unit: %s", m.Unit)
				}
				points := m.Data.(metricdata.Histogram[float64]).DataPoints
				if len(points) != 1 || points[0].Count != 1 || points[0].Sum != .25 {
					t.Errorf("db duration: %+v", points)
				}
				for _, attr := range points[0].Attributes.ToSlice() {
					if attr.Key == "error.type" && attr.Value.AsString() != "database_error" {
						t.Errorf("error attr: %v", attr)
					}
				}
			case "tripgo.trips.created", "tripgo.trips.completed":
				points := m.Data.(metricdata.Sum[int64]).DataPoints
				if len(points) != 1 || points[0].Value != 1 || points[0].Attributes.Len() != 0 {
					t.Errorf("trip count: %+v", points)
				}
			}
		}
	}
	for _, name := range []string{"db.client.operation.duration", "tripgo.trips.created", "tripgo.trips.completed"} {
		if !seen[name] {
			t.Errorf("missing %s", name)
		}
	}
}
