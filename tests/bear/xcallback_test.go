package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// resolveUmbrella locates a catalog-loaded umbrella by its top-level
// tag. testutil.Domain handles the lookup; this thin wrapper preserves
// the existing call shape used across the tests below.
func resolveUmbrella(t *testing.T, tag string) *domain.Domain {
	t.Helper()
	return testutil.Domain(t, tag)
}

func TestNewNoteURLFromDomain_QuicknoteDaily_BootstrapForm(t *testing.T) {
	u := domain.NewNoteURLFromDomain(testutil.Domain(t, "quicknote/daily"))
	if u.Form != domain.FormBootstrap {
		t.Fatalf("expected FormBootstrap, got %v", u.Form)
	}
	if u.Inner == nil {
		t.Fatal("FormBootstrap requires non-nil Inner")
	}
	if u.Inner.Form != domain.FormSimple {
		t.Errorf("Inner.Form: expected FormSimple, got %v", u.Inner.Form)
	}
	if u.Tag != "quicknote/daily" {
		t.Errorf("Tag = %q, want quicknote/daily", u.Tag)
	}
}

func TestNewNoteURLFromDomain_UmbrellaResolvesToLeaf(t *testing.T) {
	itUmbrella := resolveUmbrella(t, "it")
	u := domain.NewNoteURLFromDomain(itUmbrella)
	if !strings.HasPrefix(u.Tag, "it/") || u.Tag == "it" {
		t.Errorf("umbrella did not delegate to leaf: Tag = %q", u.Tag)
	}
	if strings.Contains(u.Backlink, "_umbrella") {
		t.Errorf("umbrella _umbrella placeholder leaked into Backlink: %q", u.Backlink)
	}
}

func TestNewNoteURL_RoundTrip_QuicknoteDaily(t *testing.T) {
	original := domain.NewNoteURLFromDomain(testutil.Domain(t, "quicknote/daily"))
	emitted := original.Emit()
	parsed, ok := domain.ParseNewNoteURLSegment(strings.TrimPrefix(emitted, " | "))
	if !ok {
		t.Fatalf("ParseNewNoteURLSegment failed for emitted output: %q", emitted)
	}
	if !original.Equals(parsed) {
		t.Errorf("round-trip lost data:\n  original: %+v\n  parsed:   %+v", original, parsed)
	}
}

func TestNewNoteURL_Equals_StructuralDriftDetected(t *testing.T) {
	a := domain.NewNoteURL{
		Tag: "quicknote/daily", CanonicalTag: "#quicknote/daily",
		Backlink: "[[✱ Daily]]", PlaceholderH1: "Quicknote",
		Label: "Нова нотатка", Form: domain.FormBootstrap,
	}
	b := a
	b.Backlink = "[[_umbrella]]"
	if a.Equals(b) {
		t.Error("Equals returned true despite Backlink drift ([[_umbrella]] leak)")
	}
}

// quicknoteDailyBootstrapURL returns a fully-populated FormBootstrap
// NewNoteURL pinned to quicknote/daily. Used as the baseline fixture
// for the Inner-leg equality tests — each test mutates a single field
// on the copy to provoke drift.
func quicknoteDailyBootstrapURL() domain.NewNoteURL {
	return domain.NewNoteURL{
		Tag: "quicknote/daily", CanonicalTag: "#quicknote/daily",
		Backlink: "[[✱ Daily]]", PlaceholderH1: "Quicknote",
		Label: "Нова нотатка", Form: domain.FormBootstrap,
		Inner: &domain.NewNoteURL{Tag: "quicknote/daily", Label: "Нова нотатка", Form: domain.FormSimple},
	}
}

// TestNewNoteURL_Equals_InnerPresenceMismatch pins the recursive
// Inner-equality branch in NewNoteURL.Equals: it must reject
// (Inner != nil) vs (Inner == nil) pairs in either direction. The
// flat (Inner == nil) struct-drift case is covered separately;
// this test fires only when one side has nested form data.
func TestNewNoteURL_Equals_InnerPresenceMismatch(t *testing.T) {
	a := quicknoteDailyBootstrapURL()
	b := a
	b.Inner = nil
	if a.Equals(b) {
		t.Error("Equals returned true despite Inner presence mismatch (a.Inner!=nil, b.Inner==nil)")
	}
	if b.Equals(a) {
		t.Error("Equals returned true in the reverse direction")
	}
}

