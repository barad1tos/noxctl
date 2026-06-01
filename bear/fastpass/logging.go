package fastpass

import (
	"context"
	"log"
)

type logSinkKey struct{}

// ContextWithLogSink installs a per-apply logging sink for fast-pass messages.
// A nil sink preserves the package default of logging through the process
// logger.
func ContextWithLogSink(ctx context.Context, sink func(format string, args ...any)) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, logSinkKey{}, sink)
}

func logf(ctx context.Context, format string, args ...any) {
	if sink, ok := ctx.Value(logSinkKey{}).(func(string, ...any)); ok && sink != nil {
		sink(format, args...)
		return
	}
	log.Printf(format, args...)
}
