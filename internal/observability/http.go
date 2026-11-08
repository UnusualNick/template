package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	unmatchedRoute       = "unmatched"
	redactedPanicValue   = "[redacted]"
	panicIndexOutOfRange = "runtime error: index out of range"
)

// Recover runs outside Tracing, so it needs a request-scoped handoff to log
// the server span's IDs after the inner middleware unwinds on a panic.
type recoverySpanContextKey struct{}

// Recover catches panics and logs their type plus a safe runtime failure kind,
// redacting arbitrary values that can contain secrets or PII. Register it first
// (outermost) on chi. The optional writer keeps problem responses consistent.
func Recover(logger *slog.Logger, internalErrorWriter ...func(http.ResponseWriter, *http.Request)) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var spanContext trace.SpanContext
			r = r.WithContext(context.WithValue(r.Context(), recoverySpanContextKey{}, &spanContext))
			written := false
			response := httpsnoop.Wrap(w, httpsnoop.Hooks{
				WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
					return func(code int) { written = true; next(code) }
				},
				Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
					return func(body []byte) (int, error) { written = true; return next(body) }
				},
			})
			defer func() {
				if recovered := recover(); recovered != nil {
					attrs := []any{"http.request.method", r.Method, "http.route", route(r), "panic.type", fmt.Sprintf("%T", recovered), "panic.value", safePanicValue(recovered), "stack", string(debug.Stack())}
					if spanContext.IsValid() {
						attrs = append(attrs, "trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String())
					}
					logger.ErrorContext(r.Context(), "request panic", attrs...)
					// An already committed response cannot become a valid 500. Abort
					// the connection rather than deliver a truncated success body.
					if written {
						panic(http.ErrAbortHandler)
					}
					if len(internalErrorWriter) > 0 && internalErrorWriter[0] != nil {
						internalErrorWriter[0](w, r)
						return
					}
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(struct {
						Type   string `json:"type"`
						Title  string `json:"title"`
						Status int    `json:"status"`
						Code   string `json:"code"`
						Detail string `json:"detail"`
					}{Type: "about:blank", Title: http.StatusText(http.StatusInternalServerError), Status: http.StatusInternalServerError, Code: "internal_error", Detail: "An internal error occurred"})
				}
			}()
			next.ServeHTTP(response, r)
		})
	}
}

// safePanicValue records only recognized runtime failure kinds. The original
// error can include untrusted input (even when it implements runtime.Error), so
// neither arbitrary text nor index values become log attributes.
func safePanicValue(recovered any) string {
	runtimeErr, ok := recovered.(runtime.Error)
	if !ok {
		return redactedPanicValue
	}
	panicType := reflect.TypeOf(recovered)
	if panicType.Kind() == reflect.Pointer && reflect.ValueOf(recovered).IsNil() {
		return redactedPanicValue
	}
	for panicType.Kind() == reflect.Pointer {
		panicType = panicType.Elem()
	}
	if panicType.PkgPath() != "runtime" {
		return redactedPanicValue
	}
	message := runtimeErr.Error()
	for _, kind := range []string{
		panicIndexOutOfRange,
		"runtime error: slice bounds out of range",
		"runtime error: invalid memory address or nil pointer dereference",
		"runtime error: integer divide by zero",
		"runtime error: hash of unhashable type",
	} {
		if strings.HasPrefix(message, kind) {
			return kind
		}
	}
	return redactedPanicValue
}

// Tracing starts a server span without recording the raw URL. In particular,
// otelhttp's default url.path attribute is unsafe for private route segments.
// Propagation uses the configured global propagator (including remote parents).
func Tracing() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := otel.Tracer(meterName).Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer))
			if holder, ok := ctx.Value(recoverySpanContextKey{}).(*trace.SpanContext); ok {
				*holder = span.SpanContext()
			}
			r = r.WithContext(ctx)
			status := http.StatusOK
			wrapped := statusRecorder(w, &status)
			defer func() {
				panicked := recover()
				if panicked != nil {
					status = http.StatusInternalServerError
				}
				if pattern := route(r); pattern != unmatchedRoute {
					span.SetName(r.Method + " " + pattern)
					span.SetAttributes(attribute.String("http.route", pattern))
				}
				span.SetAttributes(attribute.String("http.request.method", r.Method), attribute.Int("http.response.status_code", status))
				if status >= http.StatusInternalServerError {
					span.SetStatus(codes.Error, http.StatusText(status))
				}
				span.End()
				if panicked != nil {
					panic(panicked)
				}
			}()
			next.ServeHTTP(wrapped, r)
		})
	}
}

