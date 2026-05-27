package engine

// Event handlers — FSEvent filtering + debounce trigger logic +
// mtime-poll synthetic events + autotag fast-pass tick dispatcher.

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
)

type databaseBaseline struct {
	modTime       time.Time
	token         string
	pending       bool
	pendingChange databaseCandidate
}

type databaseCandidate struct {
	modTime time.Time
	token   string
}

func (b *databaseBaseline) pendingCandidate() (databaseCandidate, bool) {
	if !b.pending {
		return databaseCandidate{}, false
	}
	return b.pendingChange, true
}

func (b *databaseBaseline) markPending(candidate databaseCandidate) {
	b.modTime = candidate.modTime
	b.pendingChange = candidate
	b.pending = true
}

func (b *databaseBaseline) commit(candidate databaseCandidate) {
	b.modTime = candidate.modTime
	b.token = candidate.token
	b.pending = false
	b.pendingChange = databaseCandidate{}
}

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

// handlePollTick is the 6th-case body of Daemon.Run. database.sqlite mtime is
// only the cheap wake-up signal; a content-level token decides whether the
// daemon should route a synthetic event. This filters Bear/bearcli read
// housekeeping that advances the SQLite file mtime without changing notes.
func (d *Daemon) handlePollTick(quietTimer, maxTimer *time.Timer, burstActive *bool, baseline *databaseBaseline) {
	dbPath := filepath.Join(d.opts.BearDBDir, dbFilename)
	_, changed, replayPending, err := d.nextDatabaseCandidate(dbPath, baseline)
	if err != nil {
		log.Printf("poll: %s failed: %v", dbPath, err)
		return
	}
	if !changed {
		return
	}
	if replayPending && *burstActive {
		return
	}
	d.handleEvent(fsnotify.Event{Op: fsnotify.Write, Name: dbPath}, quietTimer, maxTimer, burstActive)
}

// autoTagPass is one row in [Daemon.handleAutoTagTick]'s pass slice — a
// named struct (instead of an anonymous literal) so the slice literal
// stays readable when the four passes are listed inline.
type autoTagPass struct {
	name    string
	enabled bool
	fn      func(context.Context) (int, error)
}

func (d *Daemon) handleCycleTimer(
	ctx context.Context,
	reason string,
	pollBaseline, autoTagBaseline *databaseBaseline,
	burstActive *bool,
	stopTimer *time.Timer,
) {
	if d.cycleOnce(ctx, reason) {
		if d.opts.MtimePollInterval > 0 {
			d.updateDatabaseBaseline(pollBaseline)
		}
		if d.opts.AutoTagPollInterval > 0 {
			d.updateDatabaseBaseline(autoTagBaseline)
		}
	}
	*burstActive = false
	stopTimer.Stop()
}

func (d *Daemon) handleAutoTagSelectTick(
	ctx context.Context,
	quietTimer, maxTimer *time.Timer,
	burstActive *bool,
	pollBaseline, autoTagBaseline *databaseBaseline,
) bool {
	closed := d.handleQueuedWatcherEvents(quietTimer, maxTimer, burstActive)
	if closed {
		return true
	}
	d.handleAutoTagLoopTick(ctx, quietTimer, maxTimer, burstActive, pollBaseline, autoTagBaseline)
	return false
}

func (d *Daemon) handleQueuedWatcherEvents(quietTimer, maxTimer *time.Timer, burstActive *bool) bool {
	for {
		select {
		case event, ok := <-d.watcher.Events():
			if !ok {
				return true
			}
			d.handleEvent(event, quietTimer, maxTimer, burstActive)
		default:
			return false
		}
	}
}

func (d *Daemon) handleAutoTagLoopTick(
	ctx context.Context,
	quietTimer, maxTimer *time.Timer,
	burstActive *bool,
	pollBaseline, autoTagBaseline *databaseBaseline,
) {
	dbPath := filepath.Join(d.opts.BearDBDir, dbFilename)
	if *burstActive || d.isRegenInProgress() {
		return
	}
	candidate, changed, _, err := d.nextDatabaseCandidate(dbPath, autoTagBaseline)
	if err != nil {
		log.Printf("auto-tag poll: %s failed: %v", dbPath, err)
		return
	}
	if !changed {
		return
	}
	wrote, ran, failed := d.handleAutoTagTick(ctx)
	if !ran {
		return
	}
	closed := d.handleQueuedWatcherEvents(quietTimer, maxTimer, burstActive)
	if closed || *burstActive {
		return
	}
	if wrote == 0 {
		if failed {
			return
		}
		autoTagBaseline.commit(candidate)
		return
	}
	if d.cycleOnce(ctx, "auto-tag fast-pass wrote changes") {
		if d.opts.MtimePollInterval > 0 {
			d.updateDatabaseBaseline(pollBaseline)
		}
		d.updateDatabaseBaseline(autoTagBaseline)
	}
}