// TestNewNoteURL_Equals_InnerContentDrift exercises the recursive
// Inner-equality branch: both Inner pointers non-nil but their fields
// differ. Without this test, a regression that compared only Inner-presence
// would slip past the SSOT contract.
func TestNewNoteURL_Equals_InnerContentDrift(t *testing.T) {
	a := quicknoteDailyBootstrapURL()
	b := a
	innerCopy := *a.Inner
	innerCopy.Tag = "library/poetry"
	b.Inner = &innerCopy
	if a.Equals(b) {
		t.Error("Equals returned true despite Inner.Tag drift")
	}
}

func TestParseNewNoteURLSegment_LegacySimple(t *testing.T) {
	segment := "[Нова нотатка](bear://x-callback-url/create?tags=library%2Fpoetry&open_note=yes)"
	u, ok := domain.ParseNewNoteURLSegment(segment)
	if !ok {
		t.Fatal("Parse failed for legacy simple form")
	}
	if u.Form != domain.FormSimple {
		t.Errorf("Form = %v, want FormSimple", u.Form)
	}
	if u.Inner != nil {
		t.Errorf("FormSimple should have nil Inner; got %+v", u.Inner)
	}
	if u.Tag != "library/poetry" {
		t.Errorf("Tag = %q, want library/poetry", u.Tag)
	}
}

func TestParseNewNoteURLSegment_LegacyTitleForm(t *testing.T) {
	segment := "[Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&title=13%20May%202026&open_note=yes)"
	u, ok := domain.ParseNewNoteURLSegment(segment)
	if !ok {
		t.Fatal("Parse failed for legacy title form")
	}
	if u.Form != domain.FormLegacyTitle {
		t.Errorf("Form = %v, want FormLegacyTitle", u.Form)
	}
}

func TestFindAllNewNoteURLsInBody_AnchoredToPipeSeparator(t *testing.T) {
	// User-pasted bear://create literal mid-paragraph, NOT preceded by " | "
	body := "# Note\nHere's a useful link: bear://x-callback-url/create?tags=foo&open_note=yes for opening notes.\n"
	urls := domain.FindAllNewNoteURLsInBody(body)
	if len(urls) != 0 {
		t.Errorf("user-pasted URL falsely picked up: %d matches found", len(urls))
	}
}

func TestFindAllNewNoteURLsInBody_PicksUpCanonicalDecoration(t *testing.T) {
	body := "# Title\n#quicknote/daily | [[✱ Daily]] | [Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n---\n"
	urls := domain.FindAllNewNoteURLsInBody(body)
	if len(urls) != 1 {
		t.Fatalf("canonical decoration: expected 1 URL, got %d", len(urls))
	}
	if urls[0].Tag != "quicknote/daily" {
		t.Errorf("Tag = %q, want quicknote/daily", urls[0].Tag)
	}
}

func TestStripNewNoteURLsFromBody_PreservesUserPastedLinks(t *testing.T) {
	body := "# Note\nLink to: bear://x-callback-url/create?tags=foo&open_note=yes\n" +
		"#quicknote/daily | [[✱ Daily]] | " +
		"[Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n"
	stripped := domain.StripNewNoteURLsFromBody(body)
	if !strings.Contains(stripped, "Link to: bear://x-callback-url/create") {
		t.Error("user-pasted link incorrectly removed")
	}
	if strings.Contains(stripped, "[Нова нотатка](bear://") {
		t.Error("canonical decoration not removed")
	}
}

func TestResolveURLDomain_LeafReturnsSelf(t *testing.T) {
	d := testutil.Domain(t, "quicknote/daily")
	if d.ResolveURLDomain() != d {
		t.Error("leaf domain ResolveURLDomain() should return self")
	}
}

func TestResolveURLDomain_UmbrellaReturnsDefaultChild(t *testing.T) {
	umbrella := resolveUmbrella(t, "it")
	resolved := umbrella.ResolveURLDomain()
	if resolved == umbrella {
		t.Fatal("umbrella did not resolve to a leaf")
	}
	if resolved.Tag != "it/domains" {
		t.Errorf("resolved.Tag = %q, want it/domains (umbrella's DefaultChild)", resolved.Tag)
	}
}

// driftBodyWithBacklink composes a synthetic body that carries a single
// canonical-tag-line decorated with a bootstrap-form new-note URL whose
// Backlink is overridden. Used to provoke structural drift inside the
// strict predicate without going through real domain config.
func driftBodyWithBacklink(t *testing.T, backlink string) string {
	t.Helper()
	u := quicknoteDailyBootstrapURL()
	u.Backlink = backlink
	return "# Title\n#quicknote/daily | " + backlink + u.Emit() + "\n---\n\n"
}