// Logging emits only the method, route template, status, duration and trace ID.
// It never emits raw paths, query strings, headers, request bodies or panic values.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			status := http.StatusOK
			wrapped := statusRecorder(w, &status)
			defer func() {
				panicked := recover()
				if panicked != nil {
					status = http.StatusInternalServerError
				}
				attrs := []any{"http.request.method", r.Method, "http.route", route(r), "http.response.status_code", status, "duration_ms", float64(time.Since(started)) / float64(time.Millisecond)}
				if sc := trace.SpanFromContext(r.Context()).SpanContext(); sc.IsValid() {
					attrs = append(attrs, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
				}
				if status >= http.StatusInternalServerError {
					logger.ErrorContext(r.Context(), "http request", attrs...)
				} else {
					logger.InfoContext(r.Context(), "http request", attrs...)
				}
				if panicked != nil {
					panic(panicked)
				}
			}()
			next.ServeHTTP(wrapped, r)
		})
	}
}

// HTTPMiddleware records server duration in seconds and active requests as an
// up/down counter. Use it within chi's router.Use chain so route templates are
// available after dispatch. For the active-request label we match against
// chi's route tree before dispatch without executing a handler.
func HTTPMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	meter := otel.Meter(meterName)
	duration, durationErr := meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	if durationErr != nil {
		logger.Warn("http duration instrument unavailable", "error", durationErr)
	}
	active, activeErr := meter.Int64UpDownCounter("http.server.active_requests", metric.WithUnit("{request}"))
	if activeErr != nil {
		logger.Warn("http active-request instrument unavailable", "error", activeErr)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			initial := []attribute.KeyValue{attribute.String("http.request.method", r.Method), attribute.String("http.route", routeBeforeDispatch(r))}
			if active != nil {
				active.Add(r.Context(), 1, metric.WithAttributes(initial...))
			}
			started := time.Now()
			status := http.StatusOK
			wrapped := statusRecorder(w, &status)
			defer func() {
				panicked := recover()
				if panicked != nil {
					status = http.StatusInternalServerError
				}
				span := trace.SpanFromContext(r.Context())
				if pattern := route(r); pattern != unmatchedRoute {
					span.SetAttributes(attribute.String("http.route", pattern))
					span.SetName(r.Method + " " + pattern)
				}
				if status >= http.StatusInternalServerError {
					span.SetStatus(codes.Error, http.StatusText(status))
					span.SetAttributes(attribute.String("error.type", "internal_error"))
				}
				if active != nil {
					active.Add(r.Context(), -1, metric.WithAttributes(initial...))
				}
				if duration != nil {
					duration.Record(r.Context(), time.Since(started).Seconds(), metric.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("http.route", route(r)), attribute.Int("http.response.status_code", status)))
				}
				if panicked != nil {
					panic(panicked)
				}
			}()
			next.ServeHTTP(wrapped, r)
		})
	}
}

func statusRecorder(w http.ResponseWriter, status *int) http.ResponseWriter {
	written := false
	return httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				if !written {
					*status = code
					written = true
				}
				next(code)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(body []byte) (int, error) {
				if !written {
					*status = http.StatusOK
					written = true
				}
				return next(body)
			}
		},
	})
}

// routeBeforeDispatch resolves a template for the in-flight gauge. Match uses
// an independent scratch context so it does not consume the real route state.
func routeBeforeDispatch(r *http.Request) string {
	ctx := chi.RouteContext(r.Context())
	if ctx == nil || ctx.Routes == nil {
		return unmatchedRoute
	}
	matched := chi.NewRouteContext()
	if ctx.Routes.Match(matched, r.Method, r.URL.Path) {
		if pattern := matched.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return unmatchedRoute
}

func route(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	if ctx := chi.RouteContext(r.Context()); ctx != nil {
		if pattern := ctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return unmatchedRoute
}
