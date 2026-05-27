package bear_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// TestProductionGoFiles_NoUnexpectedCyrillic enforces the production-source
// rule: no Cyrillic identifiers and no Cyrillic string literals unless the
// literal is explicitly marked with //cyrillic:permit. Comments are allowed to
// mention localized Bear copy; tests carry real-world fixture strings and are
// intentionally outside this gate.
func TestProductionGoFiles_NoUnexpectedCyrillic(t *testing.T) {
	repoRoot := findRepoRootFromTest(t)
	for _, path := range productionGoFiles(t, repoRoot) {
		assertNoUnexpectedCyrillic(t, path)
	}
}

func productionGoFiles(t *testing.T, repoRoot string) []string {
	t.Helper()

	var paths []string
	for _, root := range []string{"bear", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	paths = append(paths, filepath.Join(repoRoot, "tools.go"))
	return paths
}

func assertNoUnexpectedCyrillic(t *testing.T, path string) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	permitLines := cyrillicPermitLines(fset, file)
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.BasicLit:
			if hasCyrillicStringLiteral(value) && !hasCyrillicPermit(fset, permitLines, value.Pos()) {
				t.Errorf("%s:%d: Cyrillic literal without //cyrillic:permit: %s",
					path, fset.Position(value.Pos()).Line, value.Value)
			}
		case *ast.Ident:
			if hasCyrillicRune(value.Name) {
				t.Errorf("%s:%d: Cyrillic identifier: %s",
					path, fset.Position(value.Pos()).Line, value.Name)
			}
		}
		return true
	})
}

func cyrillicPermitLines(fset *token.FileSet, file *ast.File) map[int]bool {
	lines := make(map[int]bool)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.Contains(comment.Text, "cyrillic:permit") {
				lines[fset.Position(comment.Pos()).Line] = true
			}
		}
	}
	return lines
}

func hasCyrillicPermit(fset *token.FileSet, permitLines map[int]bool, position token.Pos) bool {
	line := fset.Position(position).Line
	return permitLines[line] || permitLines[line-1]
}

func hasCyrillicRune(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func hasCyrillicStringLiteral(value *ast.BasicLit) bool {
	if value.Kind != token.STRING {
		return false
	}
	unquoted, err := strconv.Unquote(value.Value)
	if err != nil {
		return hasCyrillicRune(value.Value)
	}
	return hasCyrillicRune(unquoted)
}

func TestHasCyrillicStringLiteral(t *testing.T) {
	tests := []struct {
		name    string
		literal string
		kind    token.Token
		want    bool
	}{
		{name: "ascii", literal: `"Title"`, kind: token.STRING, want: false},
		{name: "single cyrillic rune", literal: `"і"`, kind: token.STRING, want: true},
		{name: "escaped cyrillic rune", literal: `"\u0456"`, kind: token.STRING, want: true},
		{name: "non string literal", literal: `42`, kind: token.INT, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCyrillicStringLiteral(&ast.BasicLit{Kind: tt.kind, Value: tt.literal})
			if got != tt.want {
				t.Errorf("hasCyrillicStringLiteral(%s) = %t, want %t", tt.literal, got, tt.want)
			}
		})
	}
}

// TestEffectiveQuickPlaceholderH1_FallsBackToDefault locks the
// single-source-of-truth contract for the per-Domain config accessor
// (relocated to bear/domain.go per R4): when Domain.QuickPlaceholderH1
// is empty, the effective accessor returns DefaultQuickPlaceholderH1;
// when set, it returns the override verbatim.
func TestEffectiveQuickPlaceholderH1_FallsBackToDefault(t *testing.T) {
	d := &domain.Domain{Tag: "x", CanonicalTag: "#x", IndexTitle: "X"}
	if got := d.EffectiveQuickPlaceholderH1(); got != domain.DefaultQuickPlaceholderH1 {
		t.Errorf("empty field should yield default %q; got %q",
			domain.DefaultQuickPlaceholderH1, got)
	}
	d.QuickPlaceholderH1 = "Custom"
	if got := d.EffectiveQuickPlaceholderH1(); got != "Custom" {
		t.Errorf("override should win; got %q", got)
	}
}

// TestDefaultQuickPlaceholderH1_Value locks the canonical placeholder
// string used across all domains.
func TestDefaultQuickPlaceholderH1_Value(t *testing.T) {
	if domain.DefaultQuickPlaceholderH1 != "Quicknote" {
		t.Errorf("DefaultQuickPlaceholderH1 = %q, want %q",
			domain.DefaultQuickPlaceholderH1, "Quicknote")
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
	emitted := domain.NewNoteURLFromDomain(testutil.Domain(t, "quicknote/daily")).Emit()
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
