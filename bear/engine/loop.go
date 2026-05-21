package engine

// Daemon main loop — the long-running select that drives cycleOnce on
// FSEvent bursts, mtime-poll ticks, and autotag fast-pass ticks.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/barad1tos/noxctl/bear/domain"
)

// Run is the daemon main loop: wires the FSEvent watcher, the mtime
// poll fallback, and the autotag fast-pass tick into a single
// select, driving cycleOnce on each burst-completion. cycleOnce
// wraps the per-domain RunRegen path with the sentinel-yield and
// per-instance self-write gate that suppresses FSEvent feedback
// on our own writes.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.watcher.Add(d.opts.BearDBDir); err != nil {
		return fmt.Errorf("Daemon.Run watcher.Add: %w", err)
	}

	// Initialize the bearcli pool before any trigger source fires
	// (deploy fix). engine.Apply also calls this — sync.Once
	// inside package domain makes the second call a no-op — but the
	// auto-tag fast-pass calls fastpass.ApplyForeignTagEscape /
	// fastpass.ApplyDailyDefaultTag directly, bypassing engine.Apply. Without
	// this pre-loop init, the first 2s fast-pass tick fires before any
	// FSEvent burst and hits "bearcli pool not initialized".
	bearcliConcurrency := d.opts.BearcliConcurrency
	if bearcliConcurrency <= 0 {
		bearcliConcurrency = DefaultBearcliConcurrency
	}
	domain.SetBearcliConcurrency(bearcliConcurrency)

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

	// Poll-ticker setup. When MtimePollInterval == 0 the pollTick stays
	// nil and `case <-pollTick:` blocks forever — Go's canonical
	// "disabled select arm" idiom. lastMtime tracks the most recent
	// ModTime observed; initialized to the zero time.Time so the FIRST
	// poll tick after daemon startup ALWAYS observes "changed" and
	// forces a catch-up cycle.
	var lastMtime time.Time
	pollTick, stopPoll := startTickerOrNil(d.opts.MtimePollInterval)
	defer stopPoll()

	// Auto-tag fast-pass ticker setup. Same nil-channel idiom as
	// pollTick: when AutoTagPollInterval == 0 the channel stays nil and
	// `case <-autoTagTick:` blocks forever.
	autoTagTick, stopAutoTag := startTickerOrNil(d.opts.AutoTagPollInterval)
	defer stopAutoTag()

	for {
		select {
		case <-ctx.Done():
			// Drain in-flight regen via regenMu.
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
		case <-pollTick:
			// Stat database.sqlite, compare ModTime to lastMtime, route
			// a change through the same handleEvent path FSEvents use
			// (debounce, burst, self-write-gate all apply uniformly) —
			// no fast-path for poll.
			d.handlePollTick(quietTimer, maxTimer, &burstActive, &lastMtime)
		case <-autoTagTick:
			// Run ONLY the four fast-passes (foreign-tag escape, daily-
			// default, domain-bootstrap, placeholder-refresh — in that
			// order) — NEVER the full per-domain regen cycle. Skips
			// silently if a regen is already in progress. Self-write
			// gate is honored because handleAutoTagTick wraps the work
			// in markRegenStart/markRegenEnd.
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
