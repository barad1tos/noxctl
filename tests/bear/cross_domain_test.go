package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/library"
	"github.com/barad1tos/noxctl/quicknote"
)

// TestRewriteCanonicalTag_EmitsBootstrapForm verifies the Task 3 of:
// rewriteCanonicalTag now emits the bootstrap-form new-note URL via
// NewNoteURLFromDomain(target).Emit — same SSOT path every other emit
// call site uses. Markers asserted: target.CanonicalTag at line head,
// target.IndexTitle in the backlink, and text= / edit=yes in the URL
// query string (bootstrap form's two distinguishing parameters).
func TestRewriteCanonicalTag_EmitsBootstrapForm(t *testing.T) {
	target := library.PoetryDomain
	content := "# X\n" + quicknote.DailyDomain.CanonicalTag + " | [[✱ Daily]]\n---\nbody\n"
	out, rewrote := bear.RewriteCanonicalTagForTest(content, quicknote.DailyDomain.CanonicalTag, target)
	if !rewrote {
		t.Fatal("rewrote=false despite source tag present")
	}
	if !strings.Contains(out, "text=") || !strings.Contains(out, "edit=yes") {
		t.Errorf("rewritten line missing bootstrap markers (text= / edit=yes):\n%s", out)
	}
	if !strings.Contains(out, target.CanonicalTag+" | [["+target.IndexTitle+"]]") {
		t.Errorf("rewritten line missing target canonical + IndexTitle backlink:\n%s", out)
	}
}

// TestRewriteCanonicalTag_ReturnsFalseWhenSourceTagAbsent confirms the no-op
// gate: when the source tag-line isn't present, the function returns the
// content untouched plus rewrote=false. Callers short-circuit on the bool.
func TestRewriteCanonicalTag_ReturnsFalseWhenSourceTagAbsent(t *testing.T) {
	content := "# X\nno tag here\nbody\n"
	out, rewrote := bear.RewriteCanonicalTagForTest(content, quicknote.DailyDomain.CanonicalTag, library.PoetryDomain)
	if rewrote {
		t.Error("rewrote=true despite no source tag-line in content")
	}
	if out != content {
		t.Error("content mutated when no rewrite should have happened")
	}
}

// TestRewriteCanonicalTag_ReturnsTrueWhenRewritten verifies the positive
// branch of the bool return: tag-line found → rewrote=true.
func TestRewriteCanonicalTag_ReturnsTrueWhenRewritten(t *testing.T) {
	content := "# X\n" + quicknote.DailyDomain.CanonicalTag + " | [[✱ Daily]]\n---\nbody\n"
	_, rewrote := bear.RewriteCanonicalTagForTest(content, quicknote.DailyDomain.CanonicalTag, library.PoetryDomain)
	if !rewrote {
		t.Error("rewrote=false despite source tag-line present")
	}
}
