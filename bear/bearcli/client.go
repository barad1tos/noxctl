package bearcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ErrHashConflict is returned by Run when an `overwrite --base hash`
// call is rejected because the underlying note changed since we read
// its hash. Callers can `errors.Is` against this and decide to
// re-fetch + retry once.
var ErrHashConflict = errors.New("bear: optimistic hash mismatch")

// Timeout is the per-call deadline for bearcli subprocess
// invocations. Each call typically completes in <500ms; 10s tolerates
// pathological cases (Bear app paging in, sqlite-wal contention)
// without letting a hang freeze the daemon's regen pipeline
// indefinitely.
const Timeout = 10 * time.Second

// Bear CLI constants — single source of truth for command-line
// literals shared by every bearcli call site. Exported FlagBase and
// FormatJSON because callers outside this package (the note-aware
// wrappers in bearcli/notes.go and the overwrite path) need to build
// argument slices that match the same conventions.
const (
	binary = "/Applications/Bear.app/Contents/MacOS/bearcli"

	FlagFormat = "--format"
	FlagFields = "--fields"
	FlagBase   = "--base"
	FormatJSON = "json"

	// FieldsIDTitle is the bearcli --fields list for callers that
	// only need id + title (the cheapest read shape).
	FieldsIDTitle = "id,title"
	// FieldsAutoTag is the bearcli --fields list every auto-tag fast-
	// pass needs: ID + title + tag set + body.
	FieldsAutoTag = "id,title,tags,content"
)

// Run invokes the Bear CLI with args + optional stdin under the
// given context. The context is wrapped in a per-call Timeout so a
// hung bearcli invocation can't stall the daemon's regen pipeline.
// Detects the optimistic hash-mismatch case via stderr substring and
// returns ErrHashConflict so callers can retry with a fresh hash.
//
// Every call routes through acquire to honor the global concurrency
// semaphore. acquire happens BEFORE the per-call timeout so a long
// wait on the semaphore is bounded by ctx, not by Timeout —
// preventing a saturated pool from burning every goroutine's 10s
// timeout budget while still queued.
//
// Test seam: if a Backend is stamped on ctx (only test fixtures do
// this), the call is dispatched through the backend instead of the
// real exec.Command. The semaphore is acquired regardless so the
// metrics layer stays accurate even under fake-backed tests.
func Run(ctx context.Context, args []string, stdin string) ([]byte, error) {
	release, err := acquire(ctx, kindFromArgs(args))
	if err != nil {
		return nil, err
	}
	defer release()

	if backend := BackendFromContext(ctx); backend != nil {
		return backend.Run(ctx, args, stdin)
	}

	callCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, binary, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		stderr := errBuf.String()
		if strings.Contains(stderr, "hash") && strings.Contains(strings.ToLower(stderr), "mismatch") {
			return nil, fmt.Errorf("bearcli %v: %w (%s)", args, ErrHashConflict, strings.TrimSpace(stderr))
		}
		return nil, fmt.Errorf("bearcli %v failed: %w: %s", args, runErr, stderr)
	}
	return out.Bytes(), nil
}

// kindFromArgs classifies bearcli args by their sub-command (the
// first element) for the per-kind metrics counter. Unknown
// sub-commands fold into "other" — defensive, since today only the
// canonical eight are exercised but a future bearcli flag would
// surface as a known-unknown rather than a panic.
//
// `trash` (TrashNote) and `tag` (AddTag — Phase 13 orphan apply) are
// first-class kinds: prior to that wiring they collapsed into the
// "other" bucket and silently inflated the unknown-kind metric.
func kindFromArgs(args []string) string {
	if len(args) == 0 {
		return "other"
	}
	switch args[0] {
	case "list", "cat", "show", "overwrite", "create", "find", "trash", "tag":
		return args[0]
	}
	return "other"
}
