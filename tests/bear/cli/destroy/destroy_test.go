// Package destroy_test covers the pure helpers inside
// bear/cli/destroy that drive the strip/trash sweep. The
// bearcli-side mutations (TrashNote / OverwriteNoteContent) are
// exercised end-to-end via the cobra smoke test; this file pins the
// string-shape contracts that the orchestrator depends on.
package destroy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// destroyFake is a bearcli.Backend for the RunDestroy orchestration tests:
// serves a canned `list` payload, records `trash` note IDs, stubs `show`
// hash, and counts `overwrite` (canonical-strip) calls.
type destroyFake struct {
	listPayload []byte
	trashErr    error // when set, every `trash` call fails (partial-failure path)
	trashed     []string
	overwrites  int
}

func (f *destroyFake) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte("[]"), nil
	}
	switch args[0] {
	case "list":
		return f.listPayload, nil
	case "trash":
		if f.trashErr != nil {
			return nil, f.trashErr
		}
		if len(args) >= 2 {
			f.trashed = append(f.trashed, args[1])
		}
		return []byte(`{"ok":true}`), nil
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		f.overwrites++
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
}

// destroyListJSON marshals bearcli `list` rows for the destroy fixture.
func destroyListJSON(t *testing.T, rows ...map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal destroy list: %v", err)
	}
	return payload
}

// TestStripCanonical_RemovesTagLines covers the happy path: every
// line whose first token is the canonical tag (with or without a
// sub-tag) is dropped. Body content survives untouched.
func TestStripCanonical_RemovesTagLines(t *testing.T) {
	body := "# Title\n" +
		"#library/poetry | [[✱ Poetry]] | Shakespeare\n" +
		"The body line one.\n" +
		"Body line two.\n"

	got, changed := cli.StripCanonical(body, "#library/poetry")
	if !changed {
		t.Fatal("expected changed=true when canonical line is present")
	}
	if strings.Contains(got, "library/poetry") {
		t.Errorf("canonical line still present after strip:\n%s", got)
	}
	for _, want := range []string{"# Title", "The body line one.", "Body line two."} {
		if !strings.Contains(got, want) {
			t.Errorf("body content lost: missing %q\n%s", want, got)
		}
	}
}

// TestStripCanonical_NoChange ensures changed=false when no canonical
// line matches. Avoids spurious overwrite writes when destroy
// double-passes an already-stripped note.
func TestStripCanonical_NoChange(t *testing.T) {
	body := "# Title\n#other/tag\nbody.\n"
	got, changed := cli.StripCanonical(body, "#library/poetry")
	if changed {
		t.Error("expected changed=false when canonical tag is absent")
	}
	if got != body {
		t.Error("body should be returned unchanged when nothing to strip")
	}
}

// TestStripCanonical_PrefixGuard pins the prefix-collision defense:
// `#library` must NOT match `#libraryother` or `#library-prose`.
// The separator after the canonical tag (slash / pipe / space) is
// what keeps `#library` from eating unrelated sibling tags.
func TestStripCanonical_PrefixGuard(t *testing.T) {
	body := "# Title\n#libraryother | [[Other]]\n#library-prose\nbody.\n"
	got, changed := cli.StripCanonical(body, "#library")
	if changed {
		t.Errorf("prefix-collision: #library matched #libraryother / #library-prose\nresult:\n%s", got)
	}
}

// TestPromptConfirm pins the type-to-confirm gate's accept / abort
// contract. Mismatched tag, empty line, EOF all collapse to
// ErrAborted; exact match (with surrounding whitespace tolerated)
// accepts. This is the human-side safety guard on a destructive
// verb — every case below should be considered load-bearing.
func TestPromptConfirm(t *testing.T) {
	cases := []struct {
		name      string
		stdin     string
		tag       string
		wantAbort bool
	}{
		{"exact match accepts", "library/poetry\n", "library/poetry", false},
		{"trailing whitespace tolerated", "  library/poetry  \n", "library/poetry", false},
		{"mismatch aborts", "library/poet\n", "library/poetry", true},
		{"empty line aborts", "\n", "library/poetry", true},
		{"EOF aborts", "", "library/poetry", true},
		{"prefix collision aborts", "library/poetry-extra\n", "library/poetry", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := cli.PromptConfirmForTest(&out, strings.NewReader(tc.stdin), tc.tag)
			gotAbort := errors.Is(err, cli.ErrAborted)
			if gotAbort != tc.wantAbort {
				t.Errorf("abort=%v err=%v want abort=%v", gotAbort, err, tc.wantAbort)
			}
			if !tc.wantAbort && err != nil {
				t.Errorf("expected accept; got err=%v", err)
			}
		})
	}
}