// TestEqualIgnoringNewNoteLinkStrict_DetectsBacklinkDrift verifies the
// strict predicate rejects two bodies whose new-note URLs differ only
// in Backlink — the [[_umbrella]] leak regression that triggered.
func TestEqualIgnoringNewNoteLinkStrict_DetectsBacklinkDrift(t *testing.T) {
	leafBody := driftBodyWithBacklink(t, "[[✱ Daily]]")
	stale := driftBodyWithBacklink(t, "[[_umbrella]]")
	if domain.EqualIgnoringNewNoteLinkStrictForTest(leafBody, stale) {
		t.Error("strict predicate accepted Backlink drift — [[_umbrella]] leak undetected")
	}
}

// TestEqualIgnoringNewNoteLinkStrict_DetectsPlaceholderH1Drift verifies the
// strict predicate rejects PlaceholderH1 drift between two otherwise-
// identical bodies.
func TestEqualIgnoringNewNoteLinkStrict_DetectsPlaceholderH1Drift(t *testing.T) {
	u := quicknoteDailyBootstrapURL()
	u.PlaceholderH1 = "Daily"
	a := driftBodyWithBacklink(t, "[[✱ Daily]]")
	b := "# Title\n#quicknote/daily | [[✱ Daily]]" + u.Emit() + "\n---\n\n"
	if domain.EqualIgnoringNewNoteLinkStrictForTest(a, b) {
		t.Error("strict predicate accepted PlaceholderH1 drift")
	}
}

// TestEqualIgnoringNewNoteLinkStrict_FallsBackToBodyCompareWhenURLsMatch
// verifies the fallback path: when URLs match position-by-position, the
// predicate still consults the non-strict body compare.
func TestEqualIgnoringNewNoteLinkStrict_FallsBackToBodyCompareWhenURLsMatch(t *testing.T) {
	body := driftBodyWithBacklink(t, "[[✱ Daily]]")
	if !domain.EqualIgnoringNewNoteLinkStrictForTest(body, body) {
		t.Error("strict predicate failed to confirm identical bodies as equal")
	}
}

// TestNewNoteURLFromDomain_BacklinkIsEmptyWikilink asserts that the
// Backlink field in a bootstrapped NewNoteURL is the literal "[[]]"
// (empty wikilink) rather than a domain-specific UnknownBucket backlink
// like "[[✱ Daily]]" or "[[Невідомі]]". This is the seed-point behavior
// change from Wave 2 of the empty-bucket-as-stable-state plan.
func TestNewNoteURLFromDomain_BacklinkIsEmptyWikilink(t *testing.T) {
	d := testutil.Domain(t, "library/poetry")
	u := domain.NewNoteURLFromDomain(d)
	if u.Backlink != "[[]]" {
		t.Errorf("Backlink = %q, want [[]]", u.Backlink)
	}
	if u.Form != domain.FormBootstrap {
		t.Errorf("Form = %v, want FormBootstrap", u.Form)
	}
	// Round-trip: verify the emitted canonical body carries "[[]]" as backlink.
	emitted := u.Emit()
	parsed, ok := domain.ParseNewNoteURLSegment(strings.TrimPrefix(emitted, " | "))
	if !ok {
		t.Fatalf("ParseNewNoteURLSegment failed for emitted output: %q", emitted)
	}
	if parsed.Backlink != "[[]]" {
		t.Errorf("parsed Backlink = %q, want [[]]", parsed.Backlink)
	}
}

// TestEqualIgnoringNewNoteLink_AcceptsURLDriftWithBodyMatch confirms the
// non-strict atom-path predicate tolerates URL drift — body bytes are all
// that matter.
func TestEqualIgnoringNewNoteLink_AcceptsURLDriftWithBodyMatch(t *testing.T) {
	a := "# X\n#quicknote/daily | [[✱ Daily]] | " +
		"[Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n"
	b := "# X\n#quicknote/daily | [[✱ Daily]] | " +
		"[Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&title=stale&open_note=yes)\n"
	if !domain.EqualIgnoringNewNoteLinkForTest(a, b) {
		t.Error("non-strict predicate falsely rejected URL drift with body match")
	}
}

// TestEqualIgnoringNewNoteLinkStrict_RejectsURLDriftWithBodyMatch confirms
// the strict predicate engages the structural compare and rejects a
// simple-vs-bootstrap form mismatch.
func TestEqualIgnoringNewNoteLinkStrict_RejectsURLDriftWithBodyMatch(t *testing.T) {
	simple := "# X\n#quicknote/daily | [[✱ Daily]] | " +
		"[Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n"
	bootstrap := driftBodyWithBacklink(t, "[[✱ Daily]]")
	if domain.EqualIgnoringNewNoteLinkStrictForTest(simple, bootstrap) {
		t.Error("strict predicate accepted form mismatch (simple vs bootstrap) — structural compare not engaged")
	}
}
