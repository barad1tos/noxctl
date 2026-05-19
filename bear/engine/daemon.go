// Package engine daemon — long-running watcher loop driving cycleOnce on
// FSEvent bursts.
//
// On macOS this uses fsnotify's kqueue backend; legacy docs may say
// "FSEvents" but fsnotify v1.x has always been kqueue-on-Darwin
// (Pitfall 4 from research). Behavior is identical from the
// daemon's vantage point — events arrive on `watcher.Events`, debounced
// + max-burst-windowed before triggering a cycle.
//
// Self-write gate: FSEvents during a regen cycle and within
// `SelfWriteEpsilon` afterwards are assumed to come from our own
// bearcli writes. Without this gate, regen writes hub → Bear writes
// DB → daemon thinks user changed something → new regen → infinite
// loop. The gate is per-[Daemon] instance (NOT package globals like
// pre-Phase-2 `cmd/regen-watchd`).
//
// Bear-write polling fallback (POLL-01..05): in addition to
// the FSEvent path, [Daemon.Run] runs an optional polling loop that
// stats database.sqlite every `MtimePollInterval` and routes detected
// mtime changes through the same handleEvent path FSEvents use. The
// poll path is a FALLBACK, not a replacement: it closes the gap when
// Bear defers SQLite writes (e.g., drag-to-tag UI actions buffer in
// memory and flush opportunistically — observed delay ~2.5min on a
// real vault). Set `MtimePollInterval=0` to disable the loop entirely;
// the daemon then relies exclusively on FSEvents.
//
// Auto-tag fast-pass (TAG-01..05): a THIRD trigger source
// alongside FSEvent + mtime-poll. [Daemon.Run] runs an optional fast-
// pass loop that ticks every `AutoTagPollInterval` (default 2s) and
// invokes ONLY bear.ApplyForeignTagEscape + bear.ApplyDailyDefaultTag
// — NOT the full per-domain regen cycle. The goal: click-then-type
// quicknotes get `#quicknote/daily` stamped within <= 5s p95 instead
// of the empirically-measured 13-15s the FSEvent-then-Bear-flush path
// delivers. Set `AutoTagPollInterval=0` to disable the fast-pass; the
// daemon then relies on FSEvent + mtime-poll only.
//
// Self-write gate: fast-pass ticks set regenInProgress=true for the
// duration of the two pre-pass calls (via markRegenStart/markRegenEnd
// — same helpers cycleOnce uses). The widened
// effectiveSelfWriteEpsilon absorbs the fast-pass's own writes
// uniformly with cycle writes; no new gate state is introduced.
package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear"
)

