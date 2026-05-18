package custom_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/custom"
)

// fixturePrefix isolates this file's Register calls from the production
// registrations that bear/custom/{lyrics,quotes,agents}.go install at
// init time. Production names ("lyrics", "quotes", "agents") never
// collide with names starting with "fixture_", so duplicate-panic tests
// stay deterministic across test ordering.
const fixturePrefix = "fixture_"

// noopApply is the simplest valid Apply func — used wherever a test
// only cares about the registry plumbing, not the callback effect.
func noopApply(_ *bear.Domain) {}

// TestRegisterAndLookupRoundTrip exercises the happy path: Register a
// uniquely-named CustomDomain, then Lookup by the same name returns
// the same CustomDomain instance (Name + Apply pointer-equal).
func TestRegisterAndLookupRoundTrip(t *testing.T) {
	name := fixturePrefix + "round_trip"
	apply := func(d *bear.Domain) { d.IndexTitle = "round-trip-stamped" }
	custom.Register(custom.CustomDomain{Name: name, Apply: apply})

	got, err := custom.Lookup(name)
	if err != nil {
		t.Fatalf("Lookup(%q): unexpected error %v", name, err)
	}
	if got.Name != name {
		t.Errorf("Lookup.Name = %q, want %q", got.Name, name)
	}
	d := &bear.Domain{}
	got.Apply(d)
	if d.IndexTitle != "round-trip-stamped" {
		t.Errorf("Apply did not run; IndexTitle = %q, want round-trip-stamped", d.IndexTitle)
	}
}

// TestLookupUnknownReturnsErrUnknownRenderer locks: the
// sentinel is reachable via errors.Is, not by string-matching on the
// error message. The message itself MUST surface the requested name
// so operators can spot typos in [[domain]].renderer.
func TestLookupUnknownReturnsErrUnknownRenderer(t *testing.T) {
	const missing = fixturePrefix + "definitely_not_registered"
	_, err := custom.Lookup(missing)
	if err == nil {
		t.Fatal("Lookup(unknown): want error, got nil")
	}
	if !errors.Is(err, custom.ErrUnknownRenderer) {
		t.Errorf("err = %v, want errors.Is(err, ErrUnknownRenderer)", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err message %q must mention requested name %q", err.Error(), missing)
	}
}

// TestRegisterDuplicatePanics enforces the closed-catalog contract:
// each name appears exactly once. A duplicate Register is a bug, not
// a silent override — the panic must mention the offending name so
// the operator can locate the second init call.
func TestRegisterDuplicatePanics(t *testing.T) {
	name := fixturePrefix + "dup"
	custom.Register(custom.CustomDomain{Name: name, Apply: noopApply})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register(duplicate): expected panic, got none")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "duplicate") {
			t.Errorf("panic message %q must contain \"duplicate\"", msg)
		}
		if !strings.Contains(msg, name) {
			t.Errorf("panic message %q must contain offending name %q", msg, name)
		}
	}()
	custom.Register(custom.CustomDomain{Name: name, Apply: noopApply})
}

// TestRegisterEmptyNamePanics — empty Name is a programming error;
// surfacing it as a panic at init time fails fast.
func TestRegisterEmptyNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register(empty name): expected panic, got none")
		}
	}()
	custom.Register(custom.CustomDomain{Name: "", Apply: noopApply})
}

// TestRegisterNilApplyPanics — nil Apply would silently degrade
// dispatch into a no-op rendering (every atom rendered by the base
// hub-routed default). Panic loudly at init instead.
func TestRegisterNilApplyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register(nil Apply): expected panic, got none")
		}
	}()
	custom.Register(custom.CustomDomain{Name: fixturePrefix + "nil_apply", Apply: nil})
}

// TestApplyMutatesDomain — the registered Apply func actually mutates
// the *bear.Domain it receives. Anchors the dispatch contract that
// buildCustom relies on (Apply stamps callbacks onto a base Domain).
func TestApplyMutatesDomain(t *testing.T) {
	name := fixturePrefix + "mutates"
	called := false
	apply := func(d *bear.Domain) {
		called = true
		d.UnknownBucket = "mutated"
	}
	custom.Register(custom.CustomDomain{Name: name, Apply: apply})

	c, err := custom.Lookup(name)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", name, err)
	}
	d := &bear.Domain{}
	c.Apply(d)
	if !called {
		t.Error("Apply was not invoked")
	}
	if d.UnknownBucket != "mutated" {
		t.Errorf("UnknownBucket = %q, want mutated", d.UnknownBucket)
	}
}

// TestNamesAreSorted — Names returns the registered renderer names
// in stable lexical order, so Lookup error messages and CLI listings
// stay deterministic for diffability.
func TestNamesAreSorted(t *testing.T) {
	names := custom.Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names() not sorted: %v", names)
		}
	}
}

// TestAllReturnsCopy — All must return a fresh map; mutating it
// MUST NOT leak back into the registry. Otherwise dispatch's caller
// could accidentally drop a renderer mid-run.
func TestAllReturnsCopy(t *testing.T) {
	name := fixturePrefix + "copy"
	custom.Register(custom.CustomDomain{Name: name, Apply: noopApply})

	snapshot := custom.All()
	if _, ok := snapshot[name]; !ok {
		t.Fatalf("All() missing %q", name)
	}
	delete(snapshot, name)

	if _, err := custom.Lookup(name); err != nil {
		t.Errorf("mutating All() leaked back into registry: Lookup(%q) failed: %v", name, err)
	}
}

// TestProductionRenderersRegistered locks the full D-06
// contract: lyrics, quotes, agents are reachable via Lookup.
// (plan 04-02) installed all three production registrations
// at package init; removing any init in bear/custom/{lyrics,quotes,
// agents}.go must fail this test reproducibly (WR-15 — the prior
// in-progress skip branch silently PASSed and gave false confidence).
func TestProductionRenderersRegistered(t *testing.T) {
	for _, name := range []string{"lyrics", "quotes", "agents"} {
		t.Run(name, func(t *testing.T) {
			c, err := custom.Lookup(name)
			if err != nil {
				t.Fatalf("renderer %q must be registered: %v", name, err)
			}
			if c.Apply == nil {
				t.Errorf("registered %q has nil Apply", name)
			}
		})
	}
}
