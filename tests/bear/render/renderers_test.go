package bear_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

func TestDefaultParseMetaCanonical_EmptyWikilink_ExplicitlyUncategorized(t *testing.T) {
	d := &domain.Domain{
		Tag:          "library/poetry",
		CanonicalTag: "#library/poetry",
		IndexTitle:   "✱ Поезія",
	}

	// Positive: empty wikilink → ExplicitlyUncategorized: true
	t.Run("empty wikilink", func(t *testing.T) {
		body := "# title\n#library/poetry | [[]]\n---\n"
		got := render.DefaultParseMetaCanonical(d, body)
		if !got.ExplicitlyUncategorized {
			t.Errorf("expected ExplicitlyUncategorized=true, got %+v", got)
		}
		if got.Bucket != "" {
			t.Errorf("expected Bucket=\"\", got %q", got.Bucket)
		}
	})

	// Negative: no canonical header at all → zero value (ExplicitlyUncategorized: false)
	t.Run("no canonical header", func(t *testing.T) {
		body := "# title only\n---\nno tag line here\n"
		got := render.DefaultParseMetaCanonical(d, body)
		if got.ExplicitlyUncategorized {
			t.Errorf("expected ExplicitlyUncategorized=false with no canonical header, got %+v", got)
		}
		if got.Bucket != "" {
			t.Errorf("expected Bucket=\"\", got %q", got.Bucket)
		}
	})
}

func TestParseMetaFlatTable_EmptyWikilink_ExplicitlyUncategorized(t *testing.T) {
	d := &domain.Domain{
		Tag:          "library/aphorisms",
		CanonicalTag: "#library/aphorisms",
		IndexTitle:   "✱ Афоризми",
	}

	// Case 1: segment 3 is [[]] → ExplicitlyUncategorized: true
	t.Run("empty wikilink in segment 3", func(t *testing.T) {
		body := "# title\n#library/aphorisms | [[✱ Афоризми]] | [[]]\n---\n"
		got := render.ParseMetaFlatTable(d, body)
		if !got.ExplicitlyUncategorized {
			t.Errorf("expected ExplicitlyUncategorized=true for [[]], got %+v", got)
		}
		if got.Bucket != "" {
			t.Errorf("expected Bucket=\"\", got %q", got.Bucket)
		}
	})

	// Case 2: segment 3 is empty string (no wikilink at all) → AtomicMeta{} (no header detected)
	t.Run("empty segment 3 no wikilink", func(t *testing.T) {
		body := "# title\n#library/aphorisms | [[✱ Афоризми]] | \n---\n"
		got := render.ParseMetaFlatTable(d, body)
		if got.ExplicitlyUncategorized {
			t.Errorf("expected ExplicitlyUncategorized=false for empty segment 3 (no wikilink), got %+v", got)
		}
		if got.Bucket != "" {
			t.Errorf("expected Bucket=\"\", got %q", got.Bucket)
		}
	})

	// Case 3: real bucket still works
	t.Run("real bucket unchanged", func(t *testing.T) {
		body := "# title\n#library/aphorisms | [[✱ Афоризми]] | Реальне\n---\n"
		got := render.ParseMetaFlatTable(d, body)
		if got.ExplicitlyUncategorized {
			t.Errorf("expected ExplicitlyUncategorized=false for real bucket, got %+v", got)
		}
		if got.Bucket != "Реальне" {
			t.Errorf("expected Bucket=\"Реальне\", got %q", got.Bucket)
		}
	})
}
