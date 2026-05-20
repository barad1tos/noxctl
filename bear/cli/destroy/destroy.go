// Package destroy implements the `noxctl destroy <tag>` subcommand
// body. cmd/noxctl/destroy.go reduces to cobra wiring + flag
// parsing; this package owns the catalog lookup, the preview render,
// the type-to-confirm gate, and the bearcli mutation calls.
//
// Layering: this is a CLI helper, so it imports bear/ but never
// bear/config/. cmd/noxctl is the only place that owns the catalog →
// domain translation.
package destroy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/barad1tos/noxctl/bear"
)

// ErrAborted is returned when the operator declines the
// type-to-confirm prompt (or stdin is closed before a line arrives).
// cmd/noxctl maps it to exit code 1 with a "destroy aborted" stderr
// line.
var ErrAborted = errors.New("destroy: aborted by operator")

// ErrTagNotManaged is returned when the requested tag has no
// corresponding domain stanza in the loaded catalog. Mapped to
// exit code 1 with a helpful "did you mean ..." style message.
var ErrTagNotManaged = errors.New("destroy: tag not managed by this catalog")

// Options bundles every input Run needs. Plain struct, no methods,
// caller fills every field per "Accept interfaces, return structs".
type Options struct {
	Domains     []*bear.Domain // REQUIRED — loaded catalog
	Tag         string         // REQUIRED — target tag, e.g. "library/poetry"
	AutoApprove bool           // --auto-approve skips the type-to-confirm prompt
	Stdout      io.Writer      // preview + summary lands here (typically os.Stdout)
	Stderr      io.Writer      // diagnostic warnings (typically os.Stderr)
	Stdin       io.Reader      // type-to-confirm reads from here (typically os.Stdin)
}

// Run is the destroy orchestrator. Walks every note carrying the
// target tag, classifies each as master/hub (skipNote == true) or
// atom (skipNote == false), prints a preview, prompts for
// type-to-confirm unless AutoApprove, then trashes master+hub notes
// and strips canonical tag-lines from atomic ones.
//
// Atomic notes survive the destroy — only their canonical link to
// the managed structure is removed, so the body content the operator
// authored stays in Bear. Master + hub notes are auto-generated and
// get a soft-delete (bearcli trash); they can be restored from Bear's
// trash if the operator changes their mind.
func Run(ctx context.Context, opts Options) error {
	d := findDomain(opts.Domains, opts.Tag)
	if d == nil {
		return fmt.Errorf("%w: %q", ErrTagNotManaged, opts.Tag)
	}

	notes, err := bear.ListNotesForTag(ctx, opts.Tag)
	if err != nil {
		return fmt.Errorf("destroy: list notes for tag %q: %w", opts.Tag, err)
	}

	masters, atomics := classify(d, notes)
	if len(masters)+len(atomics) == 0 {
		// Nothing to destroy — skip the type-to-confirm prompt
		// entirely. A managed tag with zero notes is a legitimate
		// state (e.g. immediately after a previous destroy or
		// before the first apply); the operator-facing message
		// should reflect "no-op" rather than "are you sure?".
		_, _ = fmt.Fprintf(opts.Stdout,
			"noxctl destroy %s: tag is managed but carries no notes; nothing to do.\n",
			opts.Tag)
		return nil
	}
	renderPreview(opts.Stdout, opts.Tag, masters, atomics)

	if !opts.AutoApprove {
		if confirmErr := promptConfirm(opts.Stdout, opts.Stdin, opts.Tag); confirmErr != nil {
			return confirmErr
		}
	}

	trashed, stripped, failed := apply(ctx, d, masters, atomics, opts.Stderr)
	_, _ = fmt.Fprintf(opts.Stdout,
		"\ndestroy %s: trashed %d master/hub notes, stripped %d atomic canonical lines, %d failures.\n",
		opts.Tag, trashed, stripped, failed)
	if failed > 0 {
		return fmt.Errorf("destroy %s: %d note(s) failed; see stderr", opts.Tag, failed)
	}
	return nil
}

// findDomain returns the *Domain whose Tag matches target, or nil
// when the tag is not in the catalog. Linear scan; the catalog is
// always small (< 100 domains), so a map isn't worth the wiring.
func findDomain(domains []*bear.Domain, target string) *bear.Domain {
	for _, d := range domains {
		if d.Tag == target {
			return d
		}
	}
	return nil
}

// classify splits the note list into auto-generated master/hub notes
// (which will be trashed) and atomic notes (which will have their
// canonical tag-line stripped). Domain.skipNote is the authoritative
// classifier — same predicate the regen pipeline uses to decide which
// notes are "system" vs "content".
func classify(d *bear.Domain, notes []bear.Note) (masters, atomics []bear.Note) {
	for _, n := range notes {
		if bear.IsAuxNote(d, n) {
			masters = append(masters, n)
		} else {
			atomics = append(atomics, n)
		}
	}
	return masters, atomics
}

