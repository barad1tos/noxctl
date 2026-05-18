package config_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
)

// uniformShapeRE is the contract the formatter promises: every emitted
// line starts with `path:line:col: kind: ` where kind is one of the
// four enumerated classes. See VAL-04.
var uniformShapeRE = regexp.MustCompile(`^[^:]+:\d+:\d+: (parse|type-mismatch|unknown-field|validate): `)

// TestFormatLoadError verifies every leaf error surfaced by config.Load
// gets reshaped into the uniform `path:line:col: kind: message` shape.
// Aggregate fixtures fan out one line per leaf via errors.Join's
// Unwrap []error interface.
func TestFormatLoadError(t *testing.T) {
	cases := []struct {
		name         string
		fixture      string
		wantKind     string
		wantMinLines int
	}{
		{"unknown-field-shape", "broken-typo.toml", "unknown-field", 1},
		{"type-mismatch-shape", "broken-version-int.toml", "type-mismatch", 1},
		{"parse-shape", "broken-syntax.toml", "parse", 1},
		{"aggregate", "broken-multiple.toml", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertFormatLoadError(t, tc.fixture, tc.wantKind, tc.wantMinLines)
		})
	}
}

// assertFormatLoadError loads the named fixture, runs FormatLoadError
// against the resulting error, and verifies (a) every emitted line
// matches the uniform-shape regex, (b) the output is non-empty, (c)
// when wantKind is set the formatted output contains `: <wantKind>: `,
// (d) when wantMinLines > 1 the aggregate emitted at least that many
// lines. Helper extraction keeps each t.Run body small per
// loader_test.go::assertVersionValidation precedent.
func assertFormatLoadError(t *testing.T, fixture, wantKind string, wantMinLines int) {
	t.Helper()
	path := fixturePath(t, fixture)
	_, _, err := config.Load(path)
	if err == nil {
		t.Fatalf("%s: expected non-nil error from Load", fixture)
	}
	out := config.FormatLoadError(err, path)
	if out == "" {
		t.Fatalf("%s: FormatLoadError returned empty string for non-nil err", fixture)
	}
	lines := strings.Split(out, "\n")
	if len(lines) < wantMinLines {
		t.Errorf("%s: got %d lines, want >= %d:\n%s", fixture, len(lines), wantMinLines, out)
	}
	for _, line := range lines {
		if !uniformShapeRE.MatchString(line) {
			t.Errorf("%s: line does not match uniform shape: %q", fixture, line)
		}
	}
	if wantKind != "" && !strings.Contains(out, ": "+wantKind+": ") {
		t.Errorf("%s: expected kind %q in output:\n%s", fixture, wantKind, out)
	}
}
