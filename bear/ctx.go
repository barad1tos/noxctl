package bear

import "context"

// CheckCtx returns ctx.Err without performing any other work. Used at
// the head of per-atom for-loops in the engine path so SIGINT response
// time is bounded by at most one bearcli call (BearcliTimeout=10s)
// instead of the full pre-pass duration. Tested in isolation in
// tests/bear/ctx_test.go without bearcli dependency.
//
// Yes, this is one line. The point is: every per-atom loop site uses
// the same call (grep-detectable), and changes to ctx-cancel discipline
// happen in ONE place.
func CheckCtx(ctx context.Context) error {
	return ctx.Err()
}
