package bear

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// PinEntry is one row in the pin registry: which domain the user
// most-recently moved this atom into, and when.
type PinEntry struct {
	Domain   string    `json:"domain"`
	PinnedAt time.Time `json:"pinnedAt"`
}

// PinRegistry tracks user-driven cross-domain moves so time-based
// promotion can defer to the user's recent intent. Persisted as JSON
// at `path`, written atomically via bear.AtomicWriteJSON (tmp +
// rename + fsync(file) + fsync(parent dir)).
type PinRegistry struct {
	path  string
	mu    sync.Mutex
	pins  map[string]PinEntry
	dirty bool
}

// LoadPinRegistry reads the registry from `path`. Missing or corrupt
// files yield an empty in-memory registry — load is best-effort because
// pins are not authoritative state, just a hint for time-promotion.
// Save will (re)create the file when the in-memory state changes.
func LoadPinRegistry(path string) (*PinRegistry, error) {
	registry := &PinRegistry{path: path, pins: make(map[string]PinEntry)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return registry, nil
	}
	if err != nil {
		return registry, fmt.Errorf("LoadPinRegistry(%s): %w", path, err)
	}
	if err = json.Unmarshal(data, &registry.pins); err != nil {
		log.Printf("pins: corrupt registry at %s, starting fresh: %v", path, err)
		registry.pins = make(map[string]PinEntry)
	}
	return registry, nil
}

// Has reports whether the registry contains an entry for atomID. Used
// in tests to verify round-trip; production callers use IsPinned.
func (r *PinRegistry) Has(atomID string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pins[atomID]
	return ok
}

// RecordPinAt records (or overwrites) a pin entry for atomID at the
// supplied moment. Production callers use RecordPin which forwards
// `time.Now`. Test callers pass a fixed time for deterministic asserts.
func (r *PinRegistry) RecordPinAt(atomID, domain string, pinnedAt time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, hadEntry := r.pins[atomID]
	if hadEntry && existing.Domain == domain && existing.PinnedAt.Equal(pinnedAt) {
		return // no-op, registry already records exactly this
	}
	r.pins[atomID] = PinEntry{Domain: domain, PinnedAt: pinnedAt}
	r.dirty = true
}

// Save persists the registry to disk if anything changed since the last
// load or save. Delegates to bear.AtomicWriteJSON (tmp + rename +
// fsync(file) + fsync(parent dir)) so the call is crash-safe on APFS.
// No-op when in-memory state is clean.
func (r *PinRegistry) Save() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.dirty {
		return nil
	}
	if err := AtomicWriteJSON(r.path, r.pins, 0o600); err != nil {
		return fmt.Errorf("PinRegistry.Save: %w", err)
	}
	r.dirty = false
	return nil
}

// IsPinned reports whether atomID has a non-expired pin given the
// current wall-clock `now`. Expiry is per-domain and uses the calendar
// boundary helpers from calendar.go — pinning into daily covers the
// rest of today; into weekly the rest of the ISO week; etc. Decadal
// pins never expire (decadal is the terminal bucket).
//
// Unknown pin domains (corrupt or hand-edited registry) treat the pin
// as expired — defensive default that lets time-promotion regain
// authority over a confused entry.
func (r *PinRegistry) IsPinned(atomID string, now time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	entry, ok := r.pins[atomID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	switch entry.Domain {
	case "quicknote/daily":
		return !CalendarStartOfDay(now).After(entry.PinnedAt)
	case "quicknote/weekly":
		return !CalendarStartOfWeek(now).After(entry.PinnedAt)
	case "quicknote/monthly":
		return !CalendarStartOfMonth(now).After(entry.PinnedAt)
	case "quicknote/yearly":
		return !CalendarStartOfYear(now).After(entry.PinnedAt)
	case "quicknote/decadal":
		return true
	default:
		return false
	}
}

// RecordPin is the production entry point for pin recording — it
// forwards time.Now to RecordPinAt. Tests should use RecordPinAt
// directly with a fixed time so assertions are deterministic.
func (r *PinRegistry) RecordPin(atomID, domain string) {
	r.RecordPinAt(atomID, domain, time.Now())
}
