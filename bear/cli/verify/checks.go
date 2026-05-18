package verify

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/engine"
)

// failCheck constructs a StatusFail Check in one shot. Hoisted to a
// helper because the daemon-log and apply-idempotency blocks were
// structurally identical 4-line mutations of a pre-allocated Check —
// `dupl` flagged the pattern, and a shared constructor removes the
// duplicate token sequence without raising the lint threshold.
func failCheck(name, message string, details []string) Check {
	return Check{Name: name, Status: StatusFail, Message: message, Details: details}
}

// checkPlanParity runs `engine.Plan` against the loaded catalog and
// reports PASS when `PlanResult.HasDrift()` is false AND there are
// zero per-domain errors. Strict mode additionally fails when the
// residue scan reports untracked tag-families.
func checkPlanParity(ctx context.Context, opts Options, domains []*bear.Domain) Check {
	c := Check{Name: "plan-parity"}
	res, err := engine.Plan(ctx, engine.PlanOpts{
		Domains:      domains,
		ConfigSource: engine.ConfigSourceTOML,
		Stderr:       opts.Stderr,
	})
	if err != nil {
		c.Status = StatusError
		c.Message = fmt.Sprintf("engine.Plan returned error: %v", err)
		return c
	}
	if res.Interrupted {
		c.Status = StatusError
		c.Message = "engine.Plan interrupted (ctx canceled)"
		return c
	}
	if res.HasDrift() || res.Summary.DomainsError > 0 {
		c.Status = StatusFail
		c.Message = fmt.Sprintf(
			"plan reports drift: %d drift / %d error / %d clean across %d domains",
			res.Summary.DomainsDrift, res.Summary.DomainsError,
			res.Summary.DomainsClean, res.Summary.DomainsTotal,
		)
		c.Details = driftDomainList(res)
		return c
	}
	if opts.Strict && res.Summary.UntrackedFamilies > 0 {
		c.Status = StatusFail
		c.Message = fmt.Sprintf(
			"strict: %d untracked tag-family/families detected",
			res.Summary.UntrackedFamilies,
		)
		return c
	}
	c.Status = StatusPass
	c.Message = fmt.Sprintf(
		"%d domains clean (0 drift, 0 errors)",
		res.Summary.DomainsTotal,
	)
	return c
}

// driftDomainList collects the tags of the drift / error domains so
// the operator can target them without digging through the full JSON.
func driftDomainList(res *engine.PlanResult) []string {
	out := make([]string, 0, len(res.Domains))
	for _, d := range res.Domains {
		if d.Status == engine.StatusDrift || d.Status == engine.StatusError {
			out = append(out, fmt.Sprintf("%s (%s)", d.Tag, d.Status))
		}
	}
	return out
}

// daemonLogStartupMarker is the line the daemon emits on every fresh
// boot. `checkDaemonLog` rewinds to the most recent occurrence and
// scans forward from there — older history is by design out of scope.
const daemonLogStartupMarker = "regen-watchd starting"

// daemonLogWarnPattern matches the three categories of post-startup
// warnings that should never appear in a clean session. Anchored to
// the per-line content (not a full regex over the whole file) so a
// single warning shows up as one match.
var daemonLogWarnPattern = regexp.MustCompile(`(LOOP detected|EMERGENCY DISABLE|ERROR:)`)

// checkDaemonLog reads the daemon log, rewinds to the last "starting"
// marker, and reports PASS when zero warnings appear from that point
// onward. Distinguishes "daemon never ran" (StatusError) from "daemon
// ran clean" (StatusPass) — different operator actions follow.
func checkDaemonLog(opts Options) Check {
	c := Check{Name: "daemon-log"}
	path, err := resolveDaemonLogPath(opts.LogPath)
	if err != nil {
		c.Status = StatusError
		c.Message = err.Error()
		return c
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			c.Status = StatusError
			c.Message = fmt.Sprintf("daemon log not found at %s (daemon never ran?)", path)
			return c
		}
		c.Status = StatusError
		c.Message = fmt.Sprintf("open %s: %v", path, err)
		return c
	}
	defer func() { _ = f.Close() }()

	warnings, ok := scanLogSinceStartup(f)
	if !ok {
		c.Status = StatusError
		c.Message = fmt.Sprintf("no %q line in %s — daemon may have never started", daemonLogStartupMarker, path)
		return c
	}
	if len(warnings) > 0 {
		return failCheck(c.Name,
			fmt.Sprintf("%d warning(s) since last daemon startup", len(warnings)),
			warnings)
	}
	c.Status = StatusPass
	c.Message = "clean since last daemon startup"
	return c
}

// resolveDaemonLogPath honors an explicit override; otherwise falls
// back to `~/.cache/regen-watchd.log` (matching the daemon's hardcoded
// default).
func resolveDaemonLogPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("UserHomeDir: %w", err)
	}
	return filepath.Join(home, ".cache", "regen-watchd.log"), nil
}

