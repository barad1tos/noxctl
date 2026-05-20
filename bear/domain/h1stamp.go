package domain

import (
	"strings"
	"time"
)

// NowForNewNoteLink is the time source for StampDatetimeH1's daemon-side
// H1 stamp. Tests override this via SetNowForNewNoteLinkForTest to get
// deterministic output. Production code always reads time.Now.
var NowForNewNoteLink = time.Now

// DefaultQuickPlaceholderH1 is the single source of truth for the H1
// marker string that x-callback new-note bootstrap URLs embed when a
// domain doesn't override it. The daemon's fast-pass
// (ApplyPlaceholderRefresh) scans notes whose Title equals this
// constant and rewrites their H1 line to a fresh timestamp on the
// next tick after a click.
//
// Per-domain override via Domain.QuickPlaceholderH1 wins when set.
// Keeping the default as a single exported constant means every
// consumer reads from one place: the bootstrap URL builder, the
// placeholder-refresh scan, and tests.
const DefaultQuickPlaceholderH1 = "Quicknote"

// H1DatetimeFormat is the time.Format layout for the daemon-stamped H1
// line on atoms that arrive without one. Spec-frozen as
// "2 January 2006 at 15:04" — extracted to a constant so the canonical
// shape lives in one place across upsertAtomicBacklink,
// RenderCanonicalForBootstrap, RenderAtomicCanonicalForTest, and the
// H1-stamp emitter below.
const H1DatetimeFormat = "2 January 2006 at 15:04"

// SetNowForNewNoteLinkForTest swaps the time source used by StampDatetimeH1
// for deterministic test output. The original value is restored at test
// cleanup. Tests-only — production code never calls this.
func SetNowForNewNoteLinkForTest(t interface{ Cleanup(func()) }, fn func() time.Time) {
	prev := NowForNewNoteLink
	NowForNewNoteLink = fn
	t.Cleanup(func() { NowForNewNoteLink = prev })
}

// StampDatetimeH1 prepends a `# <NOW>` H1 line to body when body has no
// recognized H1. Recognition rules (spec component 2):
// - First non-blank line matches `# <non-blank-content>` (literal `#`,
// exactly one ASCII space, then ≥1 non-whitespace char) → recognized.
// - Anything else → not an H1; prepend `# <NOW>` above the existing
// content untouched.
//
// Idempotent: a second call on the stamped output recognizes the new H1
// and returns the body unchanged. Time source is the package-level
// NowForNewNoteLink so tests can swap via SetNowForNewNoteLinkForTest.
//
// Format: "2 January 2026 at 15:04" (e.g. "13 May 2026 at 15:25"). Same
// shape the legacy newNoteLink emitted in its title= URL parameter so
// existing user expectations of "datetime at the moment of creation"
// stay intact.
func StampDatetimeH1(body string) string {
	if hasRecognizedH1(body) {
		return body
	}
	stamp := NowForNewNoteLink().Format(H1DatetimeFormat)
	return "# " + stamp + "\n" + body
}

// hasRecognizedH1 reports whether body's first non-blank line is a real H1
// per the spec's recognition rules. `# <content>` where content is non-
// whitespace counts; `# ` (empty), `#tag`, `## Heading`, prose, etc. do
// not.
func hasRecognizedH1(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return isRecognizedH1Line(line)
	}
	return false
}

// isRecognizedH1Line implements the H1-recognition predicate on a single
// line. The line must start with `# ` (hash + exactly one ASCII space) and
// carry ≥1 non-whitespace character after it. The single `# ` prefix guard
// already rejects `##`, `###`, `#tag`, and any other `#`-prefixed line whose
// second character is not a space — none of them satisfy `HasPrefix(line, "# ")`.
func isRecognizedH1Line(line string) bool {
	if !strings.HasPrefix(line, "# ") {
		return false
	}
	return strings.TrimSpace(line[2:]) != ""
}
