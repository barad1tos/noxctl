package engine

import "log"

func applyLogSink(opts ApplyOpts) func(format string, args ...any) {
	if opts.LogSink != nil {
		return opts.LogSink
	}
	return log.Printf
}
