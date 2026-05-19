package bear_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// TestNewNoteURLFile_NoCyrillicLiterals enforces the no-Cyrillic-literals
// rule on the SSOT URL module and the H1-stamping cluster. Cyrillic in Go
// comments is fine (analyzer is *ast.BasicLit based) — only string
// literals trip the gate. Replaces the legacy TestNewNoteFile_NoCyrillicLiterals
// which targeted the now-deleted bear/new_note.go.
func TestNewNoteURLFile_NoCyrillicLiterals(t *testing.T) {
	repoRoot := findRepoRootFromTest(t)
	for _, rel := range []string{"bear/new_note_url.go", "bear/h1_stamp.go"} {
		path := filepath.Join(repoRoot, rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if hasCyrillicRun(lit.Value) {
				t.Errorf("%s:%d: Cyrillic literal: %s",
					path, fset.Position(lit.Pos()).Line, lit.Value)
			}
			return true
		})
	}
}

// hasCyrillicRun reports whether s contains a run of ≥2 consecutive
// Cyrillic letters. Matches the project's cyrillic-lint analyzer
// (tools/cyrillic-lint/cyrillic/analyzer.go) so the in-package test
// surfaces the same violations even on files not yet covered by the
// analyzer's package patterns.
func hasCyrillicRun(s string) bool {
	run := 0
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			run++
			if run >= 2 {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// TestEffectiveQuickPlaceholderH1_FallsBackToDefault locks the
// single-source-of-truth contract for the per-Domain config accessor
// (relocated to bear/domain.go per R4): when Domain.QuickPlaceholderH1
// is empty, the effective accessor returns DefaultQuickPlaceholderH1;
// when set, it returns the override verbatim.
func TestEffectiveQuickPlaceholderH1_FallsBackToDefault(t *testing.T) {
	d := &bear.Domain{Tag: "x", CanonicalTag: "#x", IndexTitle: "X"}
	if got := d.EffectiveQuickPlaceholderH1ForTest(); got != bear.DefaultQuickPlaceholderH1 {
		t.Errorf("empty field should yield default %q; got %q",
			bear.DefaultQuickPlaceholderH1, got)
	}
	d.QuickPlaceholderH1 = "Custom"
	if got := d.EffectiveQuickPlaceholderH1ForTest(); got != "Custom" {
		t.Errorf("override should win; got %q", got)
	}
}

// TestDefaultQuickPlaceholderH1_Value locks the canonical placeholder
// string used across all domains.
func TestDefaultQuickPlaceholderH1_Value(t *testing.T) {
	if bear.DefaultQuickPlaceholderH1 != "Quicknote" {
		t.Errorf("DefaultQuickPlaceholderH1 = %q, want %q",
			bear.DefaultQuickPlaceholderH1, "Quicknote")
	}
}

// TestNewNoteURL_BootstrapEmission_ContainsExpectedShape preserves the
// behavioral check from the deleted regex-era tests by asserting via
// the SSOT API. Locks the bootstrap-URL contract: every emitted URL
// carries text= (canonical body), edit=yes (caret-in-editor),
// open_note=yes, and the encoded canonical tag of the resolved leaf
// domain. No title= query param (Bear derives title from the embedded
// H1 marker).
func TestNewNoteURL_BootstrapEmission_ContainsExpectedShape(t *testing.T) {
	emitted := bear.NewNoteURLFromDomain(testutil.Domain(t, "quicknote/daily")).Emit()
	for _, marker := range []string{
		"text=",
		"edit=yes",
		"open_note=yes",
		"%23quicknote%2Fdaily",
	} {
		if !strings.Contains(emitted, marker) {
			t.Errorf("emitted bootstrap URL missing %q:\n%s", marker, emitted)
		}
	}
	if strings.Contains(emitted, "title=") {
		t.Errorf("bootstrap URL must NOT carry title= (Bear derives title from "+
			"the embedded H1 marker):\n%s", emitted)
	}
}

// findRepoRootFromTest walks up from the running test's cwd until a
// go.mod is found. Shared across test files in this package (see
// bearcli_fixtures_test.go).
func findRepoRootFromTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root (walked off the top from %q)", wd)
		}
		dir = parent
	}
}
