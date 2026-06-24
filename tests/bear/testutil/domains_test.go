package testutil_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// TestLoadCatalog_LoadsRomanReference — the canonical catalog must
// resolve from any cwd via runtime.Caller anchoring + parse cleanly +
// surface exactly 31 domains: 27 leaves + 4 umbrellas (it / library /
// llm / quicknote). Exact counts catch both shrink regressions
// (umbrella removal) and growth that lands without a corresponding
// downstream test update. Umbrella detection keys off SkipAtomicsPass,
// the field NewUmbrellaDomain sets at factory time.
func TestLoadCatalog_LoadsRomanReference(t *testing.T) {
	doms := testutil.LoadCatalog(t)
	const wantTotal = 31
	if len(doms) != wantTotal {
		t.Errorf("catalog has %d domains, want exactly %d (27 leaves + 4 umbrellas)",
			len(doms), wantTotal)
	}
	umbrellas := 0
	for _, d := range doms {
		if d.SkipAtomicsPass {
			umbrellas++
		}
	}
	if umbrellas != 4 {
		t.Errorf("umbrella count = %d, want 4 (it / library / llm / quicknote)", umbrellas)
	}
	if leaves := len(doms) - umbrellas; leaves != 27 {
		t.Errorf("leaf count = %d, want 27", leaves)
	}
}

// TestDomain_FindsKnownTag — sanity check that the per-tag accessor
// returns a non-nil domain pointer for a stable, central tag.
func TestDomain_FindsKnownTag(t *testing.T) {
	d := testutil.Domain(t, "library/poetry")
	if d.Tag != "library/poetry" {
		t.Errorf("Domain.Tag = %q, want library/poetry", d.Tag)
	}
}

// TestDomains_PreservesOrder — multi-tag accessor must surface
// domains in the request order so tests that depend on slice
// position (e.g. priority lists) stay deterministic.
func TestDomains_PreservesOrder(t *testing.T) {
	want := []string{"llm/agents", "library/poetry", "quicknote/daily"}
	got := testutil.Domains(t, want...)
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, d := range got {
		if d.Tag != want[i] {
			t.Errorf("got[%d].Tag = %q, want %q", i, d.Tag, want[i])
		}
	}
}

// TestDomains_ResolvesUmbrellas — the four umbrella parents
// (it / library / llm / quicknote) must round-trip cleanly so umbrella-
// shaped tests can rely on the accessor without a fallback.
func TestDomains_ResolvesUmbrellas(t *testing.T) {
	for _, tag := range []string{"it", "library", "llm", "quicknote"} {
		d := testutil.Domain(t, tag)
		if d.Tag != tag {
			t.Errorf("umbrella %q: got tag %q", tag, d.Tag)
		}
	}
}

// fatalCapturingTB embeds testing.TB and intercepts Fatalf so an
// error path that ends in tb.Fatalf can be asserted on without
// killing the surrounding test. Goexit mirrors real testing.T
// behavior so the call site terminates exactly as production would.
type fatalCapturingTB struct {
	testing.TB
	fatalCalled bool
	fatalMsg    string
}

func (m *fatalCapturingTB) Helper() {}

func (m *fatalCapturingTB) Fatalf(format string, args ...any) {
	m.fatalCalled = true
	m.fatalMsg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// captureFatal runs fn against a fatalCapturingTB inside a fresh
// goroutine so the real test goroutine isn't affected by Goexit.
// Returns the mock with .fatalCalled / .fatalMsg populated.
func captureFatal(t *testing.T, fn func(tb testing.TB)) *fatalCapturingTB {
	t.Helper()
	m := &fatalCapturingTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(m)
	}()
	<-done
	return m
}

// TestDomain_FatalsOnMissingTag — every accessor MUST fail loud (not
// silently return nil) when a tag is absent from the catalog.
func TestDomain_FatalsOnMissingTag(t *testing.T) {
	m := captureFatal(t, func(tb testing.TB) {
		testutil.Domain(tb, "no-such-tag-in-catalog")
	})
	if !m.fatalCalled {
		t.Fatal("Domain(<nonexistent>) should have called Fatalf")
	}
	if !strings.Contains(m.fatalMsg, "no-such-tag-in-catalog") {
		t.Errorf("Fatalf message %q should name the missing tag", m.fatalMsg)
	}
}

// TestDomains_FatalsOnAnyMissingTag — same loud-fail contract for the
// multi-tag accessor: any one missing tag fails the whole call.
func TestDomains_FatalsOnAnyMissingTag(t *testing.T) {
	m := captureFatal(t, func(tb testing.TB) {
		testutil.Domains(tb, "library/poetry", "no-such-tag-in-catalog")
	})
	if !m.fatalCalled {
		t.Fatal("Domains with a missing tag should have called Fatalf")
	}
	if !strings.Contains(m.fatalMsg, "no-such-tag-in-catalog") {
		t.Errorf("Fatalf message %q should name the missing tag", m.fatalMsg)
	}
}
