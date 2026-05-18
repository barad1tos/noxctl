package bear_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// TestBearcliFixtures_FreshUntitled drives the fresh-clicked-untitled
// shape through the canonicaliser and asserts that the rendered body
// leads with a stamped datetime H1 (spec component 2). Locks the
// integration between bearcli JSON ingest → StampDatetimeH1 path →
// renderAtomicCanonical.
func TestBearcliFixtures_FreshUntitled(t *testing.T) {
	fixedNow := time.Date(2026, 5, 13, 15, 25, 0, 0, time.Local)
	bear.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	note := loadBearcliFixture(t, "fresh_untitled.json")
	d := bear.NewFlatListDomain("library/quotes", "✱ Quotes")
	d.UnknownBucket = "_unknown"

	out := bear.RenderAtomicCanonicalForTest(t, d, note.Title, "_unknown", note.Content)
	wantHead := "# 13 May 2026 at 15:25"
	if !strings.HasPrefix(out, wantHead+"\n") {
		t.Errorf("fresh-untitled atom did not stamp datetime H1:\n  got first line: %q\n  want: %q\n  full: %s",
			strings.SplitN(out, "\n", 2)[0], wantHead, out)
	}
}

// TestBearcliFixtures_UserAuthoredH1 asserts the user-edits-first
// contract (spec component 7): an atom whose body already has a user-
// typed H1 must NOT be re-stamped — the user's title wins.
func TestBearcliFixtures_UserAuthoredH1(t *testing.T) {
	fixedNow := time.Date(2026, 5, 13, 15, 25, 0, 0, time.Local)
	bear.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	note := loadBearcliFixture(t, "user_authored_h1.json")
	d := bear.NewFlatListDomain("library/quotes", "✱ Quotes")
	d.UnknownBucket = "_unknown"

	out := bear.RenderAtomicCanonicalForTest(t, d, note.Title, "_unknown", note.Content)
	if !strings.HasPrefix(out, "# My custom title\n") {
		t.Errorf("user H1 was overwritten:\n  got first line: %q\n  full: %s",
			strings.SplitN(out, "\n", 2)[0], out)
	}
	if strings.Contains(out, "13 May 2026 at 15:25") {
		t.Errorf("daemon stamped over user H1 — must not happen:\n%s", out)
	}
}

// TestBearcliFixtures_PreambleBody asserts non-tag-line preamble is
// preserved between H1 and the canonical tag-line (spec component 5).
func TestBearcliFixtures_PreambleBody(t *testing.T) {
	note := loadBearcliFixture(t, "preamble_body.json")
	d := bear.NewFlatListDomain("library/quotes", "✱ Quotes")
	d.UnknownBucket = "_unknown"

	out := bear.RenderAtomicCanonicalForTest(t, d, note.Title, "_unknown", note.Content)
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("output too short: %s", out)
	}
	if lines[0] != "# Existing title" {
		t.Errorf("H1 not preserved: %q", lines[0])
	}
	if lines[1] != "epigraph from another work" {
		t.Errorf("preamble not preserved: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "#library/quotes") {
		t.Errorf("tag-line not in expected position: %q", lines[2])
	}
}

// TestBearcliFixtures_LegacyStaleH1 documents the legacy `+`-encoded
// shape: parsing must not crash; rendering preserves the pre-existing
// stale H1 (atom-flavor lazy migration). The fixture itself documents
// the input shape — no auto-recovery happens (spec component 6: dropped).
func TestBearcliFixtures_LegacyStaleH1(t *testing.T) {
	note := loadBearcliFixture(t, "legacy_stale_h1.json")
	d := bear.NewFlatListDomain("library/quotes", "_unknown")
	d.UnknownBucket = "_unknown"

	out := bear.RenderAtomicCanonicalForTest(t, d, note.Title, "_unknown", note.Content)
	// Existing stale H1 IS a recognized H1 per spec recognition rules —
	// daemon leaves it alone. Manual cleanup is the path forward (spec
	// component 6: user manually cleared orphans).
	if !strings.HasPrefix(out, "# 8+May+2026+at+00:49\n") {
		t.Errorf("legacy stale H1 was rewritten unexpectedly (component 6: no auto-recovery):\n%s", out)
	}
}

func loadBearcliFixture(t *testing.T, name string) bear.Note {
	t.Helper()
	// findRepoRootFromTest is defined in new_note_regex_test.go (same
	// package bear_test) — reuse rather than duplicate.
	repoRoot := findRepoRootFromTest(t)
	path := filepath.Join(repoRoot, "tests", "bear", "testdata", "bearcli", "v1", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var note bear.Note
	if unmarshalErr := json.Unmarshal(data, &note); unmarshalErr != nil {
		t.Fatalf("unmarshal %s: %v", path, unmarshalErr)
	}
	return note
}
