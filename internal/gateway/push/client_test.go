package push

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"
)

func TestRequestPositionAccepted(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})
	tripID, driverID := uuid.New(), uuid.New()
	var received []uuid.UUID
	var traceparents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/api/v1/push" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected HTTP request: %s %s, content-type %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		traceparents = append(traceparents, r.Header.Get("Traceparent"))
		var payload struct {
			RequestID   uuid.UUID `json:"request_id"`
			RecipientID uuid.UUID `json:"recipient_id"`
			Kind        string    `json:"kind"`
			Data        struct {
				TripID uuid.UUID `json:"trip_id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if payload.RequestID == uuid.Nil || payload.RecipientID != driverID || payload.Data.TripID != tripID || payload.Kind != "REQUEST_POSITION" {
			t.Errorf("invalid payload: %+v", payload)
		}
		received = append(received, payload.RequestID)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("accepted"))
	}))
	defer server.Close()
	client, err := New(server.URL+"/prefix/", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef},
		SpanID:     trace.SpanID{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)
	for range 2 {
		if err := client.RequestPosition(ctx, tripID, driverID); err != nil {
			t.Fatal(err)
		}
	}
	if len(received) != 2 || received[0] == received[1] {
		t.Fatalf("expected distinct request UUIDs, received %v", received)
	}
	spans := exporter.GetSpans()
	if len(spans) != 2 || len(traceparents) != 2 {
		t.Fatalf("expected two child spans and traceparents, got %d spans and %d headers", len(spans), len(traceparents))
	}
	for i, span := range spans {
		if span.Name != "push.request_position" || span.SpanKind != trace.SpanKindClient || span.Parent.SpanID() != spanContext.SpanID() || span.SpanContext.TraceID() != spanContext.TraceID() || span.Status.Code == codes.Error {
			t.Errorf("unexpected client span: %+v", span)
		}
		parts := strings.Split(traceparents[i], "-")
		if len(parts) != 4 || parts[1] != spanContext.TraceID().String() || parts[2] != span.SpanContext.SpanID().String() || parts[2] == spanContext.SpanID().String() || parts[3] != "01" {
			t.Errorf("traceparent %q does not carry child span %s", traceparents[i], span.SpanContext.SpanID())
		}
	}
	if spans[0].SpanContext.SpanID() == spans[1].SpanContext.SpanID() {
		t.Error("separate requests reused a child span ID")
	}
}

func TestRequestPositionStatusErrorsNeverRetry(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, ErrUnexpectedStatus},
		{http.StatusOK, ErrUnexpectedStatus},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusServiceUnavailable, ErrUnavailable},
		{http.StatusBadGateway, ErrUnavailable},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("private error body"))
			}))
			defer server.Close()
			client, err := New(server.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			err = client.RequestPosition(context.Background(), uuid.New(), uuid.New())
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
			var statusErr *StatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != tt.status {
				t.Fatalf("expected status %d, got %v", tt.status, err)
			}
			if strings.Contains(err.Error(), "private error body") || calls.Load() != 1 {
				t.Fatalf("body leaked or retried: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestRequestPositionFailureSpanRedactsBody(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		_ = provider.Shutdown(context.Background())
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("private response body"))
	}))
	defer server.Close()
	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if err := client.RequestPosition(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected unavailable error: %v", err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Status.Code != codes.Error || spans[0].SpanKind != trace.SpanKindClient {
		t.Fatalf("expected errored client span: %+v", spans)
	}
	foundStatus := false
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "http.response.status_code" && attr.Value.AsInt64() == http.StatusServiceUnavailable {
			foundStatus = true
		}
		if strings.Contains(attr.Value.Emit(), "private response body") {
			t.Errorf("response body leaked into span attribute %s", attr.Key)
		}
	}
	for _, event := range spans[0].Events {
		for _, attr := range event.Attributes {
			if strings.Contains(attr.Value.Emit(), "private response body") {
				t.Errorf("response body leaked into span event %s", attr.Key)
			}
		}
	}
	if !foundStatus {
		t.Fatalf("response status missing from span: %+v", spans[0].Attributes)
	}
}

func TestRequestPositionTransportTimeoutAndContext(t *testing.T) {
	var calls atomic.Int32
	transport := NewMockRoundTripper(gomock.NewController(t))
	transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}).AnyTimes()
	client, err := New("http://push.example", 15*time.Millisecond, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	start := time.Now()
	err = client.RequestPosition(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, ErrTransport) || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("expected bounded transport timeout, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.RequestPosition(ctx, uuid.New(), uuid.New())
	if !errors.Is(err, ErrTransport) || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected propagated cancellation, got %v", err)
	}
	if calls.Load() > 2 {
		t.Fatalf("request was retried: %d calls for 2 invocations", calls.Load())
	}
}

type trackedBody struct {
	*strings.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestRequestPositionDrainsAndClosesResponse(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusBadRequest} {
		body := &trackedBody{Reader: strings.NewReader("response to drain")}
		transport := NewMockRoundTripper(gomock.NewController(t))
		transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body, Header: make(http.Header), Request: r}, nil
		})
		client, err := New("http://push.example", time.Second, &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		_ = client.RequestPosition(context.Background(), uuid.New(), uuid.New())
		if !body.closed || body.Len() != 0 {
			t.Errorf("status %d: response body not drained and closed", status)
		}
		client.CloseIdleConnections()
	}
}

type contextBody struct {
	ctx    context.Context
	closed bool
}

func (b *contextBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *contextBody) Close() error { b.closed = true; return nil }

func TestRequestPositionCapsResponseDrain(t *testing.T) {
	body := &trackedBody{Reader: strings.NewReader(strings.Repeat("x", maxResponseDrain+1))}
	transport := NewMockRoundTripper(gomock.NewController(t))
	transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusAccepted, Body: body, Header: make(http.Header), Request: r}, nil
	})
	client, err := New("http://push.example", time.Second, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if err := client.RequestPosition(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if !body.closed || body.Len() != 1 {
		t.Fatalf("response drain exceeded %d bytes or failed to close: remaining %d, closed %t", maxResponseDrain, body.Len(), body.closed)
	}
}

func TestRequestPositionTimeoutDuringBodyClosesResponse(t *testing.T) {
	var body *contextBody
	transport := NewMockRoundTripper(gomock.NewController(t))
	transport.EXPECT().RoundTrip(gomock.Any()).DoAndReturn(func(r *http.Request) (*http.Response, error) {
		body = &contextBody{ctx: r.Context()}
		return &http.Response{StatusCode: http.StatusAccepted, Body: body, Header: make(http.Header), Request: r}, nil
	})
	client, err := New("http://push.example", 15*time.Millisecond, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	err = client.RequestPosition(context.Background(), uuid.New(), uuid.New())
	if !errors.Is(err, ErrTransport) || !errors.Is(err, context.DeadlineExceeded) || body == nil || !body.closed {
		t.Fatalf("response body deadline must return transport error and close body: %v", err)
	}
}

func TestRequestPositionDoesNotFollowRedirect(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/another")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	err = client.RequestPosition(context.Background(), uuid.New(), uuid.New())
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusTemporaryRedirect || calls.Load() != 1 {
		t.Fatalf("redirect should be rejected without retry: %v, calls %d", err, calls.Load())
	}
}

func TestNewValidatesInputsWithoutChangingProvidedClient(t *testing.T) {
	for _, base := range []string{"", "ftp://push.example", "http://", "http://push.example/?x=1", "http://user:pass@push.example"} {
		if _, err := New(base, time.Second); err == nil {
			t.Errorf("accepted invalid base URL %q", base)
		}
	}
	if _, err := New("http://push.example", 0); err == nil {
		t.Error("accepted missing timeout")
	}
	transport := NewMockRoundTripper(gomock.NewController(t))
	original := &http.Client{Timeout: 2 * time.Minute, Transport: transport}
	client, err := New("http://push.example", time.Second, original)
	if err != nil || original.Timeout != 2*time.Minute || client.http.Timeout != time.Second {
		t.Fatalf("provided client mutated or timeout not applied: %v", err)
	}
}