const (
	// DefaultDebouncePause is the quiet window after the last FSEvent
	// before Daemon.Run runs a regen cycle (click-to-result ~2-4s with
	// bearcli regen).
	DefaultDebouncePause = 2 * time.Second

	// DefaultMaxBurstWindow caps the wait when the user keeps typing —
	// 1 minute brackets a typical note-editing burst without letting
	// the daemon stall indefinitely.
	DefaultMaxBurstWindow = 1 * time.Minute

	// DefaultSelfWriteEpsilon is the self-write gate window: FSEvents
	// landing within this window of a markRegenStart are filtered as
	// daemon-originated noise rather than user edits.
	DefaultSelfWriteEpsilon = 2 * time.Second

	// DefaultMtimePollInterval is the period between database.sqlite
	// mtime checks (POLL-01 ship default). Zero is the
	// "disabled" sentinel resolved at bear/config.daemonDefaults — see
	// DaemonOpts.MtimePollInterval for the full semantics.
	DefaultMtimePollInterval = 30 * time.Second

	// DefaultAutoTagPollInterval is the period between fast-pass ticks
	// that run ForeignTagEscape + DailyDefaultTag (TAG-01 ship
	// default). Zero is the "disabled" sentinel resolved at
	// bear/config.daemonDefaults — see DaemonOpts.AutoTagPollInterval
	// for the full semantics.
	DefaultAutoTagPollInterval = 2 * time.Second

	// dbFilename is the SQLite main-DB file Bear writes to. Used by the
	// poll path (POLL-02) and the FSEvent filter
	// (isWatchedDBEvent). Bear runs in journal_mode=delete, so neither
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
	// checks (POLL-01). When > 0, Daemon.Run creates a
	// time.Ticker that drives a 6th select case. When 0, polling is
	// disabled entirely — no ticker, no goroutines, no work (Go's nil-
	// channel idiom: `case <-pollCh:` blocks forever). The 30s default
	// is applied at the config layer (bear/config.daemonDefaults), NOT
	// here, because zero must remain meaningful as a "disabled"
	// sentinel for operators who explicitly opt out.
	MtimePollInterval time.Duration

	// StatFn is the os.Stat-like seam used by the poll loop. Production
	// leaves it nil; applyDaemonDefaults wires os.Stat. Tests inject a
	// scripted fake that returns canned os.FileInfo per call. Narrow
	// surface (D-01): one function field, no new interface.
	// Mirrors the test-seam pattern at bear/new_note.go::nowForNewNoteLink.
	StatFn func(path string) (os.FileInfo, error)

	// AutoTagPollInterval is the period between fast-pass ticks that run
	// ONLY bear.ApplyForeignTagEscape + bear.ApplyDailyDefaultTag —
	// independent of the full per-domain regen cycle (TAG-01).
	// When > 0, Daemon.Run creates a second time.Ticker driving a 7th
	// select case. When 0, the fast-pass is disabled (nil-channel idiom,
	// mirrors MtimePollInterval).
	//
	// Default 2s is applied at the config layer
	// (bear/config.daemonDefaults) — same precedence chain as
	// MtimePollInterval. CLI callers thread cfg.AutoTagPollInterval
	// verbatim; tests / library callers that construct DaemonOpts manually
	// get a disabled fast-pass on the zero value.
	//
	// Test seam (D-01): the fast-pass tick body calls
	// bear.ApplyForeignTagEscape + bear.ApplyDailyDefaultTag, which both
	// route through bear.runBearcli + BackendFromContext(ctx). DaemonOpts
	// gains NO parallel field for fake injection — tests stamp the seam
	// on ctx via bear.ContextWithBackend.
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
	// valid "disabled" sentinel from DaemonConfig (POLL-03).
	// CLI callers (cmd/regen-watchd) thread cfg.MtimePollInterval
	// through verbatim; tests / library callers that construct
	// DaemonOpts manually with a zero value get a disabled poll loop,
	// which is the conservative behavior. The 30s default lives in
	// bear/config.daemonDefaults at the config layer.
	if opts.StatFn == nil {
		opts.StatFn = os.Stat
	}
}

