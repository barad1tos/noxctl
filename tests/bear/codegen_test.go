// Package bear_test holds the codegen byte-equality acceptance suite.
//
// These tests anchor the D-08 codegen contract: the one-shot
// cmd/noxctl-codegen tool walks registry.All and re-emits
// examples/roman.toml byte-for-byte, so the dispatch round-trip is
// provably lossless. When D-12 deletes the hardcoded packages,
// this entire file is removed in the same atomic commit — it tests
// doomed code.
package bear_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/barad1tos/noxctl/bear/config"
)

// expectedDomainCount mirrors registry.All — 27 leaves + 4 umbrellas.
// Hardcoded so a regression that adds/removes a domain without updating
// MIGRATION.md (per D-14) surfaces here instead of silently
// rolling forward.
const expectedDomainCount = 31

// expectedCustomRenderers locks the closed set of D-06 custom
// renderers. Sorted lexically because TestCodegenCustomBlueprintsPresent
// collects observed names through sort.Strings before comparing.
var expectedCustomRenderers = []string{"agents", "lyrics", "quotes"}

// TestCodegenMatchesExamplesRomanToml is the byte-equality acceptance
// gate from D-08 — codegen output IS examples/roman.toml. RED
// gate until Task 3 of plan 04-03 regenerates the corpus from the new
// codegen tool; GREEN afterwards.
//
// Failure mode prints a unified-ish line diff (first 40 lines around the
// first divergence) so the offender is immediately legible in CI logs.
func TestCodegenMatchesExamplesRomanToml(t *testing.T) {
	got := runCodegen(t)
	wantPath := filepath.Join(projectRoot(t), "examples", "roman.toml")
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	if bytes.Equal(got, want) {
		return
	}
	offset := firstDiffOffset(got, want)
	t.Errorf("codegen output diverges from %s at byte %d\n"+
		"  got  bytes: %d\n  want bytes: %d\n"+
		"--- diff (first divergent region) ---\n%s",
		wantPath, offset, len(got), len(want), nearbyDiff(got, want, offset))
}

// TestCodegenStanzaCount decodes the codegen output and asserts the
// expected total against registry.All's contract (27 leaves + 4
// umbrellas = 31). Decoupled from byte-equality so an encoder
// formatting regression doesn't mask a genuine count drift.
func TestCodegenStanzaCount(t *testing.T) {
	got := runCodegen(t)
	var cat config.Catalog
	if _, err := toml.Decode(string(got), &cat); err != nil {
		t.Fatalf("toml.Decode codegen output: %v", err)
	}
	if len(cat.Domains) != expectedDomainCount {
		t.Errorf("len(cat.Domains) = %d, want %d", len(cat.Domains), expectedDomainCount)
	}
}

// TestCodegenCustomBlueprintsPresent asserts the three D-06 custom
// renderers (lyrics, quotes, agents) appear exactly once each in the
// codegen output with the correct renderer name. Locks the closed-
// catalog gate: adding a 4th custom renderer requires updating both
// expectedCustomRenderers and the codegen detection heuristic, same
// gate as adding a new blueprint.
func TestCodegenCustomBlueprintsPresent(t *testing.T) {
	got := runCodegen(t)
	var cat config.Catalog
	if _, err := toml.Decode(string(got), &cat); err != nil {
		t.Fatalf("toml.Decode codegen output: %v", err)
	}
	observed := collectCustomRenderers(cat.Domains)
	if len(observed) != len(expectedCustomRenderers) {
		t.Errorf("custom-blueprint count = %d (%v), want %d (%v)",
			len(observed), observed, len(expectedCustomRenderers), expectedCustomRenderers)
		return
	}
	for index, name := range observed {
		if name != expectedCustomRenderers[index] {
			t.Errorf("observed[%d] = %q, want %q", index, name, expectedCustomRenderers[index])
		}
	}
}

// collectCustomRenderers walks the decoded stanzas, picks every one
// with Blueprint == "custom" + non-nil Renderer, and returns the names
// in stable lexical order so the assertion is deterministic.
func collectCustomRenderers(stanzas []config.Stanza) []string {
	var names []string
	for _, s := range stanzas {
		if s.Blueprint != "custom" {
			continue
		}
		if s.Renderer == nil {
			names = append(names, "<nil>")
			continue
		}
		names = append(names, *s.Renderer)
	}
	sortStrings(names)
	return names
}

// sortStrings keeps the test imports tight (no extra "sort" import
// pulled in just for this one-line helper). Insertion sort is fine for
// a 3-element slice.
func sortStrings(s []string) {
	for index := 1; index < len(s); index++ {
		for jdx := index; jdx > 0 && s[jdx] < s[jdx-1]; jdx-- {
			s[jdx], s[jdx-1] = s[jdx-1], s[jdx]
		}
	}
}

// runCodegen invokes `go run./cmd/noxctl-codegen -output -` from the
// project root and returns its stdout. The choice to shell out (rather
// than import the codegen tool's main package — illegal in Go anyway)
// matches the project's e2e convention at tests/bear/e2e/. Stderr is
// surfaced on failure so a codegen bug points at the cause immediately.
func runCodegen(t *testing.T) []byte {
	t.Helper()
	root := projectRoot(t)
	cmd := exec.Command("go", "run", "./cmd/noxctl-codegen", "-output", "-")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run ./cmd/noxctl-codegen: %v\nstderr:\n%s", err, stderr.String())
	}
	return stdout.Bytes()
}

// projectRoot locates the repository root by walking up from the test
// file's directory until a `go.mod` is found. Necessary because `go
// test` sets CWD to the test package's directory, not the module root.
func projectRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// firstDiffOffset returns the byte offset of the first differing byte,
// or min(len(a), len(b)) when one is a prefix of the other.
func firstDiffOffset(a, b []byte) int {
	limit := min(len(a), len(b))
	for index := range limit {
		if a[index] != b[index] {
			return index
		}
	}
	return limit
}

// nearbyDiff renders the 5 lines preceding and 5 lines following the
// diff offset for both inputs — enough context to identify the
// offending stanza without dumping the full corpus on failure.
func nearbyDiff(got, want []byte, offset int) string {
	return "GOT  region:\n" + extractContext(got, offset) +
		"\nWANT region:\n" + extractContext(want, offset)
}

// extractContext returns the line containing offset plus 5 lines of
// context on each side, prefixed with line numbers. Returns "<eof>"
// when offset is past the end of buf.
func extractContext(buf []byte, offset int) string {
	if offset >= len(buf) {
		return "<eof>"
	}
	lines := strings.Split(string(buf), "\n")
	cursor := 0
	lineIndex := 0
	for index, line := range lines {
		next := cursor + len(line) + 1 // +1 for the dropped newline
		if offset < next {
			lineIndex = index
			break
		}
		cursor = next
	}
	start := max(lineIndex-5, 0)
	end := min(lineIndex+6, len(lines))
	var b strings.Builder
	for index := start; index < end; index++ {
		fmtLine := "  "
		if index == lineIndex {
			fmtLine = "> "
		}
		b.WriteString(fmtLine)
		b.WriteString(lines[index])
		b.WriteByte('\n')
	}
	return b.String()
}
