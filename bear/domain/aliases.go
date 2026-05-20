package domain

import (
	"context"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// This file holds backward-compatible aliases that surface the
// bearcli sub-package's API under the bear package name. Existing
// call sites in engine/, cli/*, and tests use the bear.* spelling;
// keeping the alias layer means PR-H2 ships without forcing every
// caller to migrate in lock-step.
//
// Aliases come in three shapes:
//   - type aliases (`type BearcliBackend = bearcli.Backend`)
//   - value aliases (sentinel error variable bound through assignment)
//   - thin function wrappers that delegate to bearcli.* (one line each)
//
// Future PRs can flip individual call sites from bear.X to bearcli.X
// and the alias eventually becomes dead — at that point this file
// shrinks naturally.

// Internal lowercase aliases for bearcli command-line constants.
// Existing call sites in bear/* use the unexported names (flagFormat,
// formatJSON, etc.) and the file count makes a rename churn-y; the
// aliases keep them working without touching every line.
const (
	flagFormat    = bearcli.FlagFormat
	flagFields    = bearcli.FlagFields
	formatJSON    = bearcli.FormatJSON
	fieldsIDTitle = bearcli.FieldsIDTitle
	fieldsAutoTag = bearcli.FieldsAutoTag
)

// IsAuxNote reports whether a note is an auto-generated master or
// hub (true) versus an operator-authored atom (false). Mirrors the
// classifier the regen pipeline uses to decide which notes to skip
// during groupAtomics. CLI helpers (`noxctl destroy`) use this to
// split a tag's note set into "trash these" vs "strip canonical".
//
// Lives here (vs bearcli/) because the classifier needs a *Domain
// receiver — bearcli is type-agnostic and must not import bear.
func IsAuxNote(d *Domain, n Note) bool {
	return d.skipNote(n)
}

// BearcliBackend is the backward-compatible alias for bearcli.Backend.
type BearcliBackend = bearcli.Backend

// BearcliMetrics is the backward-compatible alias for bearcli.Metrics.
type BearcliMetrics = bearcli.Metrics

// BearcliTimeout exposes bearcli.Timeout under the original name. The
// alias preserves the type (time.Duration) so existing comparisons
// like `ctx.WithTimeout(ctx, BearcliTimeout)` keep working.
const BearcliTimeout = bearcli.Timeout

// ErrHashConflict is the sentinel returned by the overwrite path when
// bearcli rejects the write because the underlying note changed since
// the hash was read. Bound to bearcli.ErrHashConflict for compat.
var ErrHashConflict = bearcli.ErrHashConflict

// SetBearcliConcurrency forwards to bearcli.SetConcurrency.
func SetBearcliConcurrency(n int) { bearcli.SetConcurrency(n) }

// ResetBearcliPoolForTest forwards to bearcli.ResetPoolForTest.
func ResetBearcliPoolForTest(n int) { bearcli.ResetPoolForTest(n) }

// ResetBearcliMetrics forwards to bearcli.ResetMetrics.
func ResetBearcliMetrics() { bearcli.ResetMetrics() }

// AcquireBearcliForTest forwards to bearcli.AcquireForTest.
func AcquireBearcliForTest(ctx context.Context, kind string) (func(), error) {
	return bearcli.AcquireForTest(ctx, kind)
}

// BearcliMetricsSnapshot forwards to bearcli.MetricsSnapshot.
func BearcliMetricsSnapshot() BearcliMetrics { return bearcli.MetricsSnapshot() }

// ContextWithBackend forwards to bearcli.ContextWithBackend.
func ContextWithBackend(parent context.Context, backend BearcliBackend) context.Context {
	return bearcli.ContextWithBackend(parent, backend)
}

// BackendFromContext forwards to bearcli.BackendFromContext.
func BackendFromContext(ctx context.Context) BearcliBackend {
	return bearcli.BackendFromContext(ctx)
}

// ListNotesForTag forwards to bearcli.ListNotesForTag, returning the
// alias-form []Note ([]bear.Note == []note.Note).
func ListNotesForTag(ctx context.Context, tag string) ([]Note, error) {
	return bearcli.ListNotesForTag(ctx, tag)
}

// TrashNote forwards to bearcli.TrashNote.
func TrashNote(ctx context.Context, noteID string) error {
	return bearcli.TrashNote(ctx, noteID)
}

// OverwriteNoteContent forwards to bearcli.OverwriteNoteContent.
func OverwriteNoteContent(ctx context.Context, noteID, body string) error {
	return bearcli.OverwriteNoteContent(ctx, noteID, body)
}
