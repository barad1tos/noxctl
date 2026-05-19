package bear_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

func TestPinRegistryLoadMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	registry, err := bear.LoadPinRegistry(path)
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if registry == nil {
		t.Fatal("expected non-nil registry")
	}
	if registry.Has("any-id") {
		t.Error("missing-file registry should be empty")
	}
}

func TestPinRegistrySaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	registry, err := bear.LoadPinRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	pinTime := time.Date(2026, 5, 7, 22, 30, 0, 0, time.UTC)
	registry.RecordPinAt("ABC", "quicknote/daily", pinTime)
	if saveErr := registry.Save(); saveErr != nil {
		t.Fatalf("save: %v", saveErr)
	}

	reloaded, err := bear.LoadPinRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Has("ABC") {
		t.Error("expected pin to round-trip")
	}
}

func TestPinRegistryLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	if err := os.WriteFile(path, []byte("not json {"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := bear.LoadPinRegistry(path)
	if err != nil {
		t.Fatalf("corrupt should not error: got %v", err)
	}
	if registry.Has("anything") {
		t.Error("corrupt-file registry should be empty")
	}
}

func TestPinRegistrySaveAtomic(t *testing.T) {
	// After Save, no.tmp file should remain in the directory.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	registry, err := bear.LoadPinRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	registry.RecordPinAt("X", "quicknote/daily", time.Now())
	if err = registry.Save(); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if _, err = os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("expected no .tmp file, got err=%v", err)
	}
}

func TestPinRegistrySaveSkipsWhenClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	registry, err := bear.LoadPinRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	// No RecordPin calls — registry is clean.
	if err = registry.Save(); err != nil {
		t.Fatal(err)
	}
	// File should not exist (no write happened).
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no file written for clean registry, got err=%v", err)
	}
}

func TestPinRegistryIsPinnedUnpinned(t *testing.T) {
	registry, _ := bear.LoadPinRegistry(filepath.Join(t.TempDir(), "p.json"))
	if registry.IsPinned("missing", time.Now()) {
		t.Error("missing atom should not be pinned")
	}
}

// TestPinRegistryIsPinnedCalendarExpiry covers the four expiring pin
// domains in one table. Compact `mkDate` rows keep each case below
// dupl's token threshold so structural similarity across domains
// doesn't trip the linter (table-driven tests are the idiomatic Go
// pattern for this exact shape).
func TestPinRegistryIsPinnedCalendarExpiry(t *testing.T) {
	cases := []struct {
		name       string
		domain     string
		pinAt      time.Time
		stillValid time.Time
		validDesc  string
		expired    time.Time
		expDesc    string
	}{
		// Each row pairs a still-valid sample inside the calendar period
		// with a one-second-past-boundary sample to prove the strict edge
		// (period_end + 1s → expired).
		{
			"daily", "quicknote/daily",
			mkDate(2026, 5, 7, 14), mkDate(2026, 5, 7, 23), "same day",
			mkDate(2026, 5, 8, 0).Add(time.Second), "next day",
		},
		// Wed 2026-05-06 14:00 → ISO week is Mon 05-04.. Sun 05-10.
		{
			"weekly", "quicknote/weekly",
			mkDate(2026, 5, 6, 14), mkDate(2026, 5, 9, 12), "same ISO week (Sat)",
			mkDate(2026, 5, 11, 0).Add(time.Second), "next Monday",
		},
		{
			"monthly", "quicknote/monthly",
			mkDate(2026, 5, 7, 14), mkDate(2026, 5, 30, 23), "same month",
			mkDate(2026, 6, 1, 0).Add(time.Second), "next month",
		},
		{
			"yearly", "quicknote/yearly",
			mkDate(2026, 5, 7, 14), mkDate(2026, 12, 31, 23), "Dec 31 of pin year",
			mkDate(2027, 1, 1, 0).Add(time.Second), "next year",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, _ := bear.LoadPinRegistry(filepath.Join(t.TempDir(), "p.json"))
			registry.RecordPinAt("X", tc.domain, tc.pinAt)

			if !registry.IsPinned("X", tc.stillValid) {
				t.Errorf("%s pin should be valid %s", tc.name, tc.validDesc)
			}
			if registry.IsPinned("X", tc.expired) {
				t.Errorf("%s pin should be expired %s", tc.name, tc.expDesc)
			}
		})
	}
}

func TestPinRegistryIsPinnedDecadalNeverExpires(t *testing.T) {
	registry, _ := bear.LoadPinRegistry(filepath.Join(t.TempDir(), "p.json"))
	pinAt := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local)
	registry.RecordPinAt("X", "quicknote/decadal", pinAt)

	farFuture := time.Date(2099, 12, 31, 0, 0, 0, 0, time.Local)
	if !registry.IsPinned("X", farFuture) {
		t.Error("decadal pin should never expire")
	}
}

func TestPinRegistryIsPinnedUnknownDomainTreatedAsExpired(t *testing.T) {
	registry, _ := bear.LoadPinRegistry(filepath.Join(t.TempDir(), "p.json"))
	pinAt := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local)
	registry.RecordPinAt("X", "quicknote/garbage", pinAt)

	if registry.IsPinned("X", pinAt.Add(time.Hour)) {
		t.Error("unknown pin domain should be treated as expired (defensive)")
	}
}

// TestPinAtomicWritePerm asserts that PinRegistry.Save (post-Plan-01-02
// back-port to bear.AtomicWriteJSON) writes pins.json with mode 0o600
// — closes the CONCERNS.md fsync gap and the Pitfall 5 perm trap in
// one verification.
func TestPinAtomicWritePerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	registry, err := bear.LoadPinRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	registry.RecordPinAt("X", "quicknote/daily", time.Now())
	if saveErr := registry.Save(); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("pins.json perm: got %v want %v", got, want)
	}
}