// formatPollInterval renders a poll-loop interval for the startup log
// line — used for both MtimePollInterval (D-08) and
// AutoTagPollInterval (D-07). Zero is reported as "disabled"
// rather than "0s" so operators can grep for the disabled-state at a
// glance; any positive value uses [time.Duration.String] (e.g. "30s",
// "1m0s").
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
	// The named function exists purely as an anchor for the empty-
	// function linter (Sonar S1186) — it has no runtime behavior.
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
// Mirrors pre-Phase-2 cmd/regen-watchd/main.go::eventLoop verbatim
// except triggerRegen is now cycleOnce (with sentinel-yield + flock
// added). Self-write gate is per-instance.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.watcher.Add(d.opts.BearDBDir); err != nil {
		return fmt.Errorf("Daemon.Run watcher.Add: %w", err)
	}

	// Initialize the bearcli pool before any trigger source fires
	// (deploy fix). engine.Apply also calls this — sync.Once
	// inside package bear makes the second call a no-op — but the
	// auto-tag fast-pass calls bear.ApplyForeignTagEscape /
	// bear.ApplyDailyDefaultTag directly, bypassing engine.Apply. Without
	// this pre-loop init, the first 2s fast-pass tick fires before any
	// FSEvent burst and hits "bearcli pool not initialized".
	bearcliConcurrency := d.opts.BearcliConcurrency
	if bearcliConcurrency <= 0 {
		bearcliConcurrency = DefaultBearcliConcurrency
	}
	bear.SetBearcliConcurrency(bearcliConcurrency)

	log.Printf(
		"noxctl daemon ready; debounce=%s, max-burst=%s, mtime-poll=%s, autotag-poll=%s, watch=%s, domains=%d",
		d.opts.DebouncePause, d.opts.MaxBurstWindow,
		formatPollInterval(d.opts.MtimePollInterval),
		formatPollInterval(d.opts.AutoTagPollInterval),
		d.opts.BearDBDir, len(d.opts.Domains),
	)

	quietTimer := time.NewTimer(time.Hour)
	quietTimer.Stop()
	maxTimer := time.NewTimer(time.Hour)
	maxTimer.Stop()
	burstActive := false

	// Poll-ticker setup (POLL-02). When MtimePollInterval == 0
	// the pollCh stays nil and `case <-pollCh:` blocks forever — Go's
	// canonical "disabled select arm" idiom. lastMtime tracks the most
	// recent ModTime observed; initialized to the zero time.Time so
	// the FIRST poll tick after daemon startup ALWAYS observes "changed"
	// and forces a catch-up cycle (per CONTEXT D-04).
	var lastMtime time.Time
	pollCh, stopPoll := startTickerOrNil(d.opts.MtimePollInterval)
	defer stopPoll()

	// Auto-tag fast-pass ticker setup (TAG-01/02). Same
	// nil-channel idiom as pollCh: when AutoTagPollInterval == 0 the
	// channel stays nil and `case <-autoTagCh:` blocks forever.
	autoTagCh, stopAutoTag := startTickerOrNil(d.opts.AutoTagPollInterval)
	defer stopAutoTag()

	for {
		select {
		case <-ctx.Done():
			// Drain in-flight regen via regenMu (mirror cmd/regen-watchd:289-291).
			d.regenMu.Lock()
			d.regenMu.Unlock() //nolint:staticcheck // intentional drain barrier — wait for cycleOnce to finish
			return ctx.Err()
		case event, ok := <-d.watcher.Events():
			if !ok {
				return nil
			}
			d.handleEvent(event, quietTimer, maxTimer, &burstActive)
		case <-quietTimer.C:
			d.cycleOnce(ctx, "quiet period reached")
			d.updatePollBaseline(&lastMtime)
			burstActive = false
			maxTimer.Stop()
		case <-maxTimer.C:
			d.cycleOnce(ctx, "max-burst window reached (events still incoming)")
			d.updatePollBaseline(&lastMtime)
			burstActive = false
			quietTimer.Stop()
		case <-pollCh:
			// POLL-02: stat database.sqlite, compare ModTime
			// to lastMtime, route a change through the same handleEvent
			// path FSEvents use (debounce, burst, self-write-gate all
			// apply uniformly). Per CONTEXT D-02 — no fast-path for poll.
			d.handlePollTick(quietTimer, maxTimer, &burstActive, &lastMtime)
		case <-autoTagCh:
			// TAG-02 +: run ONLY the four fast-passes
			// (foreign-tag escape, daily-default, domain-bootstrap,
			// placeholder-refresh — in that order) — NEVER the full
			// per-domain regen cycle. Skips silently if a regen is
			// already in progress (TAG-03). Self-write gate is honored
			// because handleAutoTagTick wraps the work in
			// markRegenStart/markRegenEnd (TAG-05).
			d.handleAutoTagTick(ctx)
		case err, ok := <-d.watcher.Errors():
			if !ok {
				return nil
			}
			log.Printf("watcher error: %v", err)
		}
	}
}

// handleEvent processes one fsnotify.Event: filters via isWatchedDBEvent
// (basename + self-write gate), resets the quiet timer, and arms the
// max-burst timer on first event of a burst. Extracted from Run to
// keep gocognit ≤15.
func (d *Daemon) handleEvent(event fsnotify.Event, quietTimer, maxTimer *time.Timer, burstActive *bool) {
	if !d.isWatchedDBEvent(event) {
		return
	}
	quietTimer.Reset(d.opts.DebouncePause)
	if *burstActive {
		return
	}
	*burstActive = true
	maxTimer.Reset(d.opts.MaxBurstWindow)
	log.Printf(
		"burst start: %s on %s; quiet=%s, max=%s",
		event.Op, filepath.Base(event.Name), d.opts.DebouncePause, d.opts.MaxBurstWindow,
	)
}

