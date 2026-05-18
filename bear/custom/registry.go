// Package custom hosts the closed Go-side registry of renderers that
// don't fit the 6 declarative blueprints. Adding a new entry requires a
// Go code change plus a test — same gate as adding a new blueprint —
// NOT a scripting hatch. This is the spirit of D-06: the schema
// stays narrow ([[domain]] blueprint = "custom" + renderer = "<name>"
// out of a closed enum), while behavior that genuinely doesn't fit the
// 6 blueprints lives in compiled Go alongside the rest of bear/.
//
// Each bear/custom/<name>.go installs itself via init Register; the
// dispatch path in bear/config/dispatch.go::buildCustom looks the
// renderer up by name and calls its Apply func to stamp non-default
// callbacks onto a base hub-routed *bear.Domain.
//
// Layering note (D-01): bear/custom imports bear, never
// bear/config. Registry consumers (dispatch.go) live in bear/config and
// call bear/custom.Lookup; Lookup never reaches back into config.
package custom

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"sync"

	"github.com/barad1tos/noxctl/bear"
)

// CustomDomain bundles a renderer Name (the closed-enum string used by
// [[domain]].renderer) with an Apply func that mutates the *bear.Domain
// the dispatch path constructs. The Apply approach was chosen over
// "return a fully-built Domain" so the base factory can wire shared
// invariants once (CanonicalTag derivation, default ParseMeta, etc.)
// and each custom renderer only describes the deltas.
//
// Naming note: revive's exported-stutter rule flags `custom.CustomDomain`,
// but renaming to `custom.Domain` would shadow the load-bearing
// `bear.Domain` type at every call site that imports both packages.
// The D-06 plan locks `CustomDomain`; precedent for narrow,
// reasoned nolint directives is `bear/state/state.go:37`.
//
//nolint:revive // CustomDomain disambiguates from bear.Domain at every call site (D-06).
type CustomDomain struct {
	// Name is the closed-enum string keyed in [[domain]].renderer.
	// Must be non-empty and unique across all registered renderers.
	Name string
	// Apply stamps non-default callbacks onto the base Domain. Called
	// by buildCustom AFTER the base hub-routed factory wires shared
	// invariants. Must be non-nil — a nil Apply would silently leave
	// the renderer-specific behavior unwired.
	Apply func(d *bear.Domain)
}

// ErrUnknownRenderer is the single sentinel for "no renderer registered
// under this name". Always wrapped via fmt.Errorf("%w:...",...) so
// callers test via errors.Is, never string-match.
var ErrUnknownRenderer = errors.New("custom: unknown renderer")

// mu guards registry. RWMutex over Mutex because Lookup is called once
// per dispatched stanza per noxctl run (read-heavy after init-time
// writes), and All/Names are diagnostic helpers that may run alongside
// concurrent Lookups in future code paths.
var (
	mu       sync.RWMutex
	registry = make(map[string]CustomDomain, 4)
)

// Register installs c by Name. Called from init in each bear/custom/
// <name>.go file; intentionally panics on misuse so the binary never
// starts in a half-wired state.
//
// Panics on:
// - empty Name (programming bug — every renderer has a name);
// - nil Apply (programming bug — Apply is the whole point);
// - duplicate Name (closed-catalog violation; the second init is
// either a copy-paste mistake or two unrelated features colliding
// on a name).
//
// Each panic message names the offending value so the caller can
// locate the source file from the stack trace + message alone.
func Register(c CustomDomain) {
	if c.Name == "" {
		panic("custom.Register: empty Name (every renderer must declare a name)")
	}
	if c.Apply == nil {
		panic(fmt.Sprintf("custom.Register: nil Apply for %q (Apply is required)", c.Name))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[c.Name]; dup {
		panic(fmt.Sprintf("custom.Register: duplicate renderer name %q", c.Name))
	}
	registry[c.Name] = c
}

// Lookup returns the CustomDomain registered under name. Returns a
// wrapped ErrUnknownRenderer when name is not registered; the wrap
// message lists every known renderer so dispatch errors copy-paste
// directly into a fixed [[domain]].renderer = "..." line.
func Lookup(name string) (CustomDomain, error) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := registry[name]
	if !ok {
		return CustomDomain{}, fmt.Errorf("%w: %q (valid: %v)",
			ErrUnknownRenderer, name, namesLocked())
	}
	return c, nil
}

// All returns a fresh map snapshot of every registered renderer.
// Mutating the returned map is safe and never leaks back into the
// shared registry (— defensive copy).
func All() map[string]CustomDomain {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]CustomDomain, len(registry))
	maps.Copy(out, registry)
	return out
}

// Names returns the registered renderer names in stable lexical order.
// Determinism matters for error messages and any future CLI listing.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	return namesLocked()
}

// namesLocked collects sorted registry keys. Caller MUST hold mu (read
// or write); the helper exists so Lookup can build its error message
// inside the same RLock without a re-acquire dance.
func namesLocked() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RegisterMaster is the sugar for the common "swap only RenderMaster"
// shape used by lyrics + quotes (and any future custom renderer whose
// only delta from hub-routed defaults is the master rendering). It
// collapses the boilerplate into one line per init so the per-file
// blocks stay below the dupl 30-token threshold.
func RegisterMaster(name string, renderMaster func(d *bear.Domain, groups map[string][]bear.Note) string) {
	Register(CustomDomain{
		Name:  name,
		Apply: func(d *bear.Domain) { d.RenderMaster = renderMaster },
	})
}
