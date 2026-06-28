// Package engine_test — domain-bootstrap fast-pass tests.
//
// Validates `fastpass.ApplyDomainBootstrap` — the new 4th fast-pass that
// canonicalizes any note whose tags match a managed leaf domain. Covers
// all five real factory shapes (`HubRouted`, `GroupedVerticalFlat`,
// `FlatList`, etc.), the umbrella-redirect path via `ResolveURLDomain`,
// multi-leaf precedence (most-specific wins / tie skipped), the
// already-canonical no-op contract via `equalIgnoringNewNoteLinkStrict`,
// and the defensive guard for umbrella-leak regressions.
//
// Test seam: reuses the `fakeAutoTagBackend` + `ContextWithBackend`
// helpers from `autotag_poll_test.go` (same `engine_test` package).
// Each test authors its own list-payload helper because
// `untaggedListPayload` carries empty tags and would not exercise any
// `matchOwningDomain` branch.
package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
	"github.com/barad1tos/noxctl/bear/render"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// listPayload marshals a slice of fake `autoTagNote` shapes (id/title/
// tags/content) into the JSON bytes `bearcli list --format json` emits.
// Per-test helpers build domain-specific payloads on top of this.
func listPayload(t *testing.T, notes []map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(notes)
	if err != nil {
		t.Fatalf("marshal list payload: %v", err)
	}
	return raw
}

// lastOverwriteBody pulls the body argument from the most recent
// "overwrite" call recorded by the fake. `bearcli overwrite` carries
// the note body on stdin (see `overwriteWithRetry` in bear/fetches.go),
// captured into `fakeAutoTagCall.Body` by the fake's `Run` method.
func lastOverwriteBody(t *testing.T, fake *fakeAutoTagBackend) string {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, call := range slices.Backward(fake.calls) {
		if call.Kind == "overwrite" {
			return call.Body
		}
	}
	t.Fatalf("no overwrite call recorded; calls=%v", fake.calls)
	return ""
}

// TestApplyDomainBootstrap_LeafDomain_HubRouted — note tagged
// `#library/lyrics` with user-typed preamble routes to the hub-routed
// `library/lyrics` leaf, hoisting preamble below the `---` separator
// and stamping `#library/lyrics | [[]]` as the canonical tag-
// line (empty bucket per `RenderCanonicalForBootstrap`).
func TestApplyDomainBootstrap_LeafDomain_HubRouted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		domains := []*domain.Domain{testutil.Domain(t, "library/lyrics")}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "lyr-1",
			"title":   "Дощ",
			"tags":    []string{"#library/lyrics"},
			"content": "user-typed preamble line\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 1 {
			t.Errorf("rewritten count = %d, want 1", n)
		}
		if got := fake.CountKind("overwrite"); got != 1 {
			t.Errorf("overwrite count = %d, want 1", got)
		}
		body := lastOverwriteBody(t, fake)
		if !strings.Contains(body, "#library/lyrics | [[]]") {
			t.Errorf("canonical tag-line missing in body; got:\n%s", body)
		}
		if !strings.Contains(body, "\n---\n") {
			t.Errorf("separator missing — preamble not hoisted; got:\n%s", body)
		}
	})
}

// TestApplyDomainBootstrap_LeafDomain_GroupedVerticalFlat — a note in a
// grouped-vertical (2-level) domain bootstraps to
// `#<tag> | [[<IndexTitle>]] | [[]]` (empty-bucket marker in the 3rd
// segment, NOT a real bucket). Uses a synthetic domain: the reference
// catalog no longer ships a grouped-vertical domain, but the blueprint
// remains supported by the engine.
func TestApplyDomainBootstrap_LeafDomain_GroupedVerticalFlat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		gv := render.NewGroupedVerticalFlatDomain(
			"library/aphorisms", "✱ Афоризми", "Невідомі",
			[]string{"Книги", "Кіно", "Ігри"},
		)
		domains := []*domain.Domain{gv}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "aph-1",
			"title":   "Орвелл",
			"tags":    []string{"#library/aphorisms"},
			"content": "War is peace.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 1 {
			t.Errorf("rewritten count = %d, want 1", n)
		}
		body := lastOverwriteBody(t, fake)
		want := "#library/aphorisms | [[✱ Афоризми]] | [[]]"
		if !strings.Contains(body, want) {
			t.Errorf("canonical tag-line missing; want substring %q\nbody:\n%s", want, body)
		}
		// Negative: post-i18n-split split MUST land on Невідомі, NOT Книги.
		if strings.Contains(body, "#library/aphorisms | [[✱ Афоризми]] | Книги") {
			t.Errorf("aphorism routed to Книги — post-i18n-split split regression\nbody:\n%s", body)
		}
	})
}