// TestRenderPreview_TruncatesAtomicsAboveFive pins the atomic-list
// overflow shape: at most 5 names are spelled out, the remainder
// rendered as "... and N more.". A regression that bumps the limit
// or changes the truncation copy must show up here.
func TestRenderPreview_TruncatesAtomicsAboveFive(t *testing.T) {
	atomics := make([]domain.Note, 8)
	for i := range atomics {
		atomics[i] = domain.Note{
			ID:    fmt.Sprintf("id%c", 'a'+i),
			Title: fmt.Sprintf("Atom%c", 'A'+i),
		}
	}
	var buf bytes.Buffer
	cli.RenderPreviewForTest(&buf, "library/test", nil, atomics)
	out := buf.String()
	// First five names appear in full.
	for i := range 5 {
		want := fmt.Sprintf("Atom%c", 'A'+i)
		if !strings.Contains(out, want) {
			t.Errorf("preview missing first-five name %q\n%s", want, out)
		}
	}
	// Remainder collapsed.
	if !strings.Contains(out, "... and 3 more.") {
		t.Errorf("preview missing overflow line; got:\n%s", out)
	}
	// Sixth onward NOT listed by name.
	if strings.Contains(out, "AtomF") {
		t.Errorf("preview should not name AtomF (slot 6); got:\n%s", out)
	}
}

// TestRenderPreview_NoTruncationAtFiveOrFewer pins the inverse: at
// exactly 5 atomics the "and N more" line must NOT render. Catches
// off-by-one bugs in the overflow guard.
func TestRenderPreview_NoTruncationAtFiveOrFewer(t *testing.T) {
	atomics := []domain.Note{
		{ID: "1", Title: "A"},
		{ID: "2", Title: "B"},
		{ID: "3", Title: "C"},
		{ID: "4", Title: "D"},
		{ID: "5", Title: "E"},
	}
	var buf bytes.Buffer
	cli.RenderPreviewForTest(&buf, "library/test", nil, atomics)
	if strings.Contains(buf.String(), "more.") {
		t.Errorf("5 atomics should not trigger overflow line; got:\n%s", buf.String())
	}
}

// TestRunDestroy_ReturnsErrTagNotManaged_WhenTagAbsentFromCatalog pins the
// guard the operator hits first: destroying a tag with no domain stanza must
// fail with ErrTagNotManaged before any bearcli call. User-facing bug if this
// regresses: a typo'd tag silently no-ops (operator thinks they destroyed
// something) or worse, lists/mutates an unintended tag.
func TestRunDestroy_ReturnsErrTagNotManaged_WhenTagAbsentFromCatalog(t *testing.T) {
	domains := []*domain.Domain{render.NewFlatListDomain("library/test", "✱ Test")}
	var out, errBuf bytes.Buffer
	err := cli.RunDestroy(context.Background(), cli.DestroyOptions{
		Domains: domains,
		Tag:     "library/nonexistent",
		Stdout:  &out,
		Stderr:  &errBuf,
		Stdin:   strings.NewReader(""),
	})
	if !errors.Is(err, cli.ErrTagNotManaged) {
		t.Errorf("err = %v, want ErrTagNotManaged for an unknown tag", err)
	}
}

// TestRunDestroy_Aborts_WhenConfirmationMismatches pins the type-to-confirm
// safety gate: with a populated tag and a wrong confirmation line, RunDestroy
// must abort and issue ZERO mutations. User-facing bug if this regresses: a
// fat-fingered confirmation still nukes the master + strips every atom.
//
//cyrillic:permit
func TestRunDestroy_Aborts_WhenConfirmationMismatches(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	fake := &destroyFake{listPayload: destroyListJSON(
		t,
		map[string]any{"id": "m1", "title": "✱ Test", "tags": []string{"#library/test"}, "content": "# ✱ Test\n"},
		map[string]any{
			"id": "a1", "title": "Вірш", "tags": []string{"#library/test"},
			"content": "# Вірш\n#library/test | [[✱ Test]]\n---\nтіло\n",
		},
	)}
	ctx := domain.ContextWithBackend(context.Background(), fake)

	var out, errBuf bytes.Buffer
	err := cli.RunDestroy(ctx, cli.DestroyOptions{
		Domains: []*domain.Domain{render.NewFlatListDomain("library/test", "✱ Test")},
		Tag:     "library/test",
		Stdout:  &out, Stderr: &errBuf,
		Stdin: strings.NewReader("wrong-tag\n"), // does not match → abort
	})
	if !errors.Is(err, cli.ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted on confirmation mismatch", err)
	}
	if len(fake.trashed) != 0 || fake.overwrites != 0 {
		t.Errorf("aborted destroy still mutated: trashed=%v overwrites=%d (want zero of both)",
			fake.trashed, fake.overwrites)
	}
}

