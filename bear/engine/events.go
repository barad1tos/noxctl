package engine

// Event handlers — FSEvent filtering + debounce trigger logic +
// mtime-poll synthetic events + autotag fast-pass tick dispatcher.

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
)

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

// handlePollTick is the 6th-case body of Daemon.Run. Stats
// database.sqlite via opts.StatFn; on mtime advance, routes a
// synthetic fsnotify.Event through handleEvent so the debounce +
// max-burst + self-write-gate path is uniform with the real FSEvent
// trigger. Stat errors are non-fatal — they typically mean Bear
// rotated the file mid-flush, and the next tick recovers. Extracted
// from Run to keep gocognit ≤15.
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

// handleAutoTagTick is the 7th-case body of Daemon.Run. Runs the four
// fast-passes — ApplyForeignTagEscape, ApplyDailyDefaultTag,
// ApplyDomainBootstrap, then ApplyPlaceholderRefresh — independent of
// the full per-domain regen cycle. Order matches engine.Apply's
// prePassSpec loop in apply.go: foreign-tag escape first so a freshly-
// stamped daily note cannot be misclassified by the escape pass on
// the same tick; domain-bootstrap third so notes the daily/escape
// passes just stamped get their destination-canonical body written in
// the same tick; placeholder refresh last so it can rewrite the H1
// marker on notes the daily pass just produced via x-callback
// bootstrap.
//
// Gate: if a regen cycle is in progress, skip the tick silently — the
// next AutoTagPollInterval tick retries. No coalescing, no DEBUG log
// noise.
//
// Self-write gate: markRegenStart/markRegenEnd wrap the entire tick
// so the effectiveSelfWriteEpsilon absorbs the fast-pass's own
// writes. Accepts a brief FSEvent blackout window (~100-300ms per
// tick) — the next user keystroke arms the debounce timer normally.
//
// Error policy: all pre-passes run regardless of any prior pass's
// outcome; log-and-continue. Mirrors apply.go::runPrePass.
//
// Bearcli semaphore: all pre-pass calls route through
// domain.runBearcli → SetBearcliConcurrency pool. No bypass.
//
// Best-effort error contract: per-pass failures are logged with a
// `(continuing)` suffix and the tick keeps going. There is no return
// value because the daemon select loop has no consumer for it —
// fast-pass failure is not a user-visible event, the operator sees
// it in the daemon log. Use `noxctl verify --check daemon-log` to
// gate on the post-startup error rate.
func (d *Daemon) handleAutoTagSelectTick(
	ctx context.Context,
	quietTimer, maxTimer *time.Timer,
	burstActive *bool,
	lastMtime *time.Time,
) bool {
	handled, closed := d.handleQueuedWatcherEvent(quietTimer, maxTimer, burstActive)
	if closed {
		return true
	}
	if handled {
		return false
	}
	d.handleAutoTagLoopTick(ctx, *burstActive, lastMtime)
	return false
}

func (d *Daemon) handleQueuedWatcherEvent(quietTimer, maxTimer *time.Timer, burstActive *bool) (bool, bool) {
	select {
	case event, ok := <-d.watcher.Events():
		if !ok {
			return false, true
		}
		d.handleEvent(event, quietTimer, maxTimer, burstActive)
		return true, false
	default:
		return false, false
	}
}

func (d *Daemon) handleAutoTagLoopTick(ctx context.Context, burstActive bool, lastMtime *time.Time) {
	if burstActive {
		return
	}
	wrote, ran := d.handleAutoTagTick(ctx)
	if !ran {
		return
	}
	d.drainQueuedWatcherEvents()
	d.updatePollBaseline(lastMtime)
	if wrote == 0 {
		return
	}
	d.cycleOnce(ctx, "auto-tag fast-pass wrote changes")
	d.updatePollBaseline(lastMtime)
}

