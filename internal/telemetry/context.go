package telemetry

import "context"

// The request id and the caller's trace context are attached by HTTP
// middleware and read by the gateway, so they live here rather than in either
// package — the one place both already depend on.

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxParent
)

// WithRequestID attaches the id assigned to this request.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxRequestID, id)
}

// RequestIDFrom returns the request id, or "" outside a gateway request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// WithParent attaches a trace context parsed from an inbound traceparent.
func WithParent(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, ctxParent, sc)
}

// ParentFrom returns the caller's trace context. The zero value means this
// request starts a new trace.
func ParentFrom(ctx context.Context) SpanContext {
	sc, _ := ctx.Value(ctxParent).(SpanContext)
	return sc
}
