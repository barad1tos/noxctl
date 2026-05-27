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

	"github.com/barad1tos/noxctl/bear/domain"
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
			result.Metrics = domain.BearcliMetricsSnapshot()
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
			result.Metrics = domain.BearcliMetricsSnapshot()
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
		result.Metrics = domain.BearcliMetricsSnapshot()
	}
	return result, nil
}

// computeDomainHash reads the freshest master + hub bytes from Bear
// for one domain and returns sha256(strip(master) || NUL ||
// sorted-by-title strip(hubs[i])). Returns "" on read failure
// (logged but non-fatal — caller preserves prior hash).
func computeDomainHash(ctx context.Context, d *domain.Domain) string {
	master, hubs, err := snapshotDomainContent(ctx, d)
	if err != nil {
		log.Printf("apply: snapshot(%s) failed: %v (hash unchanged)", d.Tag, err)
		return ""
	}
	return ComputeContentHash(master, hubs)
}

// snapshotDomainContent fetches the post-RunRegen master + hub bytes
// for one domain via the exported domain.FetchMasterContent /
// domain.FetchHubContents wrappers (which in turn call the bearcli
// boundary inside package domain). Stripped of the [Нова нотатка]
// new-note link drift before return — caller can hash directly.
//
// Returns ("", nil, nil) for domains without a master note yet
// (transient state during initial setup); caller treats this as
// "skip the hash update, preserve prior" rather than overwriting
// with "".
func snapshotDomainContent(
	ctx context.Context,
	d *domain.Domain,
) (master string, hubs map[string]string, err error) {
	master, masterErr := domain.FetchMasterContent(ctx, d)
	if masterErr != nil {
		return "", nil, fmt.Errorf("snapshotDomainContent(%s) master: %w", d.Tag, masterErr)
	}
	hubs, hubsErr := domain.FetchHubContents(ctx, d)
	if hubsErr != nil {
		return "", nil, fmt.Errorf("snapshotDomainContent(%s) hubs: %w", d.Tag, hubsErr)
	}
	return master, hubs, nil
}

// ComputeContentHash returns sha256(strip(master) || NUL || sorted-by-title strip(hub_i)).
// Inputs are already stripped of new-note-link drift by
// snapshotDomainContent — this function is pure: same input, same
// output.
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
