package site

import "context"

type automaticContextKey struct{}
type manualProbeContextKey struct{}

// WithAutomaticRequests marks scheduled checks, renewal, and auto-cache work.
// Manual calls omit it and retain their explicit user-triggered behavior.
func WithAutomaticRequests(ctx context.Context, source string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, manualProbeContextKey{}, false)
	return context.WithValue(ctx, automaticContextKey{}, source)
}

// AutomaticRequestSource returns the automatic work source attached to ctx, or empty string for manual requests.
func AutomaticRequestSource(ctx context.Context) string {
	if ctx == nil || IsManualProbe(ctx) {
		return ""
	}
	source, _ := ctx.Value(automaticContextKey{}).(string)
	return source
}

// WithManualProbe marks an explicit user-initiated single-shot probe/retry request
// during or outside site backoff (DEC-089). It always clears any automatic source
// so downstream automatic backoff/quota guards never short-circuit the probe.
func WithManualProbe(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, automaticContextKey{}, "")
	return context.WithValue(ctx, manualProbeContextKey{}, true)
}

// IsManualProbe reports whether ctx carries an explicit user-triggered manual probe.
func IsManualProbe(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(manualProbeContextKey{}).(bool)
	return v
}
