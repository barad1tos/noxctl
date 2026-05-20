package bearcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/barad1tos/noxctl/bear/note"
	"github.com/barad1tos/noxctl/bear/overwriteoutcome"
)

// recordOutcome wires OverwriteWithRetry's three counter-relevant
// exit paths to overwriteoutcome.Record. The sub-package keeps the
// metric arithmetic testable from tests/bear/overwriteoutcome/
// without exposing pool metric internals across the public API.
func recordOutcome(outcome overwriteoutcome.Outcome) {
	overwriteoutcome.Record(
		outcome,
		&metrics.hashConflicts,
		&metrics.retriesOK,
		&metrics.retriesFail,
	)
}

// OverwriteWithRetry performs `bearcli overwrite --base <hash>` and
// retries once if bearcli rejects with ErrHashConflict (the note
// changed between our hash read and the write). The retry re-fetches
// the current hash before the second attempt — the only sensible
// recovery short of a full new regen.
//
// Increments the pool's hash-conflict counters via recordOutcome →
// overwriteoutcome.Record so the audit reporter can surface conflict
// rate and retry success ratio per regen cycle.
func OverwriteWithRetry(ctx context.Context, noteID, body string) error {
	hash, err := ShowHash(ctx, noteID)
	if err != nil {
		return err
	}
	_, err = Run(ctx, []string{"overwrite", noteID, FlagBase, hash}, body)
	if err == nil {
		recordOutcome(overwriteoutcome.NoConflict)
		return nil
	}
	if !errors.Is(err, ErrHashConflict) {
		return err
	}
	hash, err = ShowHash(ctx, noteID)
	if err != nil {
		recordOutcome(overwriteoutcome.RetryFail)
		return fmt.Errorf("retry-after-conflict: %w", err)
	}
	if _, err = Run(ctx, []string{"overwrite", noteID, FlagBase, hash}, body); err != nil {
		recordOutcome(overwriteoutcome.RetryFail)
		return fmt.Errorf("retry-after-conflict: %w", err)
	}
	recordOutcome(overwriteoutcome.RetrySucceed)
	return nil
}

// ShowHash returns the current optimistic-concurrency hash for the
// note. An empty string with no error from bearcli would silently
// disable concurrency guards — guard against that by treating empty
// hash as a fault.
func ShowHash(ctx context.Context, noteID string) (string, error) {
	out, err := Run(ctx, []string{"show", noteID, FlagFormat, FormatJSON, FlagFields, "hash"}, "")
	if err != nil {
		return "", fmt.Errorf("ShowHash(%s): %w", noteID, err)
	}
	var hashOnly note.Note
	if err = json.Unmarshal(out, &hashOnly); err != nil {
		return "", fmt.Errorf("ShowHash(%s): parse: %w", noteID, err)
	}
	if hashOnly.Hash == "" {
		return "", fmt.Errorf("ShowHash(%s): bearcli returned empty hash", noteID)
	}
	return hashOnly.Hash, nil
}
