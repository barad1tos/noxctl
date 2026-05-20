package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
)

func TestHubTitleSubTag(t *testing.T) {
	d := &bear.Domain{Tag: "claude"}
	if got := bear.HubTitleSubTag(d, "sessions"); got != "claude · sessions" {
		t.Errorf("got %q", got)
	}
}

func TestBucketFromHubTitleSubTag(t *testing.T) {
	d := &bear.Domain{Tag: "claude"}
	cases := map[string]string{
		"claude · sessions": "sessions",
		"claude · memory":   "memory",
		"unrelated note":    "",
		"claude":            "", // missing separator
		"claudex · weird":   "", // wrong prefix
	}
	for input, want := range cases {
		if got := bear.BucketFromHubTitleSubTag(d, input); got != want {
			t.Errorf("%q: got %q, want %q", input, got, want)
		}
	}
}

func TestIsHubNoteSubTag(t *testing.T) {
	d := &bear.Domain{Tag: "claude"}
	if !bear.IsHubNoteSubTag(d, bear.Note{Title: "claude · sessions"}) {
		t.Error("hub-shaped title should match")
	}
	if bear.IsHubNoteSubTag(d, bear.Note{Title: "Some atom"}) {
		t.Error("non-hub title should not match")
	}
	if bear.IsHubNoteSubTag(d, bear.Note{Title: "✱ Claude"}) {
		t.Error("master title should not match as hub")
	}
}

func TestHubBacklinkSubTagShape(t *testing.T) {
	d := &bear.Domain{Tag: "claude"}
	got := bear.HubBacklinkSubTag(d, "sessions")
	if got != "[[claude · sessions]]" {
		t.Errorf("got %q", got)
	}
}

func TestRenderHubFlatSubTag(t *testing.T) {
	d := &bear.Domain{
		Tag: "claude", CanonicalTag: "#claude", IndexTitle: "✱ Claude",
		HubTitleFor:     bear.HubTitleSubTag,
		CanonicalTagFor: bear.SubTagCanonical,
	}
	notes := []bear.Note{
		{ID: "1", Title: "Beta"},
		{ID: "2", Title: "Alpha"},
	}
	out := bear.RenderHubFlatSubTag(d, "sessions", notes, nil)
	// Bootstrap URL form — outer URL starts with `?text=` (encoded body).
	// The inner URL's `tags=claude` lives doubly-encoded inside text= as
	// `tags%3Dclaude`.
	wantHeaderPrefix := "# claude · sessions\n#claude/sessions | [[✱ Claude]] | [Нова нотатка](bear://x-callback-url/create?text="
	if !strings.HasPrefix(out, wantHeaderPrefix) {
		t.Errorf("missing canonical header, got prefix:\n%s", out[:min(180, len(out))])
	}
	if !strings.Contains(out, "tags%3Dclaude") {
		t.Errorf("bootstrap URL inner tag missing:\n%s", out[:min(240, len(out))])
	}
	if strings.Index(out, "Alpha") > strings.Index(out, "Beta") {
		t.Error("hub bullets not alphabetised")
	}
}

func TestRenderMasterHubList(t *testing.T) {
	d := &bear.Domain{
		Tag: "claude", CanonicalTag: "#claude", IndexTitle: "✱ Claude",
		HubTitleFor: bear.HubTitleSubTag,
	}
	groups := map[string][]bear.Note{
		"sessions": {{Title: "a"}, {Title: "b"}},
		"memory":   {{Title: "c"}},
		"empty":    {}, // should not render
	}
	out := bear.RenderMasterHubList(d, groups, []string{"sessions", "memory", "empty"})
	if !strings.HasPrefix(out, "# ✱ Claude\n#claude | [Нова нотатка](") {
		t.Errorf("missing master header, got prefix: %q", out[:min(80, len(out))])
	}
	// Bootstrap URL form — inner `tags=claude` lives doubly-encoded as
	// `tags%3Dclaude` inside outer `text=` payload.
	if !strings.Contains(out, "tags%3Dclaude") {
		t.Error("master header bootstrap URL should carry the doubly-encoded inner tag")
	}
	if strings.Contains(out, "title=") {
		t.Error("master header link must NOT carry title= (spec component 1) — title is stamped via StampDatetimeH1 at regen time")
	}
	if !strings.Contains(out, "&open_note=yes") {
		t.Error("master header link should ask Bear to open the new note after creation")
	}
	if !strings.Contains(out, "## Категорії (3)") {
		t.Errorf("expected category count 3, got: %s", out)
	}
	if !strings.Contains(out, "[[claude · sessions]] (2)") {
		t.Error("sessions hub link missing")
	}
	if !strings.Contains(out, "[[claude · memory]] (1)") {
		t.Error("memory hub link missing")
	}
	if strings.Contains(out, "claude · empty") {
		t.Error("empty bucket should not produce a hub link")
	}
}

func TestNewHubRoutedSubTagDomainWiresCallbacks(t *testing.T) {
	d := bear.NewHubRoutedSubTagDomain("claude", "✱ Claude", "інше", []string{"sessions"})
	if d.IsHubNote == nil || d.HubTitleFor == nil || d.BucketFromHubTitle == nil {
		t.Error("expected all sub-tag callbacks wired")
	}
	if d.CanonicalTagFor == nil {
		t.Error("expected CanonicalTagFor wired")
	}
	if d.RenderHub == nil || d.RenderMaster == nil {
		t.Error("expected RenderHub and RenderMaster wired")
	}
	// Validate sub-tag canonical via the public callback.
	if got := d.CanonicalTagFor(d, "sessions"); got != "#claude/sessions" {
		t.Errorf("CanonicalTagFor: got %q", got)
	}
	// Hub-title roundtrip via public callbacks.
	hubTitle := d.HubTitleFor(d, "sessions")
	if hubTitle != "claude · sessions" {
		t.Errorf("HubTitleFor: got %q", hubTitle)
	}
	if got := d.BucketFromHubTitle(d, hubTitle); got != "sessions" {
		t.Errorf("BucketFromHubTitle: got %q", got)
	}
}