// TestApplyDomainBootstrap_LeafDomain_FlatList — note tagged
// `#llm/characters` routes to the FlatList leaf, producing
// `#llm/characters | [[✱ LLM Персонажі]]` with no bucket suffix.
func TestApplyDomainBootstrap_LeafDomain_FlatList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		domains := []*domain.Domain{testutil.Domain(t, "llm/characters")}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "chr-1",
			"title":   "Гендальф",
			"tags":    []string{"#llm/characters"},
			"content": "Wise grey wanderer.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 1 {
			t.Errorf("rewritten count = %d, want 1", n)
		}
		body := lastOverwriteBody(t, fake)
		if !strings.Contains(body, "#llm/characters | [[✱ LLM Персонажі]]") {
			t.Errorf("canonical tag-line missing; body:\n%s", body)
		}
	})
}

// TestApplyDomainBootstrap_UmbrellaRedirect — note tagged ONLY `#llm`
// (the umbrella) routes via `ResolveURLDomain` to the umbrella's
// DefaultChild leaf `llm/agents`. The canonical body MUST carry
// `#llm/agents` (the leaf tag), proving the umbrella → DefaultChild
// swap landed in the rendered output.
func TestApplyDomainBootstrap_UmbrellaRedirect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		umbrella := render.NewUmbrellaDomain(
			"llm",
			"✱ LLM",
			"llm/agents",
			[]*domain.Domain{testutil.Domain(t, "llm/agents"), testutil.Domain(t, "llm/characters")},
		)
		// Index BOTH umbrella and leaf so matchOwningDomain can resolve
		// `#llm` (umbrella-only-tag branch) → ResolveURLDomain → leaf.
		domains := []*domain.Domain{testutil.Domain(t, "llm/agents"), testutil.Domain(t, "llm/characters"), umbrella}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "llm-bare-1",
			"title":   "RouterAgent",
			"tags":    []string{"#llm"},
			"content": "Routes prompts between providers.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 1 {
			t.Errorf("rewritten count = %d, want 1 (umbrella → DefaultChild)", n)
		}
		body := lastOverwriteBody(t, fake)
		if !strings.Contains(body, "#llm/agents") {
			t.Errorf("leaf tag #llm/agents missing — umbrella redirect failed\nbody:\n%s", body)
		}
	})
}

// TestApplyDomainBootstrap_MultipleLeafsMostSpecific — note tagged
// with both `#claude` and `#llm/agents` routes to `llm/agents` because
// its tag string (10 chars) is longer than `claude` (6 chars).
// Most-specific-leaf wins.
func TestApplyDomainBootstrap_MultipleLeafsMostSpecific(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		// Construct a synthetic `claude` leaf alongside `llm/agents` to
		// exercise the multi-leaf-with-different-lengths code path.
		claude := render.NewFlatListDomain("claude", "✱ Claude")
		domains := []*domain.Domain{claude, testutil.Domain(t, "llm/agents")}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "agent-1",
			"title":   "OpusReviewer",
			"tags":    []string{"#claude", "#llm/agents"},
			"content": "PR review specialist.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 1 {
			t.Errorf("rewritten count = %d, want 1", n)
		}
		body := lastOverwriteBody(t, fake)
		if !strings.Contains(body, "#llm/agents") {
			t.Errorf("most-specific routing failed; expected #llm/agents in body:\n%s", body)
		}
		// Negative: routing to `#claude` would mean the length comparison
		// is broken — the body must NOT carry the shorter tag as canonical.
		if strings.Contains(body, "#claude |") {
			t.Errorf("shorter leaf #claude won routing — most-specific broken\nbody:\n%s", body)
		}
	})
}

// TestApplyDomainBootstrap_MultipleLeafsTieSkip — note tagged with
// two equal-length unrelated leaves (`#it/vendors` ↔ `#it/domains`,
// both 10 chars) triggers the tie-skip path: zero overwrite calls,
// WARN logged once per noteID.
func TestApplyDomainBootstrap_MultipleLeafsTieSkip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		// Two synthetic equal-length leaves; intentionally same length so
		// `mostSpecificOrSkip` triggers the tied branch.
		leafA := render.NewFlatListDomain("foo/alpha", "✱ Alpha")
		leafB := render.NewFlatListDomain("foo/bravo", "✱ Bravo")
		domains := []*domain.Domain{leafA, leafB}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "tie-1",
			"title":   "AmbiguousNote",
			"tags":    []string{"#foo/alpha", "#foo/bravo"},
			"content": "Two equal leaves claim me.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 0 {
			t.Errorf("rewritten count = %d, want 0 (tied leaves must skip)", n)
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (ambiguous note skipped)", got)
		}
	})
}

