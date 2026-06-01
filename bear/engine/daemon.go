// Package engine daemon — long-running watcher loop driving cycleOnce on
// FSEvent bursts.
//
// On macOS this uses fsnotify's kqueue backend; legacy docs may say
// "FSEvents" but fsnotify v1.x has always been kqueue-on-Darwin.
// Behavior is identical from the daemon's vantage point — events
// arrive on `watcher.Events`, debounced + max-burst-windowed before
// triggering a cycle.
//
// Self-write gate: FSEvents during a regen cycle and within
// `SelfWriteEpsilon` afterwards are assumed to come from our own
// bearcli writes. Without this gate, regen writes hub → Bear writes
// DB → daemon thinks user changed something → new regen → infinite
// loop. The gate is per-[Daemon] instance.
//
// Bear-write polling fallback: in addition to the FSEvent path,
// [Daemon.Run] runs an optional polling loop that stats database.sqlite
// every `MtimePollInterval` and routes detected mtime changes through
// the same handleEvent path FSEvents use. The poll path is a FALLBACK,
// not a replacement: it closes the gap when Bear defers SQLite writes
// (e.g., drag-to-tag UI actions buffer in memory and flush
// opportunistically — observed delay ~2.5min on a real vault). Set
// `MtimePollInterval=0` to disable the loop entirely; the daemon then
// relies exclusively on FSEvents.
//
// Auto-tag fast-pass: a THIRD trigger source alongside FSEvent +
// mtime-poll. [Daemon.Run] runs an optional fast-pass loop that ticks
// every `AutoTagPollInterval` (default 2s) and invokes the FOUR
// canonicalization fast-passes — in order: foreign-tag escape,
// daily-default, domain-bootstrap, then placeholder-refresh
// ([Daemon.handleAutoTagTick]) — NOT the full per-domain regen cycle.
// The goal: click-then-type quicknotes get `#quicknote/daily` stamped
// within <= 5s p95 instead of the empirically-measured 13-15s the
// FSEvent-then-Bear-flush path delivers. Set `AutoTagPollInterval=0` to
// disable the fast-pass; the daemon then relies on FSEvent + mtime-poll only.
//
// Note the asymmetry with the full apply path: engine.Apply runs the
// SAME four passes plus ADDITIONAL cross-domain-moves and time-promotion
// pre-passes (bear/engine/prepasses.go) that the daemon tick does NOT —
// the tick is the latency-critical subset, not the full pre-pass suite. Each
// fast-pass issues its own full `bearcli list --location notes`, so the
// per-tick cost scales with the number of enabled passes.
//
// Self-write gate: fast-pass ticks set regenInProgress=true for the
// duration of the pre-pass calls (via markRegenStart/markRegenEnd
// — same helpers cycleOnce uses). The widened
// effectiveSelfWriteEpsilon absorbs the fast-pass's own writes
// uniformly with cycle writes; no new gate state is introduced.
package engine

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	// DefaultDebouncePause is the quiet window after the last FSEvent
	// before Daemon.Run runs a regen cycle (click-to-result ~2-4s with
	// bearcli regen).
	DefaultDebouncePause = 2 * time.Second

	// DaemonStartupLogMarker is the literal line prefix the daemon
	// emits on every fresh boot. The verify-gate scanner
	// (bear/cli/verify/checks.go) rewinds to the most recent
	// occurrence and scans forward from there. Defined here so the
	// emit site (cmd/noxctl/daemon.go) and the check site share one
	// source of truth — rename the daemon binary and only this
	// constant moves with it.
	DaemonStartupLogMarker = "regen-watchd starting"

	// LaunchdServiceLabel is the launchd job label the daemon runs
	// under — the single Go source of truth for the service name.
	// doctor's daemon.service check references it via
	// `launchctl print gui/$uid/<label>` (read-only inspection only;
	// never bootstrap/kickstart), so the label literal lives here once
	// instead of being re-typed at the check site.
	LaunchdServiceLabel = "com.bear.regen-watchd"

	// DefaultMaxBurstWindow caps the wait when the user keeps typing —
	// 1 minute brackets a typical note-editing burst without letting
	// the daemon stall indefinitely.
	DefaultMaxBurstWindow = 1 * time.Minute

	// DefaultSelfWriteEpsilon is the self-write gate window: FSEvents
	// landing within this window of a markRegenStart are filtered as
	// daemon-originated noise rather than user edits.
	DefaultSelfWriteEpsilon = 2 * time.Second

	// DefaultMtimePollInterval is the period between database.sqlite
	// mtime checks (ship default). Zero is the "disabled" sentinel
	// resolved at bear/config.daemonDefaults — see
	// DaemonOpts.MtimePollInterval for the full semantics.
	DefaultMtimePollInterval = 30 * time.Second

	// DefaultAutoTagPollInterval is the period between fast-pass ticks
	// that run ForeignTagEscape + DailyDefaultTag (ship default). Zero
	// is the "disabled" sentinel resolved at bear/config.daemonDefaults
	// — see DaemonOpts.AutoTagPollInterval for the full semantics.
	DefaultAutoTagPollInterval = 2 * time.Second

	// dbFilename is the SQLite main-DB file Bear writes to. Used by the
	// poll path and the FSEvent filter (isWatchedDBEvent). Bear runs in
	// journal_mode=delete, so neither
	// `database.sqlite-wal` nor `database.sqlite-shm` ever exists in
	// practice — they remain in the FSEvent filter only as defensive
	// coverage in case Bear changes journal modes in a future release.
	dbFilename = "database.sqlite"
)

