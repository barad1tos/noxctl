// Package bear_test is the external test surface for the bear package
// (see grouped_vertical_test.go for the package doc-comment rationale).
//
// snapshot_test.go locks down the snapshot facade contract:
//
//  1. bear.SnapshotDomainRenderInputs returns (DomainRenderInputs{Groups:
//     non-nil empty map}, nil) for a Domain whose tag matches zero notes
//     in the live Bear corpus. Empty-but-non-nil is the contract the
//     engine.Plan core and the residue scanner both depend on — they
//     range over .Groups without nil-checks.
//  2. bear.LintUntracked exposes the wire value "untracked" — the
//     constant the residue emitter and the diff renderer both serialize
//     against.
//
// The empty-tag case exercises the live bearcli boundary by design: it
// is the smallest end-to-end shape that proves the facade calls
// listNotes → computeMasterOverrides → computeHubOverrides → groupAtomics
// in the same order Apply does (bear/core.go RunRegen, lines 939-989).
// On hosts without bearcli installed (CI containers, non-darwin) the
// test skips with a clear marker rather than spuriously failing.
package bear_test

import (
	"context"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// bearcliPath mirrors bear.runBearcli's hardcoded binary location
// (bear/domain.go:34). Tests that invoke any facade that ultimately
// exec's bearcli must skip when this path is missing — keeps the
// tests/bear/ suite green on hosts that don't have Bear.app installed.
const bearcliPath = "/Applications/Bear.app/Contents/MacOS/bearcli"

func skipIfNoBearcli(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("SnapshotDomainRenderInputs invokes bearcli (macOS-only); GOOS=%s", runtime.GOOS)
	}
	if _, err := os.Stat(bearcliPath); err != nil {
		t.Skipf("bearcli not installed at %s; skipping live-corpus snapshot test", bearcliPath)
	}
}

// TestSnapshotDomainRenderInputs locks the facade's empty-tag contract:
// a Domain whose tag yields zero notes from bearcli must produce
// DomainRenderInputs with Groups initialized to an empty map (not nil)
// and Notes nil-or-empty. The contract matters because ranges
// over.Groups without a nil-guard.
//
// Using a synthetic tag that no real note carries is deliberate — it
// gives a deterministic zero-row result whose shape doesn't depend on
// the operator's vault contents.
func TestSnapshotDomainRenderInputs(t *testing.T) {
	skipIfNoBearcli(t)

	// Deliberately-unused synthetic tag — no human would create
	// `noxctl/snapshot-empty-fixture-do-not-use` in their vault.
	d := &bear.Domain{
		Tag:          "noxctl/snapshot-empty-fixture-do-not-use",
		CanonicalTag: "#noxctl/snapshot-empty-fixture-do-not-use",
		IndexTitle:   "✱ Snapshot Fixture (test-only)",
		// ParseMeta + RenderMaster are nil; safe because zero-note
		// tags never reach detectAuthor or RenderMaster on this path.
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	snap, err := bear.SnapshotDomainRenderInputs(ctx, d)
	if err != nil {
		t.Fatalf("SnapshotDomainRenderInputs returned error for empty-tag domain: %v", err)
	}
	if snap.Groups == nil {
		t.Fatalf("Groups must be initialized to an empty map (not nil); downstream consumers range without nil-guards")
	}
	if got := len(snap.Groups); got != 0 {
		t.Fatalf("Groups must be empty for a tag with zero notes; got %d buckets: %v", got, keysOf(snap.Groups))
	}
	if got := len(snap.Notes); got != 0 {
		t.Fatalf("Notes must be empty for a tag with zero notes; got %d notes", got)
	}
}

// TestSnapshotDomainRenderInputs_LintUntrackedConstant locks the wire
// value (residue scanner) and (diff renderer) both
// encode against. Renaming the constant value silently breaks JSON-output
// stability for every downstream consumer.
func TestSnapshotDomainRenderInputs_LintUntrackedConstant(t *testing.T) {
	const want = "untracked"
	if got := string(bear.LintUntracked); got != want {
		t.Fatalf("string(bear.LintUntracked) = %q, want %q (wire format locked by Plans 03-03/04)", got, want)
	}
}

// keysOf returns the bucket-name keys of g sorted-alphabetically. Used
// only by failure messages so the operator sees a stable error string
// instead of a random-order map iteration.
func keysOf(g map[string][]bear.Note) []string {
	out := make([]string, 0, len(g))
	for k := range g {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
