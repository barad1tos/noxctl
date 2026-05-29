package engine

// Content-hash gate primitives — sha256 over master + sorted hubs,
// plus the per-domain snapshot reader that powers the idempotency
// contract.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/regen"
	"github.com/barad1tos/noxctl/bear/state"
)

func applyFinalize(ctx context.Context, opts ApplyOpts, st *state.State, result *ApplyResult) (*ApplyResult, error) {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Interrupted = true
		// Leave LastApply at prior value; preserve InProgress as resume marker.
		// Save once more so on-disk state matches in-memory result.
		if saveErr := st.Save(opts.StatePath); saveErr != nil {
			log.Printf("apply: final state.Save (interrupted) failed: %v", saveErr)
		}
		if opts.WithMetrics {
			result.Metrics = bearcli.MetricsSnapshot()
		}
		return result, nil
	}
	if result.AnyFailed() {
		// Leave LastApply at prior value; preserve InProgress so the next apply
		// can warn that the prior run did not finish successfully.
		if saveErr := st.Save(opts.StatePath); saveErr != nil {
			log.Printf("apply: final state.Save (failed) failed: %v", saveErr)
		}
		if opts.WithMetrics {
			result.Metrics = bearcli.MetricsSnapshot()
		}
		return result, nil
	}
	result.CompletedAt = time.Now().UTC()
	st.LastApply = result.CompletedAt
	st.InProgress = state.InProgress{} // clear
	if err := st.Save(opts.StatePath); err != nil {
		return result, fmt.Errorf("engine.Apply state.Save(complete): %w", err)
	}
	if opts.WithMetrics {
		result.Metrics = bearcli.MetricsSnapshot()
	}
	return result, nil
}

// computeDomainHash returns sha256(strip(master) || NUL || sorted-by-title
// strip(hubs[i])) over the bodies regen.Run already fetched (D-02). The diff-
// check that decides created/changed/unchanged for each hub + the master has
// already read (or, on create/overwrite, deliberately read back) the canonical
// body — snap carries those stripped bytes, so a no-op cycle does ZERO extra
// reads here. Returns "" — so the caller preserves the PRIOR hash — when:
//   - the snapshot has no master body yet (transient initial setup), or
//   - the snapshot is Incomplete: a hub/master write succeeded but its
//     post-write read-back failed, so a body is missing (FIX-2). Hashing the
//     partial snapshot would write a wrong value and flip the domain "changed"
//     forever; preserving the prior hash defers the update one cycle.
func computeDomainHash(snap regen.DomainSnapshot) string {
	if snap.Master == "" || snap.Incomplete {
		return ""
	}
	return ComputeContentHash(snap.Master, snap.Hubs)
}

// ComputeContentHash returns sha256(strip(master) || NUL || sorted-by-title strip(hub_i)).
// Inputs are already stripped of new-note-link drift by the regen hub/master
// upsert path (which feeds regen.DomainSnapshot) — this function is pure: same
// input, same output. The plan engine still strips via regen.FetchMasterContent
// / regen.FetchHubContents before its own (non-hashing) diff comparison.
//
// Exported (rather than relying on a `computeContentHash` + in-package
// `export_test.go` test seam) because the project's test-location
// convention places external tests at `tests/bear/engine/`, a different
// directory from the package source — which means an in-package
// `_test.go` file cannot bridge unexported symbols across the
// directory gap. Exporting is the pragmatic resolution.
func ComputeContentHash(master string, hubs map[string]string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(master))
	titles := make([]string, 0, len(hubs))
	for t := range hubs {
		titles = append(titles, t)
	}
	sort.Strings(titles)
	for _, t := range titles {
		_, _ = h.Write([]byte{0}) // NUL separator
		_, _ = h.Write([]byte(hubs[t]))
	}
	return hex.EncodeToString(h.Sum(nil))
}