// TestApplyDomainBootstrap_AlreadyCanonicalNoOp — when a note's body
// already matches `RenderCanonicalForBootstrap`, the SSOT predicate
// `equalIgnoringNewNoteLinkStrict` returns true and the pass MUST skip
// the bearcli overwrite. Loop-prevention contract.
func TestApplyDomainBootstrap_AlreadyCanonicalNoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		domains := []*domain.Domain{testutil.Domain(t, "llm/characters")}
		domainsByTag := domain.DomainsByTag(domains)

		// Pre-render the canonical body via the SAME function the pass uses,
		// so equalIgnoringNewNoteLinkStrict returns true on the input.
		canonical := testutil.Domain(t, "llm/characters").RenderCanonicalForBootstrap("Wise grey wanderer.\n")

		payload := listPayload(t, []map[string]any{{
			"id":      "chr-canon-1",
			"title":   "Гендальф",
			"tags":    []string{"#llm/characters"},
			"content": canonical,
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 0 {
			t.Errorf("rewritten count = %d, want 0 (already canonical)", n)
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (idempotency broken)", got)
		}
	})
}

// TestApplyDomainBootstrap_StructuralNoteSkip — notes whose title
// starts with `✱ ` are project-convention hub/master/umbrella index
// notes, owned end-to-end by the per-domain regen path. Bootstrap
// pass MUST skip them regardless of which domain matched. Pre-fix,
// the umbrella → DefaultChild redirect in matchOwningDomain caused
// `✱ Library` (tag `#library`) to be stamped with the
// `library/poetry` leaf canonical, mangling the umbrella master.
// Incident #3 (2026-05-17 18:32) — 4 umbrella masters
// (`✱ Quicknote`, `✱ Library`, `✱ LLM`, `✱ IT`).
func TestApplyDomainBootstrap_StructuralNoteSkip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fastpass.ResetBootstrapLoopForTest()
		domains := []*domain.Domain{testutil.Domain(t, "llm/characters")}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "umbrella-master-1",
			"title":   "✱ LLM",
			"tags":    []string{"#llm"},
			"content": "# ✱ LLM\n\n## Розділи (4)\n- [[✱ LLM Агенти]] (10)\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 0 {
			t.Errorf("rewritten count = %d, want 0 (structural ✱-prefixed note must be skipped)", n)
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (umbrella master mangling regression)", got)
		}
	})
}

// TestApplyDomainBootstrap_SubTagBucketNoOp — bucket-as-subtag form
// (`#<tag>/<sub> | …`, the canonical shape for `NewGroupedVerticalDomain`)
// is already-canonical for the leaf and bootstrap pass MUST skip.
// Pre-fix, `hasCanonicalLineForLeaf` only recognized leaf form
// `#<tag> | …` and missed the sub-tag form — 2026-05-17 incident #2
// surfaced 129 distinct notes (health/інше, development/інше, …) hit
// the rewrite-cap of 5 across ~6 minutes before the emergency-disable
// circuit-breaker fired.
func TestApplyDomainBootstrap_SubTagBucketNoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fastpass.ResetBootstrapLoopForTest()
		domains := []*domain.Domain{testutil.Domain(t, "health")}
		domainsByTag := domain.DomainsByTag(domains)

		// Sub-tag bucket form — `#health/<unknown> | …`, the actual
		// shape produced by per-domain regen for grouped-vertical
		// leaves. The UnknownBucket label for this domain comes from
		// the catalog (see examples/personal.toml health entry).
		subTagged := "# Меню від ШІ\n" +
			"#health/інше | [[✱ Здоров'я]] | інше | [Нова нотатка](bear://x-callback-url/create?text=stub)\n" +
			"---\n\n" +
			"Based on [[Sibling Note]].\n"

		payload := listPayload(t, []map[string]any{{
			"id":      "health-subtag-1",
			"title":   "Меню від ШІ",
			"tags":    []string{"#health", "#health/інше"},
			"content": subTagged,
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 0 {
			t.Errorf("rewritten count = %d, want 0 (sub-tag bucket form must be recognized as canonical)", n)
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (grouped-vertical bucket-as-subtag regression)", got)
		}
	})
}

