// Package bear_test — auto_tag_test.go locks the canonical-
// bootstrap contract for ApplyDailyDefaultTag and RenderCanonicalForBootstrap.
//
// The fast-pass writes the canonical body in a single bearcli call —
// H1 preserved or stamped, tag-line directly under H1, body below
// `---` separator. parseAtomicContent classifies free-form lines that
// land between H1 and the tag-line as preamble; hoistPreambleToBody
// then relocates them into the body zone so the rendered output never
// carries content above the tag-line. The subsequent regen cycle
// no-ops via equalIgnoringNewNoteLink.
package bear_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// userGibberishLine mirrors the user-reported screenshot body ("тестовий
// текст у шапці" bug). Preserved verbatim to keep test fidelity to the
// original bug evidence; kept as a single constant to centralize the
// non-dictionary token away from inline string literals.
//
//goland:noinspection SpellCheckingInspection
const userGibberishLine = ",khvljhv"

// TestRenderCanonicalForBootstrap_EmptyContent_ProducesCanonicalForm
// asserts that a brand-new untagged note (Bear `+` button before any
// content flushes) gets a stamped H1 + canonical tag-line + `---`
// separator. This is the empty-input branch of stampDailyTag — pre-fix
// it returned `"#quicknote/daily\n"` and Bear destroyed the title
// because the new content carried no H1.
func TestRenderCanonicalForBootstrap_EmptyContent_ProducesCanonicalForm(t *testing.T) {
	fixedNow := time.Date(2026, 5, 15, 22, 30, 0, 0, time.Local)
	domain.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	out := testutil.Domain(t, "quicknote/daily").RenderCanonicalForBootstrap("")

	wantH1 := "# 15 May 2026 at 22:30"
	if !strings.HasPrefix(out, wantH1+"\n") {
		t.Errorf("missing stamped H1\n  got first line: %q\n  want: %q",
			strings.SplitN(out, "\n", 2)[0], wantH1)
	}
	if !strings.Contains(out, "#quicknote/daily | [[✱ Daily]]") {
		t.Errorf("missing canonical tag-line + backlink\n  full output:\n%s", out)
	}
	if !strings.Contains(out, "\n---\n") {
		t.Errorf("missing `---` separator\n  full output:\n%s", out)
	}
	// Tag-line must live directly under H1 (no preamble between them).
	lines := strings.SplitN(out, "\n", 3)
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "#quicknote/daily") {
		t.Errorf("tag-line not directly under H1\n  line 1: %q\n  line 2: %q",
			lines[0], lines[1])
	}
}

// TestRenderCanonicalForBootstrap_NoteWithBody_PutsBodyBelowSeparator
// locks the canonical contract for fast-pass stamping: when a user
// types body content before the fast-pass adds the tag-line,
// parseAtomicContent classifies that content as preamble, and
// hoistPreambleToBody relocates it into the body zone so the
// rendered output lands BELOW `---` rather than between H1 and
// the tag-line.
func TestRenderCanonicalForBootstrap_NoteWithBody_PutsBodyBelowSeparator(t *testing.T) {
	fixedNow := time.Date(2026, 5, 15, 21, 59, 0, 0, time.Local)
	domain.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	// Mirrors the user's screenshot: H1 from Bear auto-title, body line
	// typed by the user, no tag-line yet (fast-pass is about to add it).
	in := "# 15 May 2026 at 21:59\n" + userGibberishLine + "\n"
	out := testutil.Domain(t, "quicknote/daily").RenderCanonicalForBootstrap(in)

	// H1 preserved verbatim — never re-stamped because it's present.
	if !strings.HasPrefix(out, "# 15 May 2026 at 21:59\n") {
		t.Errorf("existing H1 was not preserved\n  got first line: %q",
			strings.SplitN(out, "\n", 2)[0])
	}
	head, body, found := strings.Cut(out, "\n---\n")
	if !found {
		t.Fatalf("no `---` separator in output:\n%s", out)
	}
	if strings.Contains(head, userGibberishLine) {
		t.Errorf("body line %q leaked into the header zone (preamble bug regressed)\n  header zone:\n%s", userGibberishLine, head)
	}
	if !strings.Contains(body, userGibberishLine) {
		t.Errorf("body line %q missing below `---` separator\n  body zone:\n%q", userGibberishLine, body)
	}
}

