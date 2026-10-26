package observability

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// NewPGXTracer installs on pgxpool.Config.ConnConfig.Tracer before pool creation.
// otelpgx cannot safely instrument errors: every end callback records err.Error()
// as an exception event and a status description. This tracer preserves the
// query, batch, copy, prepare, connect and pool-acquire spans without exporting
// SQL, arguments, connection details, statement names or error messages.
func NewPGXTracer() pgx.QueryTracer {
	histogram, _ := otel.Meter(meterName).Float64Histogram("db.client.operation.duration", metric.WithUnit("s"))
	return &dbTracer{histogram: histogram, tracer: otel.Tracer(meterName)}
}

type dbTracer struct {
	histogram metric.Float64Histogram
	tracer    trace.Tracer
}

var (
	_ pgx.QueryTracer       = (*dbTracer)(nil)
	_ pgx.BatchTracer       = (*dbTracer)(nil)
	_ pgx.CopyFromTracer    = (*dbTracer)(nil)
	_ pgx.PrepareTracer     = (*dbTracer)(nil)
	_ pgx.ConnectTracer     = (*dbTracer)(nil)
	_ pgxpool.AcquireTracer = (*dbTracer)(nil)
)

type dbQueryStateKey struct{}

type dbSpanKey struct{}

type dbQueryState struct {
	started    time.Time
	operation  string
	collection string
	span       trace.Span
}

func (t *dbTracer) start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	// Match otelpgx's behavior: do not make orphan spans for untraced requests.
	if !trace.SpanFromContext(ctx).IsRecording() {
		return ctx, nil
	}
	return t.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attrs...))
}

func (t *dbTracer) startOther(ctx context.Context, name string, attrs ...attribute.KeyValue) context.Context {
	ctx, span := t.start(ctx, name, attrs...)
	if span == nil {
		return ctx
	}
	return context.WithValue(ctx, dbSpanKey{}, span)
}

func endSafe(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		span.SetAttributes(attribute.String("error.type", dbErrorType(err)))
		span.SetStatus(codes.Error, "")
	}
	// Never RecordError: its exception.message is the untrusted error string.
	span.End()
}

func (t *dbTracer) endOther(ctx context.Context, err error) {
	span, _ := ctx.Value(dbSpanKey{}).(trace.Span)
	endSafe(span, err)
}

func (t *dbTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	operation, collection := dbQueryLabels(data.SQL)
	attrs := []attribute.KeyValue{
		attribute.String("db.operation.name", operation),
		attribute.String("db.collection.name", collection),
	}
	ctx, span := t.start(ctx, "query "+operation+" "+collection, attrs...)
	return context.WithValue(ctx, dbQueryStateKey{}, dbQueryState{
		started: time.Now(), operation: operation, collection: collection, span: span,
	})
}

func (t *dbTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	state, ok := ctx.Value(dbQueryStateKey{}).(dbQueryState)
	if !ok {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("db.operation.name", state.operation),
		attribute.String("db.collection.name", state.collection),
	}
	if data.Err != nil {
		attrs = append(attrs, attribute.String("error.type", dbErrorType(data.Err)))
	}
	endSafe(state.span, data.Err)
	if t.histogram != nil {
		t.histogram.Record(ctx, time.Since(state.started).Seconds(), metric.WithAttributes(attrs...))
	}
}

func (t *dbTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return t.startOther(ctx, "batch")
}

func (t *dbTracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchQueryData) {
	operation, collection := dbQueryLabels(data.SQL)
	_, span := t.start(ctx, "query "+operation+" "+collection,
		attribute.String("db.operation.name", operation), attribute.String("db.collection.name", collection))
	endSafe(span, data.Err)
}

func (t *dbTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	t.endOther(ctx, data.Err)
}

func (t *dbTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	return t.startOther(ctx, "copy_from")
}

func (t *dbTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceCopyFromEndData) {
	t.endOther(ctx, data.Err)
}

func (t *dbTracer) TracePrepareStart(ctx context.Context, _ *pgx.Conn, data pgx.TracePrepareStartData) context.Context {
	operation, collection := dbQueryLabels(data.SQL)
	return t.startOther(ctx, "prepare "+operation+" "+collection,
		attribute.String("db.operation.name", operation), attribute.String("db.collection.name", collection))
}

func (t *dbTracer) TracePrepareEnd(ctx context.Context, _ *pgx.Conn, data pgx.TracePrepareEndData) {
	t.endOther(ctx, data.Err)
}

func (t *dbTracer) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	return t.startOther(ctx, "connect")
}

func (t *dbTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	t.endOther(ctx, data.Err)
}

func (t *dbTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return t.startOther(ctx, "pool.acquire")
}

func (t *dbTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	t.endOther(ctx, data.Err)
}

// Only names from this fixed vocabulary can become metric labels. In particular,
// SQL, arguments, identifiers and error messages never become attributes.
func dbQueryLabels(sql string) (operation, collection string) {
	operation, collection = "OTHER", "unknown"
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return
	}
	first := strings.ToUpper(fields[0])
	table := ""
	switch first {
	case "SELECT":
		operation = first
		for i := 1; i+1 < len(fields); i++ {
			if strings.EqualFold(fields[i], "FROM") {
				table = fields[i+1]
				break
			}
		}
	case "INSERT":
		operation = first
		if len(fields) >= 3 && strings.EqualFold(fields[1], "INTO") {
			table = fields[2]
		}
	case "UPDATE":
		operation = first
		if len(fields) >= 2 {
			table = fields[1]
		}
	case "DELETE":
		operation = first
		if len(fields) >= 3 && strings.EqualFold(fields[1], "FROM") {
			table = fields[2]
		}
	case "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "SET", "SHOW":
		operation = first
	}
	switch strings.ToLower(table) {
	case "trips", "public.trips":
		collection = "trips"
	case "trip_status_history", "public.trip_status_history":
		collection = "trip_status_history"
	}
	return
}