func (d *Daemon) isRegenInProgress() bool {
	d.regenMu.Lock()
	defer d.regenMu.Unlock()
	return d.regenInProgress
}

// handleAutoTagTick runs the four fast-passes — ApplyForeignTagEscape,
// ApplyDailyDefaultTag, ApplyDomainBootstrap, then ApplyPlaceholderRefresh —
// independent of the full per-domain regen cycle. Order matches engine.Apply's
// prePassSpec loop in apply.go: foreign-tag escape first so a freshly-stamped
// daily note cannot be misclassified by the escape pass on the same tick;
// domain-bootstrap third so notes the daily/escape passes just stamped get their
// destination-canonical body written in the same tick; placeholder refresh last
// so it can rewrite the H1 marker on notes the daily pass just produced via
// x-callback bootstrap.
//
// Error policy: all pre-passes run regardless of any prior pass's outcome;
// log-and-continue. The caller keeps the database token pending when any pass
// fails so a transient bearcli error retries on the next tick.
//
// Bearcli semaphore: all pre-pass calls route through domain.runBearcli →
// SetBearcliConcurrency pool. No bypass.
func (d *Daemon) handleAutoTagTick(ctx context.Context) (int, bool, bool) {
	d.regenMu.Lock()
	if d.regenInProgress {
		d.regenMu.Unlock()
		return 0, false, false
	}
	d.regenInProgress = true
	d.regenMu.Unlock()

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
	failed := false
	for _, p := range passes {
		if !p.enabled {
			continue
		}
		n, err := p.fn(ctx)
		if err != nil {
			failed = true
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
	return wrote, true, failed
}

func (d *Daemon) updateDatabaseBaseline(baseline *databaseBaseline) {
	dbPath := filepath.Join(d.opts.BearDBDir, dbFilename)
	info, err := d.opts.StatFn(dbPath)
	if err != nil {
		return
	}
	token, tokenErr := d.databaseChangeToken(dbPath, info)
	if tokenErr != nil {
		return
	}
	baseline.commit(databaseCandidate{modTime: info.ModTime(), token: token})
}

func (d *Daemon) nextDatabaseCandidate(
	dbPath string,
	baseline *databaseBaseline,
) (databaseCandidate, bool, bool, error) {
	if candidate, ok := baseline.pendingCandidate(); ok {
		return candidate, true, true, nil
	}
	info, err := d.opts.StatFn(dbPath)
	if err != nil {
		return databaseCandidate{}, false, false, fmt.Errorf("stat: %w", err)
	}
	mt := info.ModTime()
	if !mt.After(baseline.modTime) {
		return databaseCandidate{}, false, false, nil
	}
	token, err := d.databaseChangeToken(dbPath, info)
	if err != nil {
		return databaseCandidate{}, false, false, fmt.Errorf("token: %w", err)
	}
	candidate := databaseCandidate{modTime: mt, token: token}
	if token == baseline.token {
		baseline.commit(candidate)
		return databaseCandidate{}, false, false, nil
	}
	baseline.markPending(candidate)
	return candidate, true, false, nil
}

func (d *Daemon) databaseChangeToken(dbPath string, info fs.FileInfo) (string, error) {
	token, err := d.opts.DatabaseChangeTokenFn(dbPath, info)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", fmt.Errorf("empty database change token")
	}
	return token, nil
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
func (d *Daemon) cycleOnce(ctx context.Context, reason string) bool {
	log.Printf("regen trigger: %s", reason)
	if IsApplyPending(d.opts.LockPath) {
		log.Printf("apply-pending sentinel present; skipping cycle (apply will run)")
		return false
	}
	release, err := AcquireDaemon(ctx, d.opts.LockPath)
	if err != nil {
		log.Printf("daemon flock failed: %v", err)
		return false
	}
	defer release()

	d.markRegenStart()
	defer d.markRegenEnd()

	// Construct inner ApplyOpts with SkipFlock=true so engine.Apply
	// bypasses AcquireApply + sentinel — see deadlock note above.
	applyOpts := d.opts.ApplyOpts
	applyOpts.SkipFlock = true
	result, applyErr := Apply(ctx, applyOpts)
	if applyErr != nil {
		log.Printf("daemon cycle: %v", applyErr)
		return false
	}
	if result == nil {
		log.Printf("daemon cycle returned no result; preserving database baseline for retry")
		return false
	}
	if result.Interrupted {
		log.Printf("daemon cycle interrupted; preserving database baseline for retry")
		return false
	}
	if result.AnyFailed() {
		log.Printf("daemon cycle reported failed work; preserving database baseline for retry")
		return false
	}
	return true
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

// effectiveSelfWriteEpsilon widens the fixed self-write gate when polling is
// active so one poll-tick-plus-debounce roundtrip still lands inside the same
// daemon-originated write window. Incoming events never slide this window
// forward; only known daemon writes move regenEndTime.
//
// Returns the operator-configured SelfWriteEpsilon when polling is off
// (MtimePollInterval == 0) — no need to widen if there is no poll-tick
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