// TestRenderCanonicalForBootstrap_IdempotentWithRegenCycle proves the
// "1 cycle" contract: fast-pass writes form X, regen cycle sees X,
// equalIgnoringNewNoteLink trips, no second write happens. The cycle's
// canonical render of X must equal X up to new-note URL drift.
func TestRenderCanonicalForBootstrap_IdempotentWithRegenCycle(t *testing.T) {
	fixedNow := time.Date(2026, 5, 15, 22, 30, 0, 0, time.Local)
	domain.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	in := "# 15 May 2026 at 22:30\nuser typed body\n"
	bootstrap := testutil.Domain(t, "quicknote/daily").RenderCanonicalForBootstrap(in)

	// Now simulate the regen cycle running on the same content —
	// RenderAtomicCanonicalForTest is the in-memory mirror of
	// upsertAtomicBacklink's canonicalization path.
	canonical := domain.RenderAtomicCanonicalForTest(t, testutil.Domain(t, "quicknote/daily"), "Note Title",
		testutil.Domain(t, "quicknote/daily").UnknownBucket, bootstrap)

	if !domain.EqualIgnoringNewNoteLink(bootstrap, canonical) {
		t.Errorf("bootstrap output is not idempotent under cycle re-render\n  bootstrap:\n%s\n  cycle:\n%s",
			bootstrap, canonical)
	}
}

// TestApplyForeignTagEscape_CanonicalizesDestination proves the
// foreign-tag escape path also produces canonical form for the
// destination domain in one bearcli call. A note tagged with both
// quicknote/daily and a registered domain tag should end up in the
// destination's canonical shape, not just stripped.
func TestApplyForeignTagEscape_CanonicalizesDestination(t *testing.T) {
	fixedNow := time.Date(2026, 5, 15, 22, 30, 0, 0, time.Local)
	domain.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	// Use the same RenderCanonicalForBootstrap helper that
	// processForeignTagEscape uses internally — proves the destination
	// domain produces canonical form including its own backlink.
	dst := testutil.Domain(t, "quicknote/weekly")
	stripped := "# 13 May 2026\n#quicknote/weekly\nweekly recap body\n"

	// Running the destination-tagged input through the destination
	// domain's RenderCanonicalForBootstrap should produce:
	// - H1 preserved
	// - destination's canonical tag-line + backlink directly under H1
	// - body below `---`
	out := dst.RenderCanonicalForBootstrap(stripped)

	if !strings.HasPrefix(out, "# 13 May 2026\n") {
		t.Errorf("H1 not preserved\n  first line: %q", strings.SplitN(out, "\n", 2)[0])
	}
	if !strings.Contains(out, "#quicknote/weekly | [[") {
		t.Errorf("destination canonical tag-line missing\n  output:\n%s", out)
	}
	_, body, found := strings.Cut(out, "\n---\n")
	if !found {
		t.Errorf("`---` separator missing\n  output:\n%s", out)
	}
	if !strings.Contains(body, "weekly recap body") {
		t.Errorf("body not below `---`\n  body:\n%q", body)
	}
}

// TestApplyForeignTagEscape_UnknownDestinationFallsBackToStripOnly
// proves the safety net: when the user-supplied foreign tag has no
// registered Domain, processForeignTagEscape still strips the
// #quicknote/* token from the body but does NOT attempt to render
// canonical form for an unknown destination (which would have no
// backlink target). The note remains user-managed.
func TestApplyForeignTagEscape_UnknownDestinationFallsBackToStripOnly(t *testing.T) {
	// Build a domainsByTag map that does NOT include the foreign tag.
	domainsByTag := domain.DomainsByTag([]*domain.Domain{testutil.Domain(t, "quicknote/daily")})

	if _, ok := domainsByTag["user/typed-no-domain"]; ok {
		t.Fatalf("test precondition violated: map should not contain `user/typed-no-domain`")
	}

	// The behavior under test happens inside processForeignTagEscape,
	// which is unexported. The contract: when destDomain:= lookup is
	// nil, the function logs and falls back to writing the stripped
	// body without canonical bootstrap. We assert here that the map
	// lookup returns nil for unknown tags so the code-path is
	// reachable. (The behavioral path is covered by integration in
	// tests/bear/engine/autotag_poll_test.go, which exercises the
	// fakeAutoTagBackend through the daemon's handleAutoTagTick.)
	if got := domainsByTag["user/typed-no-domain"]; got != nil {
		t.Errorf("unknown-tag lookup must return nil; got %v", got)
	}
}