// TestApplyDomainBootstrap_AlreadyBucketedNoOp — when a note's body
// already carries a canonical tag-line for the leaf (even with a
// non-`UnknownBucket` value placed by a previous per-domain regen),
// bootstrap pass MUST skip — otherwise `RenderCanonicalForBootstrap`
// resets the bucket to `""` (empty) and ping-pongs with the
// per-domain `processAtomic` path that re-buckets back to the real
// category on the next tick. This is the loop that hit Roman's vault
// on 2026-05-17 (19,040 rewrites across a ~50 min window).
//
// RED contract: the existing `AlreadyCanonicalNoOp` test fed
// `RenderCanonicalForBootstrap`'s own output back in, so the no-op
// detector trivially returned true. The real-world body has a body
// produced by `processAtomic` with a NON-Unknown bucket (e.g.
// `[[Дайте-танк]]` for `library/lyrics`). That body must be left
// alone by bootstrap pass.
func TestApplyDomainBootstrap_AlreadyBucketedNoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		domains := []*domain.Domain{testutil.Domain(t, "library/lyrics")}
		domainsByTag := domain.DomainsByTag(domains)

		// Hand-crafted body that simulates per-domain regen output: real
		// bucket `[[Дайте-танк]]` (atomic-canonical form), NOT
		// the bootstrap form's `[[]]` (empty bucket).
		bucketed := "# Я\n" +
			"#library/lyrics | [[Дайте-танк]] | [Нова нотатка](bear://x-callback-url/create?text=stub)\n" +
			"---\n\n" +
			"Placeholder atomic body — content shape is irrelevant to this assertion.\n"

		payload := listPayload(t, []map[string]any{{
			"id":      "lyr-bucketed-1",
			"title":   "Я",
			"tags":    []string{"#library", "#library/lyrics"},
			"content": bucketed,
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 0 {
			t.Errorf("rewritten count = %d, want 0 (already canonical, real bucket)", n)
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (bucket-rewrite loop regression)", got)
		}
	})
}

// TestApplyDomainBootstrap_NoManagedTagSkip — note tagged ONLY with
// an unmanaged tag triggers `matchOwningDomain` → nil → skip.
// Explicit nil-gate behavioral lock.
func TestApplyDomainBootstrap_NoManagedTagSkip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		domains := []*domain.Domain{testutil.Domain(t, "llm/characters")}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "stray-1",
			"title":   "Random",
			"tags":    []string{"#randomthing"},
			"content": "Not our note.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 0 {
			t.Errorf("rewritten count = %d, want 0", n)
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (unmanaged tag must skip)", got)
		}
	})
}

// TestApplyDomainBootstrap_DefensiveUmbrellaGuard —
// behavioral lock for the `if d.SkipAtomicsPass` defensive guard
// inside `ApplyDomainBootstrap`. Constructs a bare umbrella whose
// `defaultChildDomain` is unresolved (no child wiring), so
// `ResolveURLDomain` returns the umbrella itself. The guard MUST
// fire and skip the note, producing zero overwrites.
//
// Today this path is reached when an umbrella ends up in
// `domainsByTag` without its child wiring — a contract violation that
// would otherwise produce malformed canonical output. The defensive
// branch locks against future refactors that drop the
// `matchOwningDomain`-side resolution.
func TestApplyDomainBootstrap_DefensiveUmbrellaGuard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		// Bare umbrella — no DefaultChild leaf wired. ResolveURLDomain
		// returns self when defaultChildDomain == nil (see
		// bear/methods.go for the post-PR-H3 location).
		bareUmbrella := &domain.Domain{
			Tag:             "phantom",
			CanonicalTag:    "#phantom",
			IndexTitle:      "✱ Phantom",
			UnknownBucket:   "Other",
			SkipAtomicsPass: true,
		}
		domainsByTag := map[string]*domain.Domain{
			"phantom": bareUmbrella,
		}

		payload := listPayload(t, []map[string]any{{
			"id":      "phantom-1",
			"title":   "Leak",
			"tags":    []string{"#phantom"},
			"content": "umbrella tag only — must trigger guard.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		n, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("ApplyDomainBootstrap: %v", err)
		}
		if n != 0 {
			t.Errorf("rewritten count = %d, want 0 (umbrella leak guard must skip)", n)
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (defensive guard failed)", got)
		}
	})
}