// renderPreview prints the plan-style summary an operator sees
// before being asked to type-to-confirm. Spells out exact counts and
// the soft-delete vs strip distinction so there are no surprises
// post-confirmation.
func renderPreview(w io.Writer, tag string, masters, atomics []bear.Note) {
	_, _ = fmt.Fprintf(w, "noxctl destroy %s — preview\n\n", tag)
	_, _ = fmt.Fprintf(w, "  %d master/hub notes will be moved to Bear's trash (restorable).\n", len(masters))
	for _, m := range masters {
		_, _ = fmt.Fprintf(w, "    - %s (%s)\n", m.Title, m.ID)
	}
	_, _ = fmt.Fprintf(
		w,
		"\n  %d atomic notes will have their canonical tag-line stripped (bodies preserved).\n",
		len(atomics),
	)
	if len(atomics) > 0 {
		limit := min(len(atomics), 5)
		for _, a := range atomics[:limit] {
			_, _ = fmt.Fprintf(w, "    - %s (%s)\n", a.Title, a.ID)
		}
		if len(atomics) > limit {
			_, _ = fmt.Fprintf(w, "    ... and %d more.\n", len(atomics)-limit)
		}
	}
}

// promptConfirm reads one line from stdin and accepts only an exact
// match against the tag. EOF or a wrong-tag line returns ErrAborted;
// a genuine I/O failure on stdin (closed fd, EBADF) returns a
// distinct wrapped error so the operator sees the real cause instead
// of a misleading "aborted" message.
//
// Type-to-confirm is the human-side guard against fat-fingered
// destroy invocations; the tag name is the natural shibboleth —
// long enough to defeat muscle-memory yes, short enough to be
// typeable.
func promptConfirm(out io.Writer, in io.Reader, tag string) error {
	_, _ = fmt.Fprintf(out, "\nType %q to confirm (anything else aborts): ", tag)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("destroy: read confirmation: %w", err)
		}
		// Scan returned false with no error → EOF / closed stream;
		// treat as a deliberate abort (Ctrl-D, empty pipe).
		return ErrAborted
	}
	if strings.TrimSpace(scanner.Text()) != tag {
		return ErrAborted
	}
	return nil
}

// apply performs the destroy mutations: trash master/hub notes via
// bearcli, strip canonical tag-lines from atomic note bodies.
// Per-note failures log to stderr and increment the failed counter
// but do not abort the sweep — partial-success state is recoverable
// and the operator should see every failure, not just the first.
func apply(
	ctx context.Context,
	d *bear.Domain,
	masters, atomics []bear.Note,
	stderr io.Writer,
) (trashed, stripped, failed int) {
	for _, m := range masters {
		if err := ctx.Err(); err != nil {
			_, _ = fmt.Fprintf(stderr, "destroy: canceled mid-sweep (%v)\n", err)
			return trashed, stripped, failed
		}
		if err := bear.TrashNote(ctx, m.ID); err != nil {
			_, _ = fmt.Fprintf(stderr, "destroy: trash %q (%s): %v\n", m.Title, m.ID, err)
			failed++
			continue
		}
		trashed++
	}
	for _, a := range atomics {
		if err := ctx.Err(); err != nil {
			_, _ = fmt.Fprintf(stderr, "destroy: canceled mid-sweep (%v)\n", err)
			return trashed, stripped, failed
		}
		newContent, changed := StripCanonical(a.Content, d.CanonicalTag)
		if !changed {
			continue
		}
		if err := bear.OverwriteNoteContent(ctx, a.ID, newContent); err != nil {
			_, _ = fmt.Fprintf(stderr, "destroy: strip %q (%s): %v\n", a.Title, a.ID, err)
			failed++
			continue
		}
		stripped++
	}
	return trashed, stripped, failed
}

// StripCanonical removes every line whose first token is the
// canonical tag (`#<top>` or `#<top>/<sub>`). Returns the new body
// and a "changed" boolean so callers can short-circuit when there's
// nothing to write. Exported for unit tests in
// tests/bear/cli/destroy/.
//
// Matches the canonical tag followed by either end-of-token, a slash
// (sub-tag form), or a separator (whitespace / `|`). Avoids false
// positives like `#libraryother` when the canonical tag is
// `#library`.
func StripCanonical(content, canonicalTag string) (string, bool) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		if startsWithCanonical(line, canonicalTag) {
			changed = true
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return content, false
	}
	return strings.Join(out, "\n"), true
}

// PromptConfirmForTest exposes the type-to-confirm gate to tests in
// tests/bear/cli/destroy/. Production callers reach the same logic
// through Run; the seam exists because the project test-location
// convention forbids in-package _test.go files.
func PromptConfirmForTest(out io.Writer, in io.Reader, tag string) error {
	return promptConfirm(out, in, tag)
}

// RenderPreviewForTest exposes the preview renderer to tests so the
// "N atomics / ... and X more" overflow shape can be locked in
// without going through a full Run + bearcli round trip.
func RenderPreviewForTest(w io.Writer, tag string, masters, atomics []bear.Note) {
	renderPreview(w, tag, masters, atomics)
}

// startsWithCanonical reports whether `line` begins with the
// canonical tag followed by an acceptable separator. The separator
// guards against `#library` matching `#libraryother`.
func startsWithCanonical(line, canonicalTag string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, canonicalTag) {
		return false
	}
	rest := trimmed[len(canonicalTag):]
	if rest == "" {
		return true
	}
	switch rest[0] {
	case '/', ' ', '\t', '|':
		return true
	}
	return false
}
