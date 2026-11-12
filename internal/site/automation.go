package site

import "context"

type automaticContextKey struct{}

// WithAutomaticRequests marks scheduled checks, renewal, and auto-cache work.
// Manual calls omit it and retain their explicit user-triggered behavior.
func WithAutomaticRequests(ctx context.Context, source string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, automaticContextKey{}, source)
}

// AutomaticRequestSource returns the automatic work source attached to ctx, or empty string for manual requests.
func AutomaticRequestSource(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	source, _ := ctx.Value(automaticContextKey{}).(string)
	return source
}
