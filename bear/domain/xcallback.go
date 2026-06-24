package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// NewNoteURLForm classifies the shape of a new-note pipe-segment.
type NewNoteURLForm int

const (
	// FormBootstrap means text= carries the full canonical body plus
	// edit=yes. This is the current canonical shape every domain emits.
	FormBootstrap NewNoteURLForm = iota
	// FormSimple means tags= and open_note=yes only — the legacy
	// pre-bootstrap shape, still produced as the Inner URL inside
	// FormBootstrap.
	FormSimple
	// FormLegacyTitle is the historical shape carrying a title=
	// parameter. Kept for round-trip parsing only; production never
	// emits this form.
	FormLegacyTitle
)

// NewNoteURL is the structured representation of the trailing new-note
// pipe-segment that decorates every canonical tag-line. Always trailing,
// always pipe-prefixed (" | [<Label>](bear://...)"); never free-standing.
//
// Round-trip contract: ParseNewNoteURLSegment(u.Emit[3:]) == u for
// every Form. Structural equality (Equals) replaces string-substring
// drift detection — any change in Tag, CanonicalTag, Backlink,
// PlaceholderH1, Label, Form, or Inner triggers rewrite via
// equalIgnoringNewNoteLinkStrict.
type NewNoteURL struct {
	// Tag is the raw bearcli tag for the inner click target (no `#`).
	Tag string
	// CanonicalTag is the outer "#<tag>" prefix that appears in the
	// embedded body's canonical line. Empty for non-bootstrap forms.
	CanonicalTag string
	// Backlink is the bucket backlink emitted inside the embedded body.
	// e.g. "[[✱ Daily]]". Empty for non-bootstrap forms.
	Backlink string
	// PlaceholderH1 is the H1 marker text inside the embedded body.
	// e.g. "Quicknote". Empty for non-bootstrap forms.
	PlaceholderH1 string
	// Label is the localized "new note" link text (e.g. "Нова нотатка").
	Label string
	// Form classifies the URL shape.
	Form NewNoteURLForm
	// Inner is the nested simple-form URL embedded inside text= for
	// FormBootstrap. nil for FormSimple / FormLegacyTitle.
	Inner *NewNoteURL
}

// NewNoteURLFromDomain builds the canonical NewNoteURL for d. Umbrella
// domains transparently resolve to their DefaultChild leaf — the emitted
// URL lands clicks in the leaf's tag space.
func NewNoteURLFromDomain(d *Domain) NewNoteURL {
	leaf := d.ResolveURLDomain()
	inner := &NewNoteURL{
		Tag:   leaf.newNoteRawTag(),
		Label: T("new-note.label"),
		Form:  FormSimple,
	}
	return NewNoteURL{
		Tag:           leaf.newNoteRawTag(),
		CanonicalTag:  leaf.CanonicalTag,
		Backlink:      "[[]]",
		PlaceholderH1: leaf.EffectiveQuickPlaceholderH1(),
		Label:         T("new-note.label"),
		Form:          FormBootstrap,
		Inner:         inner,
	}
}

// Emit produces the trailing pipe-separated tag-line segment, including
// the leading " | " separator.
func (u NewNoteURL) Emit() string {
	switch u.Form {
	case FormBootstrap:
		canonicalBody := fmt.Sprintf(
			"# %s\n%s | %s | %s\n---\n\n",
			u.PlaceholderH1,
			u.CanonicalTag,
			u.Backlink,
			u.Inner.emitMarkdownLink(),
		)
		outer := fmt.Sprintf(
			"bear://x-callback-url/create?text=%s&edit=yes&open_note=yes",
			urlEncodeRFC3986(canonicalBody),
		)
		return " | [" + u.Label + "](" + outer + ")"
	case FormSimple:
		return " | " + u.emitMarkdownLink()
	case FormLegacyTitle:
		// Round-trip support only; production never emits this form.
		inner := fmt.Sprintf(
			"bear://x-callback-url/create?tags=%s&open_note=yes",
			urlEncodeRFC3986(u.Tag),
		)
		return " | [" + u.Label + "](" + inner + ")"
	default:
		return ""
	}
}

// emitMarkdownLink returns the bare "[<Label>](bear://...)" without the
// leading " | " separator. Used by FormBootstrap to embed the inner URL.
func (u NewNoteURL) emitMarkdownLink() string {
	inner := fmt.Sprintf(
		"bear://x-callback-url/create?tags=%s&open_note=yes",
		urlEncodeRFC3986(u.Tag),
	)
	return "[" + u.Label + "](" + inner + ")"
}

// newNoteSegmentBodyRegex matches "[<label>](bear://x-callback-url/create...)"
// (the segment body, with the leading " | " separator already stripped
// by the caller). One regex — replaces the historical pair of
// newNoteLinkRegex + newNoteSegmentRegex.
var newNoteSegmentBodyRegex = regexp.MustCompile(`^\[([^]]+)]\(bear://x-callback-url/create\?([^)]+)\)$`)

// newNoteFullSegmentRegex matches " | [<label>](bear://...)" anchored to
// the leading " | " separator — preventing user-pasted bear://create
// literals (without the pipe prefix) from being falsely picked up.
var newNoteFullSegmentRegex = regexp.MustCompile(` \| \[[^]]+]\(bear://x-callback-url/create[^)]*\)`)