// handlePollTick is the 6th-case body of Daemon.Run (POLL-02).
// Stats database.sqlite via opts.StatFn; on mtime advance, routes a
// synthetic fsnotify.Event through handleEvent so the debounce +
// max-burst + self-write-gate path is uniform with the real FSEvent
// trigger (POLL-02 + POLL-04). Stat errors are non-fatal — they
// typically mean Bear rotated the file mid-flush, and the next tick
// recovers. Extracted from Run to keep gocognit ≤15.
func (d *Daemon) handlePollTick(quietTimer, maxTimer *time.Timer, burstActive *bool, lastMtime *time.Time) {
	dbPath := filepath.Join(d.opts.BearDBDir, dbFilename)
	info, err := d.opts.StatFn(dbPath)
	if err != nil {
		log.Printf("poll: stat %s failed: %v", dbPath, err)
		return
	}
	mt := info.ModTime()
	if !mt.After(*lastMtime) {
		return
	}
	*lastMtime = mt
	d.handleEvent(fsnotify.Event{Op: fsnotify.Write, Name: dbPath}, quietTimer, maxTimer, burstActive)
}

// autoTagPass is one row in [Daemon.handleAutoTagTick]'s pass slice — a
// named struct (instead of an anonymous literal) so the slice literal
// stays readable when the three passes are listed inline.
type autoTagPass struct {
	name    string
	enabled bool
	fn      func(context.Context) (int, error)
}

// handleAutoTagTick is the 7th-case body of Daemon.Run (TAG-02).
// Runs the four fast-passes — ApplyForeignTagEscape, ApplyDailyDefaultTag,
// ApplyDomainBootstrap, then ApplyPlaceholderRefresh —
// independent of the full per-domain regen cycle. Order matches
// engine.Apply's prePassSpec loop in apply.go: foreign-tag escape
// first so a freshly-stamped daily note cannot be misclassified by
// the escape pass on the same tick; domain-bootstrap third so notes
// the daily/escape passes just stamped get their destination-canonical
// body written in the same tick; placeholder refresh last so it
// can rewrite the H1 marker on notes the daily pass just produced
// via x-callback bootstrap.
//
// Gate (TAG-03): if a regen cycle is in progress, skip the tick
// silently — the next AutoTagPollInterval tick retries. No
// coalescing, no DEBUG log noise.
//
// Self-write gate (TAG-05): markRegenStart/markRegenEnd wrap the
// entire tick so the effectiveSelfWriteEpsilon absorbs the
// fast-pass's own writes. Accepts a brief FSEvent blackout window
// (~100-300ms per tick) — the next user keystroke arms the debounce
// timer normally (CONTEXT D-04).
//
// Error policy (D-03): all pre-passes run regardless of any prior
// pass's outcome; log-and-continue. Mirrors apply.go::runPrePass.
//
// Bearcli semaphore (TAG-04): all pre-pass calls route through
// bear.runBearcli → SetBearcliConcurrency pool. No bypass.
func (d *Daemon) handleAutoTagTick(ctx context.Context) {
	d.regenMu.Lock()
	if d.regenInProgress {
		d.regenMu.Unlock()
		return
	}
	d.regenInProgress = true
	d.regenMu.Unlock()

	// self-write gate fix: we set regenInProgress in-line above
	// (matches markRegenStart's semantics) but DO NOT call markRegenEnd
	// at exit — that would bump regenEndTime every 2s, keeping
	// effectiveSelfWriteEpsilon's 7s window perpetually open and
	// starving the FSEvent path of any user-driven cycle. Instead, we
	// clear regenInProgress unconditionally on exit and bump
	// regenEndTime ONLY when we actually wrote something through
	// bearcli. A no-write tick produces no FSEvent and thus needs no
	// gate window.
	// canonical-bootstrap wiring: build the tag→*Domain lookup
	// once per tick so both pre-pass paths can write destination-canonical
	// form in a single bearcli call.
	domainsByTag := bear.DomainsByTag(d.opts.Domains)
	dailyDomain := domainsByTag[d.opts.DailyDefaultTag]
	// autoTagOn folds the catalog gate: an operator who omitted
	// `[meta].daily_default_tag` gets a silently disabled fast-pass
	// instead of `daily-default failed: dailyDomain is nil` log spam
	// every poll tick (default 2s). Mirror of the apply.go pre-pass
	// gate so daemon and one-shot paths agree.
	autoTagOn := d.opts.Features.AutoTagDefault && d.opts.DailyDefaultTag != ""
	feats := d.opts.Features
	mkPass := func(name string, enabled bool, fn func(context.Context) (int, error)) autoTagPass {
		return autoTagPass{name: name, enabled: enabled, fn: fn}
	}
	passes := []autoTagPass{
		mkPass("foreign-tag escape", feats.ForeignTagEscape,
			func(c context.Context) (int, error) { return bear.ApplyForeignTagEscape(c, domainsByTag) }),
		mkPass("daily-default", autoTagOn,
			func(c context.Context) (int, error) { return bear.ApplyDailyDefaultTag(c, dailyDomain) }),
		mkPass("domain-bootstrap", feats.DomainBootstrap,
			func(c context.Context) (int, error) { return bear.ApplyDomainBootstrap(c, domainsByTag) }),
		mkPass("placeholder-refresh", autoTagOn,
			func(c context.Context) (int, error) { return bear.ApplyPlaceholderRefresh(c, domainsByTag) }),
	}
	wrote := 0
	for _, p := range passes {
		if !p.enabled {
			continue
		}
		n, err := p.fn(ctx)
		if err != nil {
			log.Printf("auto-tag fast-pass: %s failed: %v (continuing)", p.name, err)
		}
		wrote += n
	}

	d.regenMu.Lock()
	d.regenInProgress = false
	if wrote > 0 {
		d.regenEndTime = time.Now()
	}
	d.regenMu.Unlock()
}

