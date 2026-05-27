package engine

// Daemon main loop — the long-running select that drives cycleOnce on
// FSEvent bursts, mtime-poll ticks, and autotag fast-pass ticks.

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// Run is the daemon main loop: wires the FSEvent watcher, the mtime
// poll fallback, and the autotag fast-pass tick into a single
// select, driving cycleOnce on each burst-completion. cycleOnce
// wraps the per-domain regen path with the sentinel-yield and
// per-instance self-write gate that suppresses FSEvent feedback
// on our own writes.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.watcher.Add(d.opts.BearDBDir); err != nil {
		return fmt.Errorf("Daemon.Run watcher.Add: %w", err)
	}

	// Initialize the bearcli pool before any trigger source fires
	// (deploy fix). engine.Apply also calls this — sync.Once
	// inside package bearcli makes the second call a no-op — but the
	// auto-tag fast-pass calls fastpass.ApplyForeignTagEscape /
	// fastpass.ApplyDailyDefaultTag directly, bypassing engine.Apply. Without
	// this pre-loop init, the first 2s fast-pass tick fires before any
	// FSEvent burst and hits "bearcli pool not initialized".
	concurrency := d.opts.BearcliConcurrency
	if concurrency <= 0 {
		concurrency = DefaultBearcliConcurrency
	}
	bearcli.SetConcurrency(concurrency)

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
	// nil and `case <-pollTick:` blocks forever. pollBaseline starts at
	// zero so the first poll tick still performs the startup catch-up
	// check, but only a content-token change can arm a cycle.
	var pollBaseline databaseBaseline
	pollTick, stopPoll := startTickerOrNil(d.opts.MtimePollInterval)
	defer stopPoll()

	// Auto-tag fast-pass ticker setup. Same nil-channel idiom as
	// pollTick: when AutoTagPollInterval == 0 the channel stays nil and
	// `case <-autoTagTick:` blocks forever. Its baseline starts at the
	// current DB content token so an idle daemon does not poll Bear every tick.
	var autoTagBaseline databaseBaseline
	if d.opts.AutoTagPollInterval > 0 {
		d.updateDatabaseBaseline(&autoTagBaseline)
	}
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
			d.handleCycleTimer(
				ctx, "quiet period reached", &pollBaseline, &autoTagBaseline, &burstActive, maxTimer,
			)
		case <-maxTimer.C:
			d.handleCycleTimer(
				ctx, "max-burst window reached (events still incoming)",
				&pollBaseline, &autoTagBaseline, &burstActive, quietTimer,
			)
		case <-pollTick:
			// Stat database.sqlite first, then compare a content token
			// before routing a synthetic event through the same debounce
			// path FSEvents use. Mtime is only a cheap wake-up signal.
			d.handlePollTick(quietTimer, maxTimer, &burstActive, &pollBaseline)
		case <-autoTagTick:
			if d.handleAutoTagSelectTick(ctx, quietTimer, maxTimer, &burstActive, &pollBaseline, &autoTagBaseline) {
				return nil
			}
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
