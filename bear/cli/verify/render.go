package verify

import (
	"github.com/barad1tos/noxctl/bear/cli/diag"
)

// RenderForTest exposes the production renderer to the external test
// package. Delegates to diag.Render with verify's "verify" verb so the
// rendered text+JSON is byte-identical to what Run emits, and a
// signature change in the shared renderer trips the verify render tests.
func RenderForTest(opts Options, result *Result) error {
	return diag.Render(opts.Stdout, opts.Output, "verify", result)
}