// DaemonOpts is the long-running counterpart to ApplyOpts. Each cycle
// internally calls engine.Apply with the embedded ApplyOpts; BearDBDir
// is the watch target (typically Bear's Group Container Application
// Data dir).
type DaemonOpts struct {
	ApplyOpts // embedded — cycle invokes Apply with these

	// BearDBDir is the directory passed to fsnotify.Watcher.Add. REQUIRED.
	// Typically "$HOME/Library/Group Containers/9K33E3U3T4.net.shinyfrog.bear/Application Data".
	BearDBDir string

	// DebouncePause defaults to DefaultDebouncePause when zero.
	DebouncePause time.Duration

	// MaxBurstWindow defaults to DefaultMaxBurstWindow when zero.
	MaxBurstWindow time.Duration

	// SelfWriteEpsilon defaults to DefaultSelfWriteEpsilon when zero.
	SelfWriteEpsilon time.Duration

	// MtimePollInterval is the period between database.sqlite mtime
	// checks. When > 0, Daemon.Run creates a time.Ticker that drives a
	// 6th select case. When 0, polling is disabled entirely — no
	// ticker, no goroutines, no work (Go's nil-channel idiom:
	// `case <-pollTick:` blocks forever). The 30s default is applied at
	// the config layer (bear/config.daemonDefaults), NOT here, because
	// zero must remain meaningful as a "disabled" sentinel for
	// operators who explicitly opt out.
	MtimePollInterval time.Duration

	// StatFn is the os.Stat-like seam used by the poll loop. Production
	// leaves it nil; applyDaemonDefaults wires os.Stat. Tests inject a
	// scripted fake that returns canned os.FileInfo per call. Narrow
	// surface: one function field, no new interface. Mirrors the test-
	// seam pattern at bear/new_note.go::nowForNewNoteLink.
	StatFn func(path string) (os.FileInfo, error)

	// DatabaseChangeTokenFn returns a content-level token for database.sqlite
	// after StatFn observes an mtime advance. The mtime remains the cheap
	// wake-up signal, but this token decides whether the daemon should treat
	// the wake-up as meaningful Bear content/tag change or SQLite housekeeping.
	// Production CLI wires SQLiteNoteChangeToken; tests can inject a scripted
	// token source. Nil defaults to a file-metadata token for library callers.
	DatabaseChangeTokenFn func(path string, info os.FileInfo) (string, error)

	// AutoTagPollInterval is the period between fast-pass ticks that run
	// the four canonicalization fast-passes (foreign-tag escape, daily-default,
	// domain-bootstrap, placeholder-refresh) — independent of the full
	// per-domain regen cycle. When > 0,
	// Daemon.Run creates a second time.Ticker driving a 7th select
	// case. When 0, the fast-pass is disabled (nil-channel idiom,
	// mirrors MtimePollInterval).
	//
	// Default 2s is applied at the config layer
	// (bear/config.daemonDefaults) — same precedence chain as
	// MtimePollInterval. CLI callers thread cfg.AutoTagPollInterval
	// verbatim; tests / library callers that construct DaemonOpts manually
	// get a disabled fast-pass on the zero value.
	//
	// Test seam: the fast-pass tick body calls the four canonicalization
	// passes, which all route through bearcli.Run + BackendFromContext(ctx).
	// DaemonOpts
	// gains NO parallel field for fake injection — tests stamp the seam
	// on ctx via bearcli.ContextWithBackend.
	AutoTagPollInterval time.Duration
}

// FsWatcher narrows *fsnotify.Watcher to what Daemon actually consumes.
// EXPORTED so test packages outside `engine` can construct fake
// implementations directly without an in-package alias. Production
// wires fsnotifyAdapter; tests inject a fake watcher with controllable
// channels. Mirrors the test-seam pattern at bear/new_note.go's
// `nowForNewNoteLink`.
type FsWatcher interface {
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Add(path string) error
	Close() error
}

// fsnotifyAdapter wraps a real *fsnotify.Watcher to satisfy FsWatcher.
type fsnotifyAdapter struct{ w *fsnotify.Watcher }

