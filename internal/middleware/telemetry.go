package middleware

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/gofreego/openpay/internal/appcontext"

	"github.com/gofreego/goutils/logger"
)

// TraceMiddleLayer adds trace_id and span_id to every log line emitted inside a
// span. Without it, traces and logs are two separate investigations: you can
// see a request was slow, or read what it logged, but not join them.
//
// Register it alongside logger.RequestMiddleLayer at startup.
func TraceMiddleLayer(ctx context.Context, msg string, fields *logger.Fields) (context.Context, string, *logger.Fields) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ctx, msg, fields
	}
	fields.AddField("traceId", sc.TraceID().String())
	fields.AddField("spanId", sc.SpanID().String())
	return ctx, msg, fields
}

// annotateSpan records who made the request on the current span, so a trace can
// be searched by operator or by the request id a customer quoted.
func annotateSpan(ctx context.Context, caller appcontext.Caller) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("openpay.request_id", caller.RequestID)}
	if caller.UserID != "" {
		attrs = append(attrs, attribute.String("openpay.user_id", caller.UserID))
	}
	if caller.IdempotencyKey != "" {
		attrs = append(attrs, attribute.String("openpay.idempotency_key", caller.IdempotencyKey))
	}
	span.SetAttributes(attrs...)
}