// updatePollBaseline re-stats database.sqlite immediately after cycleOnce
// completes and locks lastMtime to the resulting ModTime. Without this
// reset the next poll tick (~MtimePollInterval after cycle end) sees the
// daemon's OWN bearcli writes as a fresh mtime advance and triggers a
// redundant cycle — an empirical cycle-storm observable as 7 cycles in
// 6 minutes of idle daemon (database.sqlite mtime advances ~4s after
// every cycleOnce, the 5s poll catches it, and SelfWriteEpsilon=2s has
// already closed). Resetting the baseline post-cycle aligns lastMtime with reality
// the daemon already knows about, so only EXTERNAL (user) writes after
// cycle-end can re-trigger.
//
// No-op when MtimePollInterval == 0 (polling disabled, lastMtime is
// unread anyway). Stat errors are non-fatal — a stale baseline just
// means the next poll re-stats and may catch up on a transient.
func (d *Daemon) updatePollBaseline(lastMtime *time.Time) {
	if d.opts.MtimePollInterval == 0 {
		return
	}
	dbPath := filepath.Join(d.opts.BearDBDir, dbFilename)
	info, err := d.opts.StatFn(dbPath)
	if err != nil {
		return
	}
	*lastMtime = info.ModTime()
}

// cycleOnce runs one regen cycle: sentinel skip-check (priority yield
// to apply per D-11), flock acquire, engine.Apply (with SkipFlock=true
// to avoid nested flock — see B-flock-deadlock note below), flock
// release. Errors logged-and-continued; the next FSEvent triggers
// the next attempt.
//
// B-flock-deadlock (Iteration 2 fix): cycleOnce holds the daemon
// flock via AcquireDaemon. Calling engine.Apply with SkipFlock=false
// would invoke AcquireApply on the same lockPath from an independent
// fd; macOS BSD flock semantics deadlock on nested LOCK_EX from
// separate fds AND flock is not ctx-aware, so cancellation cannot
// break the deadlock. Setting SkipFlock=true on the inner ApplyOpts
// tells engine.Apply to bypass BOTH AcquireApply AND the
// .apply-pending sentinel write (semantic correctness — daemon's
// internal Apply is not "external apply requesting priority").
func (d *Daemon) cycleOnce(ctx context.Context, reason string) {
	log.Printf("regen trigger: %s", reason)
	if IsApplyPending(d.opts.LockPath) {
		log.Printf("apply-pending sentinel present; skipping cycle (apply will run)")
		return
	}
	release, err := AcquireDaemon(ctx, d.opts.LockPath)
	if err != nil {
		log.Printf("daemon flock failed: %v", err)
		return
	}
	defer release()

	d.markRegenStart()
	defer d.markRegenEnd()

	// Construct inner ApplyOpts with SkipFlock=true so engine.Apply
	// bypasses AcquireApply + sentinel (B-flock-deadlock).
	applyOpts := d.opts.ApplyOpts
	applyOpts.SkipFlock = true
	if _, applyErr := Apply(ctx, applyOpts); applyErr != nil {
		log.Printf("daemon cycle: %v", applyErr)
	}
}

