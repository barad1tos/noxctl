package engine_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/engine"
)

func TestFirstDivergentLineCases(t *testing.T) {
	cases := []struct {
		name     string
		a, b     string
		mustHave []string
	}{
		{"second-line", "a\nb\nc", "a\nB\nc", []string{"line 2", `"b"`, `"B"`}},
		{"equal-prefix-len-mismatch", "a\nb", "a\nb\nc", []string{"trailing length differs"}},
		{"first-line", "foo", "bar", []string{"line 1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := engine.FirstDivergentLine(tc.a, tc.b)
			for _, want := range tc.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in %q", want, got)
				}
			}
		})
	}
}

func TestFirstDivergentLinesEmitsBothSides(t *testing.T) {
	out := engine.FirstDivergentLines("a\nb\nc\nd", "a\nB\nC\nd", 3)
	if len(out) == 0 {
		t.Fatal("expected non-empty output")
	}
	// First entry should reference hardcoded; second reference toml.
	hardSeen, tomlSeen := false, false
	for _, line := range out {
		if strings.HasPrefix(line, "hardcoded[") {
			hardSeen = true
		}
		if strings.HasPrefix(line, "toml[") {
			tomlSeen = true
		}
	}
	if !hardSeen || !tomlSeen {
		t.Errorf("expected both sides represented; got %v", out)
	}
}

func TestFirstDivergentLinesRespectsMaxLines(t *testing.T) {
	a := "X\nb\nc\nd\ne\nf"
	b := "X\nB\nC\nD\nE\nF"
	out := engine.FirstDivergentLines(a, b, 2)
	// 2 sides × 2 lines = 4 entries.
	if len(out) != 4 {
		t.Errorf("expected 4 entries (2 lines × 2 sides), got %d: %v", len(out), out)
	}
}

func TestIndexByTagSkipsNil(t *testing.T) {
	a := &bear.Domain{Tag: "a"}
	b := &bear.Domain{Tag: "b"}
	out := engine.IndexByTag([]*bear.Domain{a, nil, b})
	if len(out) != 2 {
		t.Errorf("IndexByTag len = %d, want 2", len(out))
	}
	if out["a"] != a || out["b"] != b {
		t.Errorf("IndexByTag map missing entries; got %v", out)
	}
}

func TestIndexByTagEmptyInput(t *testing.T) {
	out := engine.IndexByTag(nil)
	if out == nil {
		t.Error("IndexByTag(nil) returned nil; want empty non-nil map")
	}
	if len(out) != 0 {
		t.Errorf("IndexByTag(nil) len = %d, want 0", len(out))
	}
}

func TestUnionSortedTagsSortsAndDeduplicates(t *testing.T) {
	a := map[string]*bear.Domain{
		"library/poetry": nil,
		"llm/agents":     nil,
		"library/lyrics": nil,
	}
	b := map[string]*bear.Domain{
		"library/poetry": nil, // overlap
		"it/vendors":     nil,
	}
	got := engine.UnionSortedTags(a, b)
	want := []string{"it/vendors", "library/lyrics", "library/poetry", "llm/agents"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUnionSortedTagsEmpty(t *testing.T) {
	got := engine.UnionSortedTags(nil, nil)
	if len(got) != 0 {
		t.Errorf("expected empty slice; got %v", got)
	}
}
