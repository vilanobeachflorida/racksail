package store

import "context"

type readerKey struct{}

// WithReader returns a context carrying the state a change is planned
// against. The change pipeline gives it to a kind's validation, which can
// then look at other resources as the change set leaves them, such as
// whether a server still has an admin once a user's roles change.
func WithReader(ctx context.Context, r Reader) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, readerKey{}, r)
}

// ReaderFrom returns the Reader WithReader put in ctx.
func ReaderFrom(ctx context.Context) (Reader, bool) {
	r, ok := ctx.Value(readerKey{}).(Reader)
	return r, ok && r != nil
}
