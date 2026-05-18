package recap_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/cli/recap"
	"github.com/barad1tos/noxctl/bear/engine"
)

func TestRenderRecap_FullOutput_HappyPath(t *testing.T) {
	result := &engine.ApplyResult{
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
		PrePasses: map[string]engine.PrePassCounts{
			"foreign_tag":        {OK: 1},
			"auto_tag":           {OK: 1},
			"cross_domain":       {OK: 1},
			"time_promotion":     {OK: 1},
			"duplicate_registry": {OK: 1},
		},
		Domains: map[string]engine.DomainCounts{
			"library/poetry":   {Unchanged: 28},
			"library/articles": {Created: 1, Unchanged: 47},
			"llm/agents":       {Changed: 2, Unchanged: 8},
		},
	}
	var buf bytes.Buffer
	recap.Render(&buf, result, false)
	out := buf.String()

	// Headers present in non-quiet mode.
	if !strings.Contains(out, "PRE-PASSES") {
		t.Errorf("expected PRE-PASSES header; got: %s", out)
	}
	if !strings.Contains(out, "PLAY RECAP") {
		t.Errorf("expected PLAY RECAP header; got: %s", out)
	}
	// All five pre-passes appear.
	for _, name := range []string{"foreign_tag", "auto_tag", "cross_domain", "time_promotion", "duplicate_registry"} {
		if !strings.Contains(out, name) {
			t.Errorf("pre-pass %q missing from output: %s", name, out)
		}
	}
	// All three domains appear.
	for _, tag := range []string{"library/poetry", "library/articles", "llm/agents"} {
		if !strings.Contains(out, tag) {
			t.Errorf("domain %q missing from output: %s", tag, out)
		}
	}
	// Sorted order assertion: alphabetical. library/articles before library/poetry.
	if strings.Index(out, "library/poetry") < strings.Index(out, "library/articles") {
		t.Errorf("domains not sorted alphabetically: %s", out)
	}
}

func TestRenderRecap_QuietSuppressesHeadersAndOK(t *testing.T) {
	result := &engine.ApplyResult{
		PrePasses: map[string]engine.PrePassCounts{
			"auto_tag": {OK: 1},
		},
		Domains: map[string]engine.DomainCounts{
			"library/poetry": {Unchanged: 28},
		},
	}
	var buf bytes.Buffer
	recap.Render(&buf, result, true)
	out := buf.String()

	if strings.Contains(out, "PRE-PASSES") {
		t.Errorf("quiet mode should suppress PRE-PASSES header; got: %s", out)
	}
	if strings.Contains(out, "PLAY RECAP") {
		t.Errorf("quiet mode should suppress PLAY RECAP header; got: %s", out)
	}
	if strings.Contains(out, "auto_tag") {
		t.Errorf("quiet mode should suppress OK pre-pass rows; got: %s", out)
	}
	if strings.Contains(out, "library/poetry") {
		t.Errorf("quiet mode should suppress unchanged-only domain rows; got: %s", out)
	}
}

func TestRenderRecap_QuietPreservesFailureRows(t *testing.T) {
	result := &engine.ApplyResult{
		PrePasses: map[string]engine.PrePassCounts{
			"auto_tag":    {OK: 1},
			"foreign_tag": {Failed: 1},
		},
		Domains: map[string]engine.DomainCounts{
			"library/poetry":   {Unchanged: 28},
			"library/articles": {Failed: 1},
		},
	}
	var buf bytes.Buffer
	recap.Render(&buf, result, true)
	out := buf.String()

	// Failures must NEVER be silent — even in quiet mode.
	if !strings.Contains(out, "foreign_tag") {
		t.Errorf("quiet mode dropped FAILURE pre-pass row; got: %s", out)
	}
	if !strings.Contains(out, "library/articles") {
		t.Errorf("quiet mode dropped FAILURE domain row; got: %s", out)
	}
	// OK rows still suppressed.
	if strings.Contains(out, "auto_tag") {
		t.Errorf("quiet mode should suppress OK pre-pass row; got: %s", out)
	}
	if strings.Contains(out, "library/poetry") {
		t.Errorf("quiet mode should suppress unchanged-only domain row; got: %s", out)
	}
}

func TestRenderRecap_NilResultIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	recap.Render(&buf, nil, false)
	if buf.Len() != 0 {
		t.Errorf("nil result should produce no output; got: %s", buf.String())
	}
}
