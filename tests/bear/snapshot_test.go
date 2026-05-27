// Package bear_test is the external test surface for the bear package.
//
// snapshot_test.go locks down the snapshot facade contract:
//
//  1. regen.SnapshotDomainRenderInputs returns (RenderInputs{Groups:
//     non-nil empty map}, nil) for a Domain whose tag matches zero notes
//     from the bearcli list boundary. Empty-but-non-nil is the contract the
//     engine.Plan core and the residue scanner both depend on — they
//     range over .Groups without nil-checks.
//  2. audit.LintUntracked exposes the wire value "untracked" — the
//     constant the residue emitter and the diff renderer both serialize
//     against.
//
// The empty-tag case exercises the bearcli boundary through a fake backend:
// it is the smallest hermetic shape that proves the facade calls
// listNotes → computeMasterOverrides → computeHubOverrides → groupAtomics
// in the same order Apply does (bear/regen/run.go::Run).
package bear_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/regen"
)

// TestSnapshotDomainRenderInputs locks the facade's empty-tag contract:
// a Domain whose tag yields zero notes from bearcli must produce
// RenderInputs with Groups initialized to an empty map (not nil)
// and Notes nil-or-empty. The contract matters because ranges
// over.Groups without a nil-guard.
//
// Using a synthetic tag that no real note carries is deliberate — it
// gives a deterministic zero-row result whose shape doesn't depend on
// the operator's vault contents.
func TestSnapshotDomainRenderInputs(t *testing.T) {
	// Deliberately-unused synthetic tag — no human would create
	// `noxctl/snapshot-empty-fixture-do-not-use` in their vault.
	d := &domain.Domain{
		Tag:          "noxctl/snapshot-empty-fixture-do-not-use",
		CanonicalTag: "#noxctl/snapshot-empty-fixture-do-not-use",
		IndexTitle:   "✱ Snapshot Fixture (test-only)",
		// ParseMeta + RenderMaster are nil; safe because zero-note
		// tags never reach DetectAuthor or RenderMaster on this path.
	}

	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	backend := snapshotBackend{t: t, tag: d.Tag}
	ctx := bearcli.ContextWithBackend(context.Background(), backend)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	snap, err := regen.SnapshotDomainRenderInputs(ctx, d)
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

type snapshotBackend struct {
	t   *testing.T
	tag string
}

func (s snapshotBackend) Run(_ context.Context, arguments []string, standardInput string) ([]byte, error) {
	s.t.Helper()
	if standardInput != "" {
		return nil, fmt.Errorf("snapshot backend received stdin %q, want empty", standardInput)
	}
	wantArguments := []string{
		"list",
		"--tag",
		s.tag,
		"--format",
		"json",
		"--fields",
		"id,title,content,tags,created",
	}
	if !slices.Equal(arguments, wantArguments) {
		return nil, fmt.Errorf("snapshot backend args = %v, want %v", arguments, wantArguments)
	}
	return []byte("[]"), nil
}

// TestSnapshotDomainRenderInputs_LintUntrackedConstant locks the
// underlying string value of `audit.LintUntracked` against a
// regression where a future rename of the constant silently shifts
// what every operator-facing renderer and JSON-marshal call site
// would emit downstream.
//
// Asserted via json.Marshal of a local struct so the comparison
// runs at runtime — a direct `string(audit.LintUntracked) == "x"`
// check would be constant-folded by the compiler under the current
// value and trip IntelliJ's always-false inspection. The local
// struct's `json:"category"` tag is purely a wire-format harness;
// `audit.Finding` itself does not yet serialize to JSON in
// production paths, so the test locks the constant value (not a
// shipping wire schema).
func TestSnapshotDomainRenderInputs_LintUntrackedConstant(t *testing.T) {
	const wantWire = `"category":"untracked"`
	payload := struct {
		Category audit.LintCategory `json:"category"`
	}{Category: audit.LintUntracked}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !bytes.Contains(encoded, []byte(wantWire)) {
		t.Fatalf("audit.LintUntracked must JSON-marshal containing %s; got %s", wantWire, encoded)
	}
}

// keysOf returns the bucket-name keys of g sorted-alphabetically. Used
// only by failure messages so the operator sees a stable error string
// instead of a random-order map iteration.
func keysOf(g map[string][]domain.Note) []string {
	out := make([]string, 0, len(g))
	for k := range g {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