func (d *Daemon) handleAutoTagTick(ctx context.Context) (int, bool) {
	d.regenMu.Lock()
	if d.regenInProgress {
		d.regenMu.Unlock()
		return 0, false
	}
	d.regenInProgress = true
	d.regenMu.Unlock()

	// We set regenInProgress in-line above (matching markRegenStart's
	// semantics) so watcher events delivered during the fast-pass are
	// classified as daemon-originated. The Run loop drains queued watcher
	// events and refreshes the poll baseline after this method returns;
	// that keeps read-only bearcli side effects from starting a full regen.
	// Only actual writes extend the post-write gate and request a follow-up
	// full apply.
	// canonical-bootstrap wiring: build the tag→*Domain lookup
	// once per tick so both pre-pass paths can write destination-canonical
	// form in a single bearcli call.
	domainsByTag := domain.DomainsByTag(d.opts.Domains)
	dailyDomain := domainsByTag[d.opts.DailyDefaultTag]
	// dailyTagOn folds the catalog gate: an operator who omitted
	// `[meta].daily_default_tag` gets a silently disabled fast-pass
	// instead of `daily-default failed: dailyDomain is nil` log spam
	// every poll tick (default 2s). Mirror of the apply.go gate so
	// daemon and one-shot paths agree.
	//
	// placeholder-refresh stays on Features.AutoTagDefault alone:
	// ApplyPlaceholderRefresh iterates every domain with a non-empty
	// QuickPlaceholderH1, independent of the daily tag. Folding the
	// daily gate in would silently disable placeholder refresh for
	// catalogs that declare `quick_placeholder_h1` on a domain
	// without setting `[meta].daily_default_tag`.
	dailyTagOn := d.opts.Features.AutoTagDefault && d.opts.DailyDefaultTag != ""
	feats := d.opts.Features
	mkPass := func(name string, enabled bool, fn func(context.Context) (int, error)) autoTagPass {
		return autoTagPass{name: name, enabled: enabled, fn: fn}
	}
	passes := []autoTagPass{
		mkPass("foreign-tag escape", feats.ForeignTagEscape,
			func(c context.Context) (int, error) { return fastpass.ApplyForeignTagEscape(c, domainsByTag) }),
		mkPass("daily-default", dailyTagOn,
			func(c context.Context) (int, error) { return fastpass.ApplyDailyDefaultTag(c, dailyDomain) }),
		mkPass("domain-bootstrap", feats.DomainBootstrap,
			func(c context.Context) (int, error) { return fastpass.ApplyDomainBootstrap(c, domainsByTag) }),
		mkPass("placeholder-refresh", feats.AutoTagDefault,
			func(c context.Context) (int, error) { return fastpass.ApplyPlaceholderRefresh(c, domainsByTag) }),
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
	return wrote, true
}

func (d *Daemon) drainQueuedWatcherEvents() {
	for {
		select {
		case _, ok := <-d.watcher.Events():
			if !ok {
				return
			}
		default:
			return
		}
	}
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
// to apply), flock acquire, engine.Apply (with SkipFlock=true to avoid
// nested flock — see the deadlock note below), flock release. Errors
// logged-and-continued; the next FSEvent triggers the next attempt.
//
// Nested-flock deadlock note: cycleOnce holds the daemon flock via
// AcquireDaemon. Calling engine.Apply with SkipFlock=false would
// invoke AcquireApply on the same lockPath from an independent fd;
// macOS BSD flock semantics deadlock on nested LOCK_EX from separate
// fds AND flock is not ctx-aware, so cancellation cannot break the
// deadlock. Setting SkipFlock=true on the inner ApplyOpts tells
// engine.Apply to bypass BOTH AcquireApply AND the .apply-pending
// sentinel write (semantic correctness — daemon's internal Apply is
// not "external apply requesting priority").
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
	// bypasses AcquireApply + sentinel — see deadlock note above.
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
	now := time.Now()
	if !now.Before(d.regenEndTime.Add(d.effectiveSelfWriteEpsilon())) {
		return false
	}
	// A gated DB event proves Bear is still flushing work caused by this
	// daemon. Slide the watermark forward so later delivery of the same
	// delayed SQLite activity does not escape the original gate window.
	d.regenEndTime = now
	return true
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