// TestRunDestroy_TrashesMasterAndStripsAtoms_OnAutoApprove pins the happy
// destroy journey: with --auto-approve, the master/hub note is trashed and
// each atom's canonical line is stripped (atom body survives). User-facing
// bug if this regresses: destroy either leaves the auto-generated master
// behind or deletes the operator's atom content instead of just unlinking it.
//
//cyrillic:permit
func TestRunDestroy_TrashesMasterAndStripsAtoms_OnAutoApprove(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	fake := &destroyFake{listPayload: destroyListJSON(
		t,
		map[string]any{"id": "m1", "title": "✱ Test", "tags": []string{"#library/test"}, "content": "# ✱ Test\n"},
		map[string]any{
			"id": "a1", "title": "Вірш", "tags": []string{"#library/test"},
			"content": "# Вірш\n#library/test | [[✱ Test]]\n---\nтіло\n",
		},
	)}
	ctx := domain.ContextWithBackend(context.Background(), fake)

	var out, errBuf bytes.Buffer
	err := cli.RunDestroy(ctx, cli.DestroyOptions{
		Domains:     []*domain.Domain{render.NewFlatListDomain("library/test", "✱ Test")},
		Tag:         "library/test",
		AutoApprove: true,
		Stdout:      &out, Stderr: &errBuf,
		Stdin: strings.NewReader(""),
	})
	if err != nil {
		t.Fatalf("RunDestroy auto-approve: %v", err)
	}
	if len(fake.trashed) != 1 || fake.trashed[0] != "m1" {
		t.Errorf("master/hub not trashed exactly once: trashed=%v, want [m1]", fake.trashed)
	}
	if fake.overwrites != 1 {
		t.Errorf("atom canonical strip count = %d, want 1 (the single atom)", fake.overwrites)
	}
	if !strings.Contains(out.String(), "trashed 1 master/hub notes, stripped 1 atomic") {
		t.Errorf("summary line missing expected counts; got:\n%s", out.String())
	}
}

// TestRunDestroy_ReportsFailures_WhenTrashFails pins the partial-failure
// path: when bearcli rejects the master/hub trash, RunDestroy must count the
// failure, surface it in the summary, and return a non-nil error — never
// report success. The atom strip still runs (log-and-continue), so the
// operator sees an honest "trashed 0 ... stripped 1 ... 1 failures" line.
// User-facing bug if this regresses: a destroy that half-failed reads as
// complete and the operator never retries the stuck master.
//
//cyrillic:permit
func TestRunDestroy_ReportsFailures_WhenTrashFails(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	fake := &destroyFake{
		trashErr: errors.New("bearcli trash: simulated rejection"),
		listPayload: destroyListJSON(
			t,
			map[string]any{"id": "m1", "title": "✱ Test", "tags": []string{"#library/test"}, "content": "# ✱ Test\n"},
			map[string]any{
				"id": "a1", "title": "Вірш", "tags": []string{"#library/test"},
				"content": "# Вірш\n#library/test | [[✱ Test]]\n---\nтіло\n",
			},
		),
	}
	ctx := domain.ContextWithBackend(context.Background(), fake)

	var out, errBuf bytes.Buffer
	err := cli.RunDestroy(ctx, cli.DestroyOptions{
		Domains:     []*domain.Domain{render.NewFlatListDomain("library/test", "✱ Test")},
		Tag:         "library/test",
		AutoApprove: true,
		Stdout:      &out, Stderr: &errBuf,
		Stdin: strings.NewReader(""),
	})
	if err == nil {
		t.Fatalf("RunDestroy returned nil despite a trash failure; want a wrapped error")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error should report the failure; got %v", err)
	}
	if !strings.Contains(out.String(), "1 failures") {
		t.Errorf("summary must report the failure count; got:\n%s", out.String())
	}
	if len(fake.trashed) != 0 {
		t.Errorf("trash failed, so nothing should be recorded as trashed; got %v", fake.trashed)
	}
}

// TestStripCanonical_AcceptedShapes pins the two non-trivial
// canonical-line shapes that must still be recognized as a strip
// target:
//
//   - sub-tag form (`#library/poetry/Shakespeare | …`) — some
//     blueprints emit this; the slash separator after the canonical
//     tag is valid.
//   - leading whitespace (`  #library/poetry | …`) — pasted-from-
//     elsewhere notes sometimes carry indentation; TrimLeft inside
//     startsWithCanonical handles it.
func TestStripCanonical_AcceptedShapes(t *testing.T) {
	cases := []struct {
		label string
		body  string
	}{
		{"sub-tag form", "# Title\n#library/poetry/Shakespeare | [[Master]]\nbody.\n"},
		{"leading whitespace", "# Title\n  #library/poetry | [[Master]]\nbody.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, changed := cli.StripCanonical(tc.body, "#library/poetry")
			if !changed {
				t.Fatalf("%s: expected changed=true", tc.label)
			}
			if strings.Contains(got, "library/poetry") {
				t.Errorf("%s: canonical line still present:\n%s", tc.label, got)
			}
		})
	}
}