// TestRefreshQuicknotePlaceholder_MarkerPresent locks the rewrite contract:
// when content starts with the literal `# Quicknote\n` H1, replace just that
// line with `# <stamp>\n` and leave the rest of the body byte-identical.
// UB-Task 6 generalized the helper: placeholder is now a parameter, and the
// literal "Quicknote" exercises the same code path the old hardcoded constant
// did.
func TestRefreshQuicknotePlaceholder_MarkerPresent(t *testing.T) {
	in := "# Quicknote\n#quicknote/daily | [[✱ Daily]] | [Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n---\n\n"
	out, refreshed := fastpass.RefreshPlaceholderH1ForTest(in, "Quicknote", "16 May 2026 at 00:35")

	if !refreshed {
		t.Fatalf("marker present but refreshed=false")
	}
	wantHead := "# 16 May 2026 at 00:35\n"
	if !strings.HasPrefix(out, wantHead) {
		t.Errorf("H1 not replaced\n  first line: %q\n  want prefix: %q",
			strings.SplitN(out, "\n", 2)[0]+"\n", wantHead)
	}
	wantTail := "#quicknote/daily | [[✱ Daily]] | [Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n---\n\n"
	if !strings.HasSuffix(out, wantTail) {
		t.Errorf("body below H1 was modified\n  got:\n%s\n  want suffix:\n%s", out, wantTail)
	}
}

// TestRefreshQuicknotePlaceholder_MarkerAbsent — no refresh when the body
// does not start with `# Quicknote\n`. Returns the input unchanged with
// refreshed=false so the caller can skip the bearcli overwrite.
func TestRefreshQuicknotePlaceholder_MarkerAbsent(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"already-timestamped", "# 16 May 2026 at 00:30\n#quicknote/daily | [[✱ Daily]]\n---\n\n"},
		{"no H1 at all", "#quicknote/daily | [[✱ Daily]]\n---\n\n"},
		{"H1 with different text", "# Some Other Title\n#quicknote/daily | [[✱ Daily]]\n---\n\n"},
		{"empty content", ""},
		{"Quicknote not at start", "leading text\n# Quicknote\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, refreshed := fastpass.RefreshPlaceholderH1ForTest(tc.in, "Quicknote", "16 May 2026 at 00:35")
			if refreshed {
				t.Errorf("expected refreshed=false; got true\n  in:  %q\n  out: %q", tc.in, out)
			}
			if out != tc.in {
				t.Errorf("expected content unchanged; got %q\n  in: %q", out, tc.in)
			}
		})
	}
}

// silence unused-import warning when context is needed in future tests.
var _ = context.Background

// fakeRefreshBackend records bearcli calls and returns canned payloads. Used
// by TestApplyQuicknotePlaceholderRefresh_* to drive the scan deterministically.
type fakeRefreshBackend struct {
	listPayload     []byte
	overwriteCalls  []fakeOverwriteCall
	listArgsCapture []string
}

type fakeOverwriteCall struct {
	NoteID  string
	Content string
}

func (f *fakeRefreshBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "list":
		f.listArgsCapture = append(f.listArgsCapture, args...)
		return f.listPayload, nil
	case "overwrite":
		// bear/fetches.go::overwriteWithRetry passes the note ID positionally as
		// args[1]. The full call shape is ["overwrite", noteID, "--base", hash].
		var noteID string
		if len(args) >= 2 && !strings.HasPrefix(args[1], "--") {
			noteID = args[1]
		}
		f.overwriteCalls = append(f.overwriteCalls, fakeOverwriteCall{NoteID: noteID, Content: stdin})
		return []byte(`{"ok":true}`), nil
	case "show":
		// overwriteWithRetry calls show first for hash; return a minimal stub.
		return []byte(`{"id":"abc","hash":"deadbeef","content":""}`), nil
	}
	return nil, nil
}

// fakeNote is the in-memory shape of one bearcli `list` row used by the
// placeholder-refresh tests.
type fakeNote struct {
	ID, Title string
	Tags      []string
	Content   string
}

// setupRefreshFixture builds a fakeRefreshBackend pre-loaded with notes,
// installs it in a fresh context, and resets the bearcli pool. Centralizes
// the marshal-and-wire sequence that was duplicated across the
// TestApply*PlaceholderRefresh_* cases.
func setupRefreshFixture(t *testing.T, notes ...fakeNote) (*fakeRefreshBackend, context.Context) {
	t.Helper()
	rows := make([]map[string]any, 0, len(notes))
	for _, n := range notes {
		rows = append(rows, map[string]any{
			"id":      n.ID,
			"title":   n.Title,
			"tags":    n.Tags,
			"content": n.Content,
		})
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	backend := &fakeRefreshBackend{listPayload: payload}
	ctx := domain.ContextWithBackend(context.Background(), backend)
	domain.ResetBearcliPoolForTest(1)
	return backend, ctx
}

// setFixedNowForTest pins domain.SetNowForNewNoteLinkForTest to a fixed
// instant so timestamp-stamping is deterministic.
func setFixedNowForTest(t *testing.T, fixedNow time.Time) {
	t.Helper()
	domain.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })
}

