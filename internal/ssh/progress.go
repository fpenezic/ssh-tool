package ssh

import "context"

type progressKey struct{}

// WithProgress attaches a connect-progress callback to ctx, so code below
// Connect that only sees the context (the network-profile dialer, the
// shared bastion build) can say what the connect is waiting on.
func WithProgress(ctx context.Context, fn func(stage string)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, fn)
}

// ReportProgress sends stage to the callback on ctx, if there is one.
func ReportProgress(ctx context.Context, stage string) {
	if fn, ok := ctx.Value(progressKey{}).(func(string)); ok {
		fn(stage)
	}
}
