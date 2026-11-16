// Package push sends position requests to the Push Service over HTTP.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName       = "github.com/UnusualNick/template/internal/gateway/push"
	maxResponseDrain = 64 << 10
)

var (
	ErrTransport        = errors.New("push service transport failure")
	ErrUnexpectedStatus = errors.New("push service rejected request")
	ErrRateLimited      = errors.New("push service rate limited request")
	ErrUnavailable      = errors.New("push service unavailable")
)

// StatusError reports an unsuccessful HTTP response without retaining its body.
// Use errors.Is to distinguish rate limits, unavailability, and other rejections.
type StatusError struct {
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("push service returned HTTP %d", e.StatusCode)
}

func (e *StatusError) Unwrap() error {
	switch {
	case e.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case e.StatusCode >= http.StatusInternalServerError:
		return ErrUnavailable
	default:
		return ErrUnexpectedStatus
	}
}

// Client owns its HTTP client configuration. CloseIdleConnections releases idle
// connections when the gateway is shut down; it does not close in-flight calls.
type Client struct {
	endpoint string
	http     *http.Client
}

// New constructs a gateway with a required timeout. A supplied HTTP client is
// copied rather than modified, so callers may inject a test transport safely.
// Redirects are not followed: each RequestPosition makes exactly one HTTP call.
func New(baseURL string, timeout time.Duration, optionalClient ...*http.Client) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("push service timeout must be positive")
	}
	if len(optionalClient) > 1 {
		return nil, errors.New("push service accepts at most one HTTP client")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("push service base URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/api/v1/push"
	base.RawPath = ""

	var httpClient http.Client
	if len(optionalClient) == 1 && optionalClient[0] != nil {
		httpClient = *optionalClient[0]
	} else {
		// A private transport keeps this gateway's idle connections independent
		// of the process-wide default HTTP client.
		httpClient.Transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	httpClient.Timeout = timeout
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: base.String(), http: &httpClient}, nil
}

// RequestPosition asks Push Service to request the driver's position for a trip.
// It does not retry, including on rate limits and temporary unavailability.
func (c *Client) RequestPosition(ctx context.Context, tripID, driverID uuid.UUID) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "push.request_position", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	span.SetAttributes(attribute.String("http.request.method", http.MethodPost))

	requestID, err := uuid.NewRandom()
	if err != nil {
		recordSpanFailure(span, "request_preparation")
		return fmt.Errorf("create push request ID: %w", err)
	}
	body, err := json.Marshal(struct {
		RequestID   uuid.UUID `json:"request_id"`
		RecipientID uuid.UUID `json:"recipient_id"`
		Kind        string    `json:"kind"`
		Data        struct {
			TripID uuid.UUID `json:"trip_id"`
		} `json:"data"`
	}{
		RequestID:   requestID,
		RecipientID: driverID,
		Kind:        "REQUEST_POSITION",
		Data: struct {
			TripID uuid.UUID `json:"trip_id"`
		}{TripID: tripID},
	})
	if err != nil {
		recordSpanFailure(span, "request_preparation")
		return fmt.Errorf("encode push request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		recordSpanFailure(span, "request_preparation")
		return fmt.Errorf("create push HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	// The gateway must propagate W3C trace context even if observability.Init
	// has not yet configured the global propagator.
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := c.http.Do(req)
	if err != nil {
		recordSpanFailure(span, "transport")
		return fmt.Errorf("%w: %w", ErrTransport, err)
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	// Drain up to a bounded limit for connection reuse. A larger response will
	// be discarded by closing its body; neither it nor the URL is recorded.
	_, drainErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseDrain))
	if resp.StatusCode != http.StatusAccepted {
		statusErr := &StatusError{StatusCode: resp.StatusCode}
		switch statusErr.Unwrap() {
		case ErrRateLimited:
			recordSpanFailure(span, "rate_limited")
		case ErrUnavailable:
			recordSpanFailure(span, "unavailable")
		default:
			recordSpanFailure(span, "unexpected_status")
		}
		return statusErr
	}
	if drainErr != nil {
		recordSpanFailure(span, "transport")
		return fmt.Errorf("%w: drain response: %w", ErrTransport, drainErr)
	}
	return nil
}

// A fixed error category prevents downstream response bodies, URLs, and IDs
// from entering the telemetry stream through exception attributes.
func recordSpanFailure(span trace.Span, category string) {
	span.SetAttributes(attribute.String("error.type", category))
	span.RecordError(errors.New(category))
	span.SetStatus(codes.Error, category)
}

// CloseIdleConnections releases idle connections owned by this client.
func (c *Client) CloseIdleConnections() {
	c.http.CloseIdleConnections()
}