// TestApplyQuicknotePlaceholderRefresh_MarkerNote_GetsTimestampStamped
// covers the happy path: a `#quicknote/daily`-tagged note whose Title is
// the literal placeholder "Quicknote" gets its H1 rewritten with a fresh
// timestamp. Body below H1 is preserved.
func TestApplyQuicknotePlaceholderRefresh_MarkerNote_GetsTimestampStamped(t *testing.T) {
	setFixedNowForTest(t, time.Date(2026, 5, 16, 0, 35, 0, 0, time.Local))
	backend, ctx := setupRefreshFixture(t, fakeNote{
		ID:      "note-abc",
		Title:   "Quicknote",
		Tags:    []string{"quicknote/daily"},
		Content: "# Quicknote\n#quicknote/daily | [[✱ Daily]]\n---\n\n",
	})

	refreshed, err := fastpass.ApplyQuicknotePlaceholderRefresh(ctx, testutil.Domain(t, "quicknote/daily"))
	if err != nil {
		t.Fatalf("ApplyQuicknotePlaceholderRefresh: %v", err)
	}
	if refreshed != 1 {
		t.Errorf("refreshed = %d, want 1", refreshed)
	}
	if len(backend.overwriteCalls) != 1 {
		t.Fatalf("overwrite calls = %d, want 1", len(backend.overwriteCalls))
	}
	got := backend.overwriteCalls[0]
	if got.NoteID != "note-abc" {
		t.Errorf("overwrite id = %q, want %q", got.NoteID, "note-abc")
	}
	wantHead := "# 16 May 2026 at 00:35\n"
	if !strings.HasPrefix(got.Content, wantHead) {
		t.Errorf("overwrite content head = %q, want prefix %q",
			strings.SplitN(got.Content, "\n", 2)[0]+"\n", wantHead)
	}
}

// TestApplyQuicknotePlaceholderRefresh_NonMarkerNote_Skipped covers the
// negative path: a `#quicknote/daily`-tagged note with a regular timestamp
// H1 is left alone (no bearcli overwrite issued).
func TestApplyQuicknotePlaceholderRefresh_NonMarkerNote_Skipped(t *testing.T) {
	backend, ctx := setupRefreshFixture(t, fakeNote{
		ID:      "note-old",
		Title:   "15 May 2026 at 22:00",
		Tags:    []string{"quicknote/daily"},
		Content: "# 15 May 2026 at 22:00\n#quicknote/daily | [[✱ Daily]]\n---\n\nold body\n",
	})

	refreshed, err := fastpass.ApplyQuicknotePlaceholderRefresh(ctx, testutil.Domain(t, "quicknote/daily"))
	if err != nil {
		t.Fatalf("ApplyQuicknotePlaceholderRefresh: %v", err)
	}
	if refreshed != 0 {
		t.Errorf("refreshed = %d, want 0 (non-marker should be skipped)", refreshed)
	}
	if len(backend.overwriteCalls) != 0 {
		t.Errorf("overwrite calls = %d, want 0", len(backend.overwriteCalls))
	}
}

// TestApplyQuicknotePlaceholderRefresh_LegacyForwardsToGenericScan
// proves the legacy entry point now forwards into the generalized
// global scan (UB-Task 6). The legacy signature is retained for
// binary compat with a one-element domain map; the bearcli args
// must match the new global-scan contract (no --tag filter, since
// placeholders are shared across domains and dispatch happens by
// Title + tag-set match inside the loop).
func TestApplyQuicknotePlaceholderRefresh_LegacyForwardsToGenericScan(t *testing.T) {
	backend := &fakeRefreshBackend{listPayload: []byte("[]")}
	ctx := domain.ContextWithBackend(context.Background(), backend)
	domain.ResetBearcliPoolForTest(1)

	_, err := fastpass.ApplyQuicknotePlaceholderRefresh(ctx, testutil.Domain(t, "quicknote/daily"))
	if err != nil {
		t.Fatalf("ApplyQuicknotePlaceholderRefresh: %v", err)
	}

	wantArgs := []string{
		"list",
		"--sort", "created:desc",
		"--limit", "20",
		"--format", "json",
		"--fields", "id,title,tags,content",
	}
	if !reflect.DeepEqual(backend.listArgsCapture, wantArgs) {
		t.Errorf("legacy alias must forward to the global-scan contract\n  got:  %v\n  want: %v",
			backend.listArgsCapture, wantArgs)
	}
}