func (a *fsnotifyAdapter) Events() <-chan fsnotify.Event { return a.w.Events }
func (a *fsnotifyAdapter) Errors() <-chan error          { return a.w.Errors }
func (a *fsnotifyAdapter) Add(p string) error            { return a.w.Add(p) }
func (a *fsnotifyAdapter) Close() error                  { return a.w.Close() }

// Daemon owns the FSEvents loop, debounce/burst timers, and self-write
// gate for a long-running noxctl daemon process. Created via
// NewDaemon; lifecycle driven by Run(ctx) which returns when ctx is
// canceled or the watcher closes.
type Daemon struct {
	opts            DaemonOpts
	watcher         FsWatcher
	regenMu         sync.Mutex
	regenInProgress bool
	regenEndTime    time.Time
}

// NewDaemon constructs a Daemon backed by a real *fsnotify.Watcher.
// Defaults DebouncePause/MaxBurstWindow/SelfWriteEpsilon if zero.
func NewDaemon(opts DaemonOpts) (*Daemon, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("NewDaemon: %w", err)
	}
	return NewDaemonWithWatcher(opts, &fsnotifyAdapter{w: w}), nil
}

// NewDaemonWithWatcher constructs a Daemon backed by a caller-supplied
// FsWatcher. Production wires fsnotifyAdapter via NewDaemon; tests
// inject a fake watcher to drive the event loop without real fsnotify.
//
// Exported (rather than the originally-planned `newDaemonWithWatcher`
// + `export_test.go` test seam) because the project's test-location
// convention places external tests at `tests/bear/engine/`, a separate
// directory from the package source — which means an in-package
// `_test.go` file cannot bridge unexported symbols across the directory
// gap. Mirrors the apply.go::ComputeContentHash precedent (see
// docstring there for the same rationale).
func NewDaemonWithWatcher(opts DaemonOpts, w FsWatcher) *Daemon {
	applyDaemonDefaults(&opts)
	return &Daemon{opts: opts, watcher: w}
}

func applyDaemonDefaults(opts *DaemonOpts) {
	if opts.DebouncePause == 0 {
		opts.DebouncePause = DefaultDebouncePause
	}
	if opts.MaxBurstWindow == 0 {
		opts.MaxBurstWindow = DefaultMaxBurstWindow
	}
	if opts.SelfWriteEpsilon == 0 {
		opts.SelfWriteEpsilon = DefaultSelfWriteEpsilon
	}
	// MtimePollInterval intentionally is NOT defaulted here: zero is a
	// valid "disabled" sentinel from DaemonConfig. CLI callers thread
	// cfg.MtimePollInterval through verbatim; tests / library callers
	// that construct DaemonOpts manually with a zero value get a
	// disabled poll loop, which is the conservative behavior. The 30s
	// default lives in bear/config.daemonDefaults at the config layer.
	if opts.StatFn == nil {
		opts.StatFn = os.Stat
	}
	if opts.DatabaseChangeTokenFn == nil {
		opts.DatabaseChangeTokenFn = fileMetadataChangeToken
	}
}

func fileMetadataChangeToken(_ string, info os.FileInfo) (string, error) {
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), nil
}

// formatPollInterval renders a poll-loop interval for the startup log
// line — used for both MtimePollInterval and AutoTagPollInterval.
// Zero is reported as "disabled" rather than "0s" so operators can
// grep for the disabled-state at a glance; any positive value uses
// [time.Duration.String] (e.g. "30s", "1m0s").
func formatPollInterval(d time.Duration) string {
	if d == 0 {
		return "disabled"
	}
	return d.String()
}

// noopTickerStop is the no-op stop function returned by
// [startTickerOrNil] when the configured interval is zero (the
// "disabled select arm" idiom used by both Daemon.Run pollers).
func noopTickerStop() {
	// Intentional no-op: when AutoTagPollInterval or MtimePollInterval
	// is zero, no ticker is ever created, so there is nothing to stop.
	// The named function gives the empty-function linter a stable
	// anchor — it has no runtime behavior.
}

// startTickerOrNil creates a [time.Ticker] of the given interval and
// returns its channel together with a stop function. When interval == 0
// the channel is nil and the stop function is a no-op, encoding the
// "disabled select arm" idiom used by both [Daemon.Run]'s mtime-poll
// and auto-tag fast-pass tickers. The caller
// `defer`s the returned stop so a single helper covers both arms
// without code duplication.
func startTickerOrNil(interval time.Duration) (<-chan time.Time, func()) {
	if interval <= 0 {
		return nil, noopTickerStop
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

// Close releases the underlying watcher. Safe to call multiple times
// only insofar as the underlying fsnotify watcher is — for the test
// fake watcher the call closes channels exactly once.
func (d *Daemon) Close() error {
	return d.watcher.Close()
}

// Run drives the FSEvents loop until ctx is canceled or the watcher
// closes. Returns ctx.Err on cancellation, nil on watcher close.
//