// ScanDaemonLogForTest exposes `scanLogSinceStartup` to the external
// test package under `tests/bear/cli/verify/`. Production code reaches
// the same logic through `checkDaemonLog`; this wrapper exists solely
// to let hermetic tests exercise the rewind-to-last-startup semantics
// without a real log file.
func ScanDaemonLogForTest(r interface{ Read(p []byte) (int, error) }) ([]string, bool) {
	return scanLogSinceStartup(r)
}

// scanLogSinceStartup walks the log once, remembering the most recent
// "regen-watchd starting" line index, then collects every warning
// from that line forward. Returns (warnings, ok) — ok=false signals
// no startup marker was found.
//
// Single-pass on purpose; the daemon log can grow to MBs on a busy
// vault and a two-pass scan doubles the I/O.
func scanLogSinceStartup(r interface{ Read(p []byte) (int, error) }) ([]string, bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var warnings []string
	startupSeen := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, daemonLogStartupMarker) {
			startupSeen = true
			warnings = warnings[:0] // reset on each new startup
			continue
		}
		if !startupSeen {
			continue
		}
		if daemonLogWarnPattern.MatchString(line) {
			warnings = append(warnings, line)
		}
	}
	return warnings, startupSeen
}

// checkApplyIdempotency runs `engine.Apply` twice. The first pass
// may write (if the vault is drifted from the catalog); the second
// pass MUST report zero changes across every domain. Mirrors the
// idempotency contract: "≤3 passes to `unchanged` for every hub and
// master" — verify enforces the stronger one-pass-to-unchanged form
// because the catalog is the source of truth.
//
// Destructive: opt-in via --with-apply. Acquires the flock; daemon
// (if running) blocks until verify releases.
func checkApplyIdempotency(ctx context.Context, opts Options, domains []*bear.Domain) Check {
	c := Check{Name: "apply-idempotency"}

	// Pass 1: bring the vault to canonical state. Failures here are
	// not idempotency-class — they're the underlying apply path being
	// broken (e.g. bearcli outage mid-write). Treat as runtime error
	// so the operator sees the right top-line.
	first, err := runApplyOnce(ctx, opts, domains)
	if err != nil {
		c.Status = StatusError
		c.Message = fmt.Sprintf("first apply failed: %v", err)
		return c
	}

	// Pass 2: must be a strict no-op.
	second, err := runApplyOnce(ctx, opts, domains)
	if err != nil {
		c.Status = StatusError
		c.Message = fmt.Sprintf("second apply failed: %v", err)
		return c
	}

	offenders := nonIdempotentDomains(second)
	if len(offenders) > 0 {
		return failCheck(c.Name,
			fmt.Sprintf("%d domain(s) wrote on the second apply pass", len(offenders)),
			offenders)
	}
	if second.AnyFailed() {
		c.Status = StatusFail
		c.Message = "second apply pass reported per-domain failures"
		return c
	}

	// Capture pass-1 stats in the message — useful to know whether
	// the vault was already clean (0 changes on pass 1) or whether
	// verify converged it.
	c.Status = StatusPass
	c.Message = fmt.Sprintf(
		"second pass clean; pass-1 stats: %d created / %d changed / %d unchanged / %d failed across %d domains",
		sumApplyField(first, func(d engine.DomainCounts) int { return d.Created }),
		sumApplyField(first, func(d engine.DomainCounts) int { return d.Changed }),
		sumApplyField(first, func(d engine.DomainCounts) int { return d.Unchanged }),
		sumApplyField(first, func(d engine.DomainCounts) int { return d.Failed }),
		len(first.Domains),
	)
	return c
}

// runApplyOnce wraps `engine.Apply` with the verify-specific
// ApplyOpts (TOML-only catalog, all features default-on per the
// production daemon's set). Returns the result for stat collection
// or an error on infrastructure-level failure.
func runApplyOnce(ctx context.Context, opts Options, domains []*bear.Domain) (*engine.ApplyResult, error) {
	res, err := engine.Apply(ctx, engine.ApplyOpts{
		Domains:  domains,
		Features: engine.AllFeaturesOn(),
		Stderr:   opts.Stderr,
	})
	if err != nil {
		return nil, err
	}
	if res.Interrupted {
		return nil, fmt.Errorf("apply interrupted (ctx canceled)")
	}
	return res, nil
}

// nonIdempotentDomains returns the tags of any domain whose second
// pass produced a write (Created > 0 OR Changed > 0). Failed counts
// are surfaced separately via AnyFailed.
func nonIdempotentDomains(res *engine.ApplyResult) []string {
	out := make([]string, 0)
	for tag, counts := range res.Domains {
		if counts.Created > 0 || counts.Changed > 0 {
			out = append(out, fmt.Sprintf("%s (created=%d changed=%d)",
				tag, counts.Created, counts.Changed))
		}
	}
	return out
}

// sumApplyField totals one DomainCounts field across all domains in
// an ApplyResult — keeps the four-line summary message at the call
// site under the lll limit.
func sumApplyField(res *engine.ApplyResult, pick func(engine.DomainCounts) int) int {
	total := 0
	for _, c := range res.Domains {
		total += pick(c)
	}
	return total
}