// TestApplyPlaceholderRefresh_DispatchesToDomainByTitleAndTag covers
// the multi-domain dispatch: a note titled "Quicknote" tagged with
// a known domain gets its H1 rewritten using that domain's
// effectiveQuickPlaceholderH1.
func TestApplyPlaceholderRefresh_DispatchesToDomainByTitleAndTag(t *testing.T) {
	setFixedNowForTest(t, time.Date(2026, 5, 16, 0, 35, 0, 0, time.Local))
	backend, ctx := setupRefreshFixture(t, fakeNote{
		ID:      "note-abc",
		Title:   "Quicknote",
		Tags:    []string{"quicknote/daily"},
		Content: "# Quicknote\n#quicknote/daily | [[✱ Daily]]\n---\n\n",
	})

	domainsByTag := domain.DomainsByTag([]*domain.Domain{testutil.Domain(t, "quicknote/daily")})
	refreshed, err := fastpass.ApplyPlaceholderRefresh(ctx, domainsByTag)
	if err != nil {
		t.Fatalf("ApplyPlaceholderRefresh: %v", err)
	}
	if refreshed != 1 {
		t.Errorf("refreshed = %d, want 1", refreshed)
	}
	if len(backend.overwriteCalls) != 1 {
		t.Fatalf("overwrite calls = %d, want 1", len(backend.overwriteCalls))
	}
	wantHead := "# 16 May 2026 at 00:35\n"
	if !strings.HasPrefix(backend.overwriteCalls[0].Content, wantHead) {
		t.Errorf("overwrite content head = %q, want prefix %q",
			strings.SplitN(backend.overwriteCalls[0].Content, "\n", 2)[0]+"\n", wantHead)
	}
}

// TestApplyPlaceholderRefresh_SkipsUntaggedNotes proves the dispatch's
// tag-verification step: a note titled "Quicknote" but NOT tagged
// with any known domain is left alone (no false-positive overwrite
// on a user-typed note that happens to share the title).
func TestApplyPlaceholderRefresh_SkipsUntaggedNotes(t *testing.T) {
	backend, ctx := setupRefreshFixture(t, fakeNote{
		ID:      "note-stranger",
		Title:   "Quicknote",
		Tags:    []string{},
		Content: "# Quicknote\nuser typed manually\n",
	})

	domainsByTag := domain.DomainsByTag([]*domain.Domain{testutil.Domain(t, "quicknote/daily")})
	refreshed, err := fastpass.ApplyPlaceholderRefresh(ctx, domainsByTag)
	if err != nil {
		t.Fatalf("ApplyPlaceholderRefresh: %v", err)
	}
	if refreshed != 0 {
		t.Errorf("refreshed = %d, want 0 (no domain tag match)", refreshed)
	}
	if len(backend.overwriteCalls) != 0 {
		t.Errorf("overwrite calls = %d, want 0", len(backend.overwriteCalls))
	}
}

// TestApplyPlaceholderRefresh_ListArgsAreGlobal locks the bearcli scan
// shape: a global created:desc scan (no --tag filter), limit 20.
// Previously the scan was --tag quicknote/daily; the generalized form
// is global because placeholders are shared across domains.
func TestApplyPlaceholderRefresh_ListArgsAreGlobal(t *testing.T) {
	backend := &fakeRefreshBackend{listPayload: []byte("[]")}
	ctx := domain.ContextWithBackend(context.Background(), backend)
	domain.ResetBearcliPoolForTest(1)

	domainsByTag := domain.DomainsByTag([]*domain.Domain{testutil.Domain(t, "quicknote/daily")})
	_, err := fastpass.ApplyPlaceholderRefresh(ctx, domainsByTag)
	if err != nil {
		t.Fatalf("ApplyPlaceholderRefresh: %v", err)
	}

	wantArgs := []string{
		"list",
		"--sort", "created:desc",
		"--limit", "20",
		"--format", "json",
		"--fields", "id,title,tags,content",
	}
	if !reflect.DeepEqual(backend.listArgsCapture, wantArgs) {
		t.Errorf("bearcli list args do not match global-scan contract\n  got:  %v\n  want: %v",
			backend.listArgsCapture, wantArgs)
	}
}