// ParseNewNoteURLSegment parses a segment (WITHOUT the leading " | ")
// into a NewNoteURL. Returns ok=false for malformed segments or non-
// new-note shapes (user-authored bear://create deep-links etc).
func ParseNewNoteURLSegment(segment string) (NewNoteURL, bool) {
	match := newNoteSegmentBodyRegex.FindStringSubmatch(strings.TrimSpace(segment))
	if match == nil {
		return NewNoteURL{}, false
	}
	label := match[1]
	query := match[2]
	params, err := url.ParseQuery(query)
	if err != nil {
		return NewNoteURL{}, false
	}
	if text := params.Get("text"); text != "" {
		return parseBootstrapForm(label, text)
	}
	if params.Get("title") != "" {
		return NewNoteURL{
			Tag:   params.Get("tags"),
			Label: label,
			Form:  FormLegacyTitle,
		}, true
	}
	return NewNoteURL{
		Tag:   params.Get("tags"),
		Label: label,
		Form:  FormSimple,
	}, true
}

// parseBootstrapForm decodes the text= canonical body to recover Tag,
// CanonicalTag, Backlink, PlaceholderH1, and the inner simple URL.
// Expected body shape:
//
//	# <PlaceholderH1>
//	<CanonicalTag> | <Backlink> | [<innerLabel>](bear://x-callback-url/create?tags=<tag>&open_note=yes)
//	---
//
// (trailing blank line and separator may or may not be present).
func parseBootstrapForm(label, encodedText string) (NewNoteURL, bool) {
	text, err := url.QueryUnescape(encodedText)
	if err != nil {
		return NewNoteURL{}, false
	}
	lines := strings.SplitN(text, "\n", 3)
	if len(lines) < 2 {
		return NewNoteURL{}, false
	}
	h1Line := lines[0]
	canonicalLine := lines[1]
	if !strings.HasPrefix(h1Line, "# ") {
		return NewNoteURL{}, false
	}
	placeholderH1 := strings.TrimPrefix(h1Line, "# ")
	parts := strings.Split(canonicalLine, " | ")
	if len(parts) < 3 {
		return NewNoteURL{}, false
	}
	canonicalTag := parts[0]
	backlink := parts[1]
	innerSegmentBody := parts[len(parts)-1]
	inner, ok := ParseNewNoteURLSegment(innerSegmentBody)
	if !ok {
		return NewNoteURL{}, false
	}
	innerCopy := inner
	return NewNoteURL{
		Tag:           innerCopy.Tag,
		CanonicalTag:  canonicalTag,
		Backlink:      backlink,
		PlaceholderH1: placeholderH1,
		Label:         label,
		Form:          FormBootstrap,
		Inner:         &innerCopy,
	}, true
}

// Equals reports whether u and other carry identical structural state.
// Replaces string-substring comparison — catches Backlink/PlaceholderH1/
// label/tag drift that previously slipped through bootstrap-form check.
func (u NewNoteURL) Equals(other NewNoteURL) bool {
	if u.Tag != other.Tag ||
		u.CanonicalTag != other.CanonicalTag ||
		u.Backlink != other.Backlink ||
		u.PlaceholderH1 != other.PlaceholderH1 ||
		u.Label != other.Label ||
		u.Form != other.Form {
		return false
	}
	if (u.Inner == nil) != (other.Inner == nil) {
		return false
	}
	if u.Inner != nil && !u.Inner.Equals(*other.Inner) {
		return false
	}
	return true
}

// FindAllNewNoteURLsInBody scans body for canonical-tag-line new-note
// decorations and returns them parsed. Anchored to the leading " | "
// separator — user-pasted bear://create literals (without the pipe
// prefix) are NOT returned.
func FindAllNewNoteURLsInBody(body string) []NewNoteURL {
	matches := newNoteFullSegmentRegex.FindAllString(body, -1)
	urls := make([]NewNoteURL, 0, len(matches))
	for _, match := range matches {
		segment := strings.TrimPrefix(match, " | ")
		if u, ok := ParseNewNoteURLSegment(segment); ok {
			urls = append(urls, u)
		}
	}
	return urls
}

// StripNewNoteURLsFromBody removes every canonical-tag-line new-note
// decoration from body. Anchored to " | " separator (user-pasted
// bear:// literals are preserved).
func StripNewNoteURLsFromBody(body string) string {
	return newNoteFullSegmentRegex.ReplaceAllString(body, "")
}

// DropTrailingNewNoteURLSegment drops the final element of parts when
// it parses as a new-note URL segment. ParseMeta variants call this
// before indexing parts so the section/bucket fields don't get
// corrupted by the URL text.
func DropTrailingNewNoteURLSegment(parts []string) []string {
	if len(parts) == 0 {
		return parts
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if _, ok := ParseNewNoteURLSegment(last); ok {
		return parts[:len(parts)-1]
	}
	return parts
}

// urlEncodeRFC3986 percent-encodes a URL component the way RFC 3986
// specifies — spaces as %20, NOT as `+`. Standard library
// `url.QueryEscape` follows `application/x-www-form-urlencoded` and
// encodes spaces as `+`, which Bear's x-callback parser does NOT
// decode back to spaces (it only honors `%20`). Result: titles like
// "8 May 2026 at 00:49" passed through QueryEscape become
// `8+May+2026+at+00%3A49`, and Bear creates the note with the literal
// title `8+May+2026+at+00:49` (visible plus signs). Encoding via
// %20 round-trips cleanly.
func urlEncodeRFC3986(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