func (d *Daemon) markRegenStart() {
	d.regenMu.Lock()
	d.regenInProgress = true
	d.regenMu.Unlock()
}

func (d *Daemon) markRegenEnd() {
	d.regenMu.Lock()
	d.regenInProgress = false
	d.regenEndTime = time.Now()
	d.regenMu.Unlock()
}

// SetRegenInProgressForTest is a test seam — production code uses
// markRegenStart/markRegenEnd. Mirrors the SetBearcliConcurrency
// precedent: a tiny exported helper that lets tests flip an internal
// flag without orchestrating a real cycle. Honors the regenMu lock so
// it composes safely with markRegenStart/markRegenEnd if they fire
// concurrently.
//
// Production code MUST NOT call this — there is no use case outside
// TestDaemonAutoTagPoll_SkippedWhenRegenInProgress and any future
// direct-flag-toggle test. Setting v=true does NOT update regenEndTime
// (which would corrupt the self-write gate watermark); the test seam
// is intentionally narrower than markRegenStart.
func (d *Daemon) SetRegenInProgressForTest(v bool) {
	d.regenMu.Lock()
	defer d.regenMu.Unlock()
	d.regenInProgress = v
}

func (d *Daemon) isSelfTriggered() bool {
	d.regenMu.Lock()
	defer d.regenMu.Unlock()
	if d.regenInProgress {
		return true
	}
	return time.Now().Before(d.regenEndTime.Add(d.effectiveSelfWriteEpsilon()))
}

// effectiveSelfWriteEpsilon widens the self-write gate when polling is
// active so the post-cycle window covers one full poll-tick-plus-debounce
// roundtrip. Without this, the daemon's own bearcli writes still propagate
// to disk for several seconds after cycleOnce returns (Bear flushes the
// SQLite commit asynchronously), and the next poll tick (~MtimePollInterval
// after cycle end) sees an mtime advance that the static 2s
// SelfWriteEpsilon has already stopped gating. Empirically the trailing
// flush window is 3-5s; budget +3s past the poll tick + debounce sum
// covers it without leaning on a magic constant.
//
// Returns the operator-configured SelfWriteEpsilon when polling is off
// (MtimePollInterval == 0) — no need to widen if there's no poll-tick
// stream chasing the daemon's own writes.
func (d *Daemon) effectiveSelfWriteEpsilon() time.Duration {
	if d.opts.MtimePollInterval == 0 {
		return d.opts.SelfWriteEpsilon
	}
	pollWindow := d.opts.MtimePollInterval + d.opts.DebouncePause + 3*time.Second
	if pollWindow > d.opts.SelfWriteEpsilon {
		return pollWindow
	}
	return d.opts.SelfWriteEpsilon
}

// isWatchedDBEvent reports whether the FSEvent should reset the
// debounce timer. Filters on file basename (database.sqlite{,-wal,-shm})
// and self-write gate.
func (d *Daemon) isWatchedDBEvent(event fsnotify.Event) bool {
	base := filepath.Base(event.Name)
	if base != dbFilename && base != dbFilename+"-wal" && base != dbFilename+"-shm" {
		return false
	}
	if d.isSelfTriggered() {
		return false
	}
	return event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Chmod) != 0
}