// TestApplyDomainBootstrap_LoopGuard_SkipsAfterCap drives the same
// non-canonical note through ApplyDomainBootstrap 5+1 times. Each of
// the first 5 calls fires an overwrite (the fake backend serves the
// same canned input each time — the bootstrap pass never sees its
// own write reflected back). On call N=5, `recordRewrite` marks the
// note "stuck" (count == `bootstrapNoteRewriteCap`). Call N=6 hits
// `shouldSkipNote` and returns 0 — defense-in-depth circuit-breaker.
func TestApplyDomainBootstrap_LoopGuard_SkipsAfterCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fastpass.ResetBootstrapLoopForTest()
		domains := []*domain.Domain{testutil.Domain(t, "llm/characters")}
		domainsByTag := domain.DomainsByTag(domains)

		payload := listPayload(t, []map[string]any{{
			"id":      "loop-cap-1",
			"title":   "Гендальф",
			"tags":    []string{"#llm/characters"},
			"content": "Wise grey wanderer.\n",
		}})
		fake := newFakeAutoTagBackend(payload)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		const noteCap = 5
		driveBootstrapNTimes(t, ctx, domainsByTag, noteCap, 1)
		assertOverwriteCount(t, fake, noteCap, "cumulative")
		// (noteCap+1)th call — guard MUST fire, zero new overwrites.
		driveBootstrapNTimes(t, ctx, domainsByTag, 1, 0)
		assertOverwriteCount(t, fake, noteCap, "post-cap")
	})
}

// driveBootstrapNTimes invokes ApplyDomainBootstrap n times, asserting
// every call returns wantPerCall rewrites. Test-helper extracted to
// keep LoopGuard_SkipsAfterCap under the gocognit ≤15 budget.
func driveBootstrapNTimes(
	t *testing.T,
	ctx context.Context,
	domainsByTag map[string]*domain.Domain,
	n, wantPerCall int,
) {
	t.Helper()
	for i := 1; i <= n; i++ {
		got, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
		if err != nil {
			t.Fatalf("call %d: ApplyDomainBootstrap: %v", i, err)
		}
		if got != wantPerCall {
			t.Errorf("call %d: rewritten = %d, want %d", i, got, wantPerCall)
		}
	}
}

// assertOverwriteCount checks the fake's cumulative overwrite count
// with a label-prefixed error message so multi-phase test diagnoses
// stay readable.
func assertOverwriteCount(t *testing.T, fake *fakeAutoTagBackend, want int, label string) {
	t.Helper()
	if got := fake.CountKind("overwrite"); got != want {
		t.Errorf("%s overwrites = %d, want %d", label, got, want)
	}
}

// TestApplyDomainBootstrap_SourceRegexUmbrellaGuard is a source-regex
// regression lock asserting the defensive
// `if d.SkipAtomicsPass` branch literal stays present in
// bear/fastpass/bootstrap.go AND that the word "umbrella" appears within 5
// lines of it (the comment that explains why the branch exists).
// Future refactors that delete the guard as "unreachable dead code"
// will trip this test instead of silently restoring the bug class.
func TestApplyDomainBootstrap_SourceRegexUmbrellaGuard(t *testing.T) {
	source, err := os.ReadFile("../../../bear/fastpass/bootstrap.go")
	if err != nil {
		t.Fatalf("read bear/fastpass/bootstrap.go: %v", err)
	}
	lines := strings.Split(string(source), "\n")
	guardRE := regexp.MustCompile(`if\s+d\.SkipAtomicsPass`)
	umbrellaRE := regexp.MustCompile(`(?i)umbrella`)

	var guardLines []int
	for i, line := range lines {
		if guardRE.MatchString(line) {
			guardLines = append(guardLines, i)
		}
	}
	if len(guardLines) == 0 {
		t.Fatalf("defensive `if d.SkipAtomicsPass` guard missing from bear/fastpass/bootstrap.go — defensive-guard regression lock failed")
	}
	for _, gl := range guardLines {
		// Scan ±5 lines for the word "umbrella" — the comment that
		// explains why this branch exists. Empty window means the
		// guard was kept but its rationale was deleted; future
		// reviewer might still delete it as unmotivated.
		lo := max(0, gl-5)
		hi := min(len(lines), gl+6)
		window := strings.Join(lines[lo:hi], "\n")
		if !umbrellaRE.MatchString(window) {
			t.Errorf("guard at line %d has no 'umbrella' context in ±5 lines — rationale lost", gl+1)
		}
	}
}
