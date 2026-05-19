// Package cyrillic ships a go/analysis Analyzer that flags any Go
// string literal whose unquoted content carries two or more
// consecutive Cyrillic letters and whose containing file is NOT
// under one of the permitted paths. Use bear.T(key) for any
// user-facing string.
package cyrillic

import (
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// cyrillicRE matches any run of two or more consecutive Cyrillic
// letters, mirroring the original.golangci.yml forbidigo regex
// (≥ 2 letters avoids tripping on stray single Latin/Cyrillic
// homoglyphs in mostly-ASCII identifiers).
var cyrillicRE = regexp.MustCompile(`\p{Cyrillic}{2,}`)

// carveOutPaths whitelists directories where Cyrillic literals are
// legitimate (locale tables, frozen legacy daemon entry, fixture
// trees, the analyzer sub-module itself). Permanent and not
// user-configurable on purpose: a runtime override would defeat the
// threat-model mitigation that pins user-facing copy to bear.T(key).
var carveOutPaths = []*regexp.Regexp{
	regexp.MustCompile(`/bear/locales/`),
	regexp.MustCompile(`/cmd/regen-watchd/`),
	regexp.MustCompile(`/tests/bear/`),
	regexp.MustCompile(`/tools/cyrillic-lint/`),
}

// permitDirective is the inline opt-out token. Mirrors forbidigo's
// `//permit:<linter-name>` shape but specialised — the cyrillic
// analyzer is the only one that reads it. Placed on the same line
// as the literal OR on the immediately preceding line.
const permitDirective = "//cyrillic:permit"

// Analyzer flags any Go string literal whose unquoted content carries
// ≥ 2 consecutive Cyrillic letters and whose containing file is NOT
// under one of the permitted paths (bear/locales, cmd/regen-watchd,
// tests/bear, tools/cyrillic-lint).
//
// Use bear.T(key) for any user-facing string.
var Analyzer = &analysis.Analyzer{
	Name:     "cyrillicliteral",
	Doc:      "flags Cyrillic string literals (use bear.T(key) instead).",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	insp.Preorder([]ast.Node{(*ast.BasicLit)(nil)}, func(n ast.Node) {
		checkLiteral(pass, n.(*ast.BasicLit))
	})
	return nil, nil
}

// checkLiteral evaluates a single *ast.BasicLit. Extracted from run to
// keep gocognit ≤ 15.
func checkLiteral(pass *analysis.Pass, lit *ast.BasicLit) {
	if lit.Kind != token.STRING {
		return
	}
	pos := pass.Fset.Position(lit.Pos())
	if isCarvedOut(pos.Filename) {
		return
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return
	}
	if !cyrillicRE.MatchString(s) {
		return
	}
	if hasPermitDirective(pass, lit) {
		return
	}
	pass.Reportf(lit.Pos(),
		"cyrillic literal forbidden — use bear.T(key) lookup.")
}

// isCarvedOut returns true when filename matches any whitelist regex.
// `testdata` directories are NEVER carved out — analysistest fixtures
// must be linted, otherwise the tool's own positive/negative tests
// can't run inside `tools/cyrillic-lint/`.
func isCarvedOut(filename string) bool {
	if strings.Contains(filename, "/testdata/") {
		return false
	}
	for _, re := range carveOutPaths {
		if re.MatchString(filename) {
			return true
		}
	}
	return false
}

// hasPermitDirective scans the file's comments for `//cyrillic:permit`
// either on the same line as the literal OR on the immediately
// preceding line. Mirrors forbidigo's `//permit` directive semantics.
func hasPermitDirective(pass *analysis.Pass, lit *ast.BasicLit) bool {
	litPos := pass.Fset.Position(lit.Pos())
	for _, file := range pass.Files {
		if pass.Fset.Position(file.Pos()).Filename != litPos.Filename {
			continue
		}
		if fileHasPermit(pass, file, litPos.Line) {
			return true
		}
	}
	return false
}

// fileHasPermit checks one parsed file's comment groups for a
// permit directive on litLine or litLine-1.
func fileHasPermit(pass *analysis.Pass, file *ast.File, litLine int) bool {
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if !strings.Contains(c.Text, permitDirective) {
				continue
			}
			cLine := pass.Fset.Position(c.Pos()).Line
			if cLine == litLine || cLine == litLine-1 {
				return true
			}
		}
	}
	return false
}
