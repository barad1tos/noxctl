package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
)

func TestApply_NoStateFileLoadsCleanly(t *testing.T) {
	dir := t.TempDir()
	opts := engine.ApplyOpts{
		Domains:   nil,
		Pins:      nil,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.AllFeaturesOn(),
	}
	result, err := engine.Apply(context.Background(), opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if result.Interrupted {
		t.Errorf("expected Interrupted=false on successful no-domain Apply")
	}
	if result.CompletedAt.IsZero() {
		t.Errorf("expected CompletedAt set on success, got zero")
	}
}

func TestApply_FeaturesGate_DisablesPrePass(t *testing.T) {
	dir := t.TempDir()
	opts := engine.ApplyOpts{
		Domains:   nil,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features: engine.Features{
			AutoTagDefault:    false,
			CrossDomainMoves:  false,
			TimePromotion:     false,
			ForeignTagEscape:  false,
			DuplicateRegistry: false,
		},
	}
	result, err := engine.Apply(context.Background(), opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := result.PrePasses["auto_tag"]; ok {
		t.Errorf("auto_tag pre-pass ran despite Features.AutoTagDefault=false")
	}
	if _, ok := result.PrePasses["foreign_tag"]; ok {
		t.Errorf("foreign_tag pre-pass ran despite Features.ForeignTagEscape=false")
	}
}

func TestApply_FeaturesGate_EnablesPrePass(t *testing.T) {
	dir := t.TempDir()
	opts := engine.ApplyOpts{
		Domains:   nil,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.AllFeaturesOn(),
		// Auto-tag fast-pass is now gated on a non-empty
		// DailyDefaultTag in addition to the feature flag — empty
		// tag means "operator omitted [meta].daily_default_tag" and
		// the spec is treated as disabled. Set a synthetic value so
		// AllFeaturesOn actually exercises every pre-pass row here.
		DailyDefaultTag: "stub/daily",
	}
	result, err := engine.Apply(context.Background(), opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, name := range []string{"foreign_tag", "auto_tag", "cross_domain", "time_promotion", "duplicate_registry"} {
		if _, ok := result.PrePasses[name]; !ok {
			t.Errorf("pre-pass %q missing from result.PrePasses", name)
		}
	}
}

// TestApply_AutoTagGatedOnDailyDefaultTag pins the "empty tag = silent
// disable" contract for the daily-default fast-pass while preserving
// placeholder-refresh independence:
//
//   - auto_tag is skipped when DailyDefaultTag is empty — earlier
//     wiring called ApplyDailyDefaultTag with a nil domain (map
//     lookup on empty key) which returned an error and stamped a
//     spurious Failed=1 in the result.
//   - placeholder_refresh must still run because ApplyPlaceholder
//     Refresh has no dependency on the daily tag — it iterates every
//     domain with a non-empty QuickPlaceholderH1. Folding the daily
//     gate into both passes would silently disable placeholder
//     refresh for catalogs that set `quick_placeholder_h1` on a
//     domain without declaring `[meta].daily_default_tag`.
func TestApply_AutoTagGatedOnDailyDefaultTag(t *testing.T) {
	dir := t.TempDir()
	opts := engine.ApplyOpts{
		Domains:   nil,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.AllFeaturesOn(),
		// DailyDefaultTag deliberately empty.
	}
	result, err := engine.Apply(context.Background(), opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := result.PrePasses["auto_tag"]; ok {
		t.Error("pre-pass \"auto_tag\" ran despite empty DailyDefaultTag")
	}
	if _, ok := result.PrePasses["placeholder_refresh"]; !ok {
		t.Error("pre-pass \"placeholder_refresh\" was skipped; it must stay on " +
			"Features.AutoTagDefault alone (independent of DailyDefaultTag)")
	}
}

func TestApply_StateOnSuccessClearsInProgress(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	opts := engine.ApplyOpts{
		Domains:   nil,
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{}, // all false to skip pre-passes that need bearcli
	}
	if _, err := engine.Apply(context.Background(), opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var st state.State
	if err = json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if st.InProgress.Verb != "" {
		t.Errorf("expected InProgress cleared after success, got Verb=%q", st.InProgress.Verb)
	}
	if st.LastApply.IsZero() {
		t.Errorf("expected LastApply set after success, got zero")
	}
}

func TestApply_ContentHashStable_StripsNewNoteLink(t *testing.T) {
	// ComputeContentHash is exported in package engine (see apply.go
	// docstring for the project-policy deviation that motivated
	// exporting rather than using a test-seam shim). The strip-of-
	// new-note-link happens inside bear.FetchMasterContent
	// (snapshot.go), NOT inside ComputeContentHash — so this test
	// verifies that ComputeContentHash is deterministic on already-
	// stripped inputs (the strip-then-hash discipline at the pipeline
	// boundary).
	masterStripped := "## Поезії\n[Author One]\n"
	h1 := engine.ComputeContentHash(masterStripped, nil)
	h2 := engine.ComputeContentHash(masterStripped, nil)
	if h1 != h2 {
		t.Errorf("content hash non-deterministic: %q vs %q", h1, h2)
	}
	// With one hub, hash differs from no-hub:
	h3 := engine.ComputeContentHash(masterStripped, map[string]string{"Hub A": "body"})
	if h1 == h3 {
		t.Errorf("hash should differ when hubs added; both %q", h1)
	}
}
