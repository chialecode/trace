package bilibili

import "context"

type automaticContextKey struct{}

// WithAutomaticRequests marks scheduled checks, renewal and auto-cache work.
// Manual calls omit it and retain their explicit user-triggered behavior.
func WithAutomaticRequests(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, automaticContextKey{}, source)
}

func AutomaticRequestSource(ctx context.Context) string {
	source, _ := ctx.Value(automaticContextKey{}).(string)
	return source
}

func (c *Client) SetAutomaticRequestHandlers(guard func() error, stopped func(string)) {
	c.automationMu.Lock()
	c.automaticGuard, c.automaticStopped = guard, stopped
	c.automationMu.Unlock()
}

func (c *Client) ReportAutomaticError(ctx context.Context, err error) {
	source, _ := ctx.Value(automaticContextKey{}).(string)
	if source == "" || source == "subscription" {
		return
	}
	kind := KindOf(err)
	if kind != ErrKindRiskControl && kind != ErrKindRateLimited {
		return
	}
	c.automationMu.RLock()
	stopped := c.automaticStopped
	c.automationMu.RUnlock()
	if stopped != nil {
		stopped(kind)
	}
}

func (c *Client) allowAutomaticRequest(ctx context.Context) error {
	if source, _ := ctx.Value(automaticContextKey{}).(string); source == "" {
		return nil
	}
	c.automationMu.RLock()
	guard := c.automaticGuard
	c.automationMu.RUnlock()
	if guard != nil {
		return guard()
	}
	return nil
}
