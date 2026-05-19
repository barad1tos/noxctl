// Package engine apply orchestrator — one-shot wrapper around the
// pre-pass + per-domain RunRegen pipeline that previously lived in the
// legacy daemon's runAllRegens routine. Same pre-pass order, same
// log-and-continue policy, same self-write-gate discipline (the gate
// itself lives on engine.Daemon; Apply is one-shot and needs no gate).
//
// Layering: stdlib + bear + bear/state + golang.org/x/sys/unix.
// engine never imports bear/config — CLI shims map
// *config.Catalog.Features into engine.Features at the CLI boundary.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/state"
	"golang.org/x/sync/errgroup"
)

// ApplyOpts configures one Apply invocation. Plain struct, no methods,
// no defaults; caller supplies every field per "Accept interfaces,
// return structs".
type ApplyOpts struct {
	Domains   []*bear.Domain    // REQUIRED — engine iterates and calls RunRegen
	Pins      *bear.PinRegistry // REQUIRED — may be empty registry; nil-safe per bear/pins.go
	StatePath string            // REQUIRED — "./.noxctl/state.json"
	LockPath  string            // REQUIRED — "./.noxctl/.lock" (used by )
	Features  Features          // REQUIRED — flat pre-pass toggles
	NoWait    bool              // optional; --no-wait fail-fast on lock contention
	Stderr    io.Writer         // optional; default os.Stderr — used by lock-acquire wait advisory
	// AuditEnabled, when true, runs the AuditDomains pre-pass. Mirrors
	// the legacy daemon's REGEN_AUDIT != "off" gate. Default false.
	AuditEnabled bool
	// SkipFlock — optional; when true, Apply skips both the AcquireApply
	// flock acquire AND the.apply-pending sentinel write. Used by
	// engine.Daemon.cycleOnce, which already holds the daemon flock and
	// must not nest-acquire (would deadlock on macOS BSD flock semantics
	// — flock is not ctx-aware, so a nested LOCK_EX on an independent fd
	// blocks indefinitely). Daemon sets SkipFlock=true on the inner
	// ApplyOpts inside cycleOnce.
	SkipFlock bool

	// BearcliConcurrency is the operator-tuned cap for concurrent bearcli
	// subprocesses. Zero or negative => engine default
	// DefaultBearcliConcurrency. When > 0, Apply calls
	// bear.SetBearcliConcurrency(n) once at entry before any pre-pass
	// fires. The sync.Once gate inside SetBearcliConcurrency means
	// subsequent calls are silent no-ops; bench mode uses
	// bear.ResetBearcliPoolForTest to re-arm between sweep cycles.
	BearcliConcurrency int

	// WithMetrics, when true, copies the bearcli pool snapshot into
	// ApplyResult.Metrics at Apply completion. Zero cost when false. Set
	// by --bench mode; production daemon leaves false.
	WithMetrics bool

	// DomainTimingHook, when non-nil, is called inside runDomainAndSave
	// after each RunRegen returns. tag = domain.Tag, elapsed = RunRegen
	// wall-clock. Used by --bench mode to accumulate
	// per_domain[] in the JSON envelope; nil in production. Hook fires
	// from worker goroutines — implementations MUST be concurrency-safe
	// (atomic/mutex).
	DomainTimingHook func(tag string, elapsed time.Duration)

	// DailyDefaultTag binds the operator-chosen tag for untagged-on-
	// create notes (e.g. Bear's compose-from-Notes-view). Empty disables
	// the auto-tag fast-pass entirely so a fork with no notion of a
	// "daily" tag stays silent. Wired from [meta].daily_default_tag in
	// the TOML catalog.
	DailyDefaultTag string

	// PromotionRules drives the time-based promotion fast-pass. Empty
	// disables time-promotion entirely. Each rule names a source tag,
	// a target tag, and the calendar boundary that triggers the move.
	// Wired from [[promotion]] blocks in the TOML catalog.
	PromotionRules []bear.PromotionRule
}

// DefaultBearcliConcurrency is the ship-default capacity for
// `bear.SetBearcliConcurrency` across every entry point — apply,
// daemon, plan. Exported so the read-only `noxctl plan` path can
// install the same ceiling without redeclaring the constant
// (Sourcery PR-5: silently drifting defaults are a maintenance
// trap).
const DefaultBearcliConcurrency = 8

// Apply runs the orchestrator one-shot: acquires flock,
// runs pre-passes (gated by opts.Features), iterates opts.Domains
// calling RunRegen, persists state.json incrementally per-domain,
// releases flock.
//
// State.LastApply is set ONLY on successful completion; SIGINT or
// per-domain failure leaves it at the prior value. State.InProgress
// is set on entry, cleared on success — the plan engine uses both
// markers to discriminate completed-vs-interrupted runs.
//
// Stateless: owns no gate. Daemon (bear/engine/daemon.go) carries the
// regenMu/regenInProgress/regenEndTime state for FSEvents loops.
//
// This orchestrator preserves the per-DOMAIN ctx.Err check mirrored
// from the legacy daemon.
func Apply(ctx context.Context, opts ApplyOpts) (*ApplyResult, error) {
	result := &ApplyResult{
		PrePasses: make(map[string]PrePassCounts),
		Domains:   make(map[string]DomainCounts),
		StartedAt: time.Now().UTC(),
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	// Step -1: initialize the global bearcli subprocess
	// pool. SetBearcliConcurrency is sync.Once-gated inside package bear,
	// so a second Apply in the same process is a no-op. Bench-mode
	// (--sweep) drives ResetBearcliPoolForTest between cycles to re-arm
	// at a new capacity; production daemon path is one-shot per process.
	if opts.BearcliConcurrency <= 0 {
		opts.BearcliConcurrency = DefaultBearcliConcurrency
	}
	bear.SetBearcliConcurrency(opts.BearcliConcurrency)

	// Step 0: acquire flock + write apply-pending sentinel. Gated on
	// !opts.SkipFlock so engine.Daemon.cycleOnce can call engine.Apply
	// without nesting flock — macOS BSD flock is not ctx-aware, so a
	// nested LOCK_EX on an independent fd from the same process blocks
	// indefinitely. When SkipFlock=true: skip BOTH the flock acquire
	// AND the sentinel write (semantic correctness — the daemon's
	// internal Apply is not an external apply requesting priority).
	release := func() { /* no-op default; reassigned when !SkipFlock acquires lock */ }
	if !opts.SkipFlock {
		lk, lockErr := AcquireApply(ctx, opts.LockPath, opts.NoWait, opts.Stderr)
		if lockErr != nil {
			return result, fmt.Errorf("engine.Apply lock: %w", lockErr)
		}
		release = lk
	}
	defer func() { release() }()

	// Step 1: load existing state (or zero State if file absent).
	st, err := state.Load(opts.StatePath)
	if err != nil {
		return result, fmt.Errorf("engine.Apply state.Load: %w", err)
	}
	if st.Version == "" {
		st.Version = "1"
	}
	if st.Domains == nil {
		st.Domains = make(map[string]state.DomainState)
	}

	// Step 2: write InProgress marker before any pre-pass mutation.
	st.InProgress = state.InProgress{Verb: "apply", StartedAt: result.StartedAt}
	if err = st.Save(opts.StatePath); err != nil {
		return result, fmt.Errorf("engine.Apply state.Save(InProgress): %w", err)
	}

	// Step 3: pre-passes (gated by opts.Features).
	applyPrePasses(ctx, opts, result)

	// Step 4: per-domain loop.
	applyPerDomain(ctx, opts, st, result)

	// Step 5: finalize — set LastApply + clear InProgress IFF success.
	return applyFinalize(ctx, opts, st, result)
}

// prePassSpec is a data-driven row for one toggleable pre-pass.
// applyPrePasses iterates a slice of these and feeds each through
// runPrePass — replaces an earlier inlined if/else cascade that
// tripped both gocognit and dupl thresholds.
type prePassSpec struct {
	enabled bool
	name    string // PrePasses-map key, e.g. "foreign_tag"
	label   string // log prefix, e.g. "foreign-tag escape"
	fn      func() error
}

func applyPrePasses(ctx context.Context, opts ApplyOpts, result *ApplyResult) {
	if opts.AuditEnabled {
		findings := bear.AuditDomains(ctx, opts.Domains)
		bear.LogAuditFindings(findings, log.Printf)
	}
	// canonical-bootstrap wiring: build the tag→*Domain lookup
	// once so both fast-pass paths can write destination-canonical form
	// in a single bearcli call instead of relying on the next regen
	// cycle to restructure.
	domainsByTag := bear.DomainsByTag(opts.Domains)
	// dailyTagOn folds the catalog-driven "operator declared a daily
	// default tag" gate into the feature toggle. With AutoTagDefault on
	// but DailyDefaultTag empty the daily-default fast-pass is treated
	// as disabled — otherwise the spec calls ApplyDailyDefaultTag with
	// a nil domain (lookup miss on empty key) and stamps a spurious
	// Failed=1 for a feature the operator never opted into.
	//
	// placeholder-refresh stays gated on AutoTagDefault alone: it
	// iterates every domain with a non-empty QuickPlaceholderH1 and
	// has no dependency on the daily tag. Folding DailyDefaultTag
	// into its gate would silently disable placeholder refresh for
	// any catalog that declares `quick_placeholder_h1` on a domain
	// without also setting `[meta].daily_default_tag`.
	dailyTagOn := opts.Features.AutoTagDefault && opts.DailyDefaultTag != ""
	// dupl: keyed-fields layout makes the 6 entries look similar by
	// shape, but each closure dispatches a distinct pre-pass with
	// different inputs. The repetition is the cost of the readable
	// vertical structure; collapsing into a helper hides the dispatch
	// list and makes the order harder to audit.
	//nolint:dupl
	specs := []prePassSpec{
		{
			enabled: opts.Features.ForeignTagEscape,
			name:    "foreign_tag",
			label:   "foreign-tag escape",
			fn: func() error {
				_, err := bear.ApplyForeignTagEscape(ctx, domainsByTag)
				return err
			},
		},
		{
			enabled: dailyTagOn,
			name:    "auto_tag",
			label:   "auto-tag",
			fn: func() error {
				_, err := bear.ApplyDailyDefaultTag(ctx, domainsByTag[opts.DailyDefaultTag])
				return err
			},
		},
		{
			enabled: opts.Features.DomainBootstrap,
			name:    "domain_bootstrap",
			label:   "domain-bootstrap canonicalize",
			fn: func() error {
				_, err := bear.ApplyDomainBootstrap(ctx, domainsByTag)
				return err
			},
		},
		{
			enabled: opts.Features.AutoTagDefault,
			name:    "placeholder_refresh",
			label:   "placeholder refresh",
			fn: func() error {
				_, err := bear.ApplyPlaceholderRefresh(ctx, domainsByTag)
				return err
			},
		},
		{
			enabled: opts.Features.CrossDomainMoves,
			name:    "cross_domain",
			label:   "cross-domain moves",
			fn: func() error {
				return bear.ApplyCrossDomainMoves(ctx, opts.Domains, opts.Pins)
			},
		},
		{
			enabled: opts.Features.TimePromotion,
			name:    "time_promotion",
			label:   "time-promotion",
			fn: func() error {
				return bear.ApplyTimeBasedPromotion(ctx, opts.Domains, opts.Pins, opts.PromotionRules)
			},
		},
	}
	for _, spec := range specs {
		runPrePass(spec, result)
	}
	if opts.Pins != nil {
		if err := opts.Pins.Save(); err != nil {
			log.Printf("pins: save failed: %v (in-memory state preserved)", err)
		}
	}
	if opts.Features.DuplicateRegistry {
		registry, err := bear.BuildDuplicateRegistry(ctx, opts.Domains)
		if err != nil {
			log.Printf("duplicates: registry build failed: %v (continuing with plain wikilinks)", err)
			result.PrePasses["duplicate_registry"] = PrePassCounts{Failed: 1}
		} else {
			for _, domain := range opts.Domains {
				domain.Duplicates = registry
			}
			result.PrePasses["duplicate_registry"] = PrePassCounts{OK: 1}
		}
	}
}

// runPrePass invokes one pre-pass when enabled, logs failure with the
// canonical "(continuing per-domain regen)" suffix, and stamps a
// PrePassCounts entry under spec.name.
func runPrePass(spec prePassSpec, result *ApplyResult) {
	if !spec.enabled {
		return
	}
	if err := spec.fn(); err != nil {
		log.Printf("%s failed: %v (continuing per-domain regen)", spec.label, err)
		result.PrePasses[spec.name] = PrePassCounts{Failed: 1}
		return
	}
	result.PrePasses[spec.name] = PrePassCounts{OK: 1}
}

// applyPerDomain orchestrates the per-domain RunRegen pipeline using a
// per-umbrella errgroup dependency graph.
//
// Structure:
// - Top-level errgroup.WithContext(ctx) — each of the N umbrella
// families (one entry per nil-keyed standalone-group plus one per
// real umbrella) becomes one outer goroutine.
// - Inside each family goroutine: an inner errgroup runs all leaves
// of that family concurrently; after inner.Wait returns, the
// umbrella domain's own RunRegen executes on the same goroutine.
// - Independent families fan out in parallel; back-pressure on actual
// bearcli subprocesses lives in the bear.SetBearcliConcurrency
// semaphore, NOT at the errgroup layer.
//
// State serialization: state.State map writes + st.Save calls happen
// under stateMu. The mutex is held strictly around the map mutation +
// Save call — NEVER during RunRegen or bearcli I/O.
//
// Per-domain failures inside RunRegen are log-and-continue (same
// contract as the sequential predecessor); only ctx cancellation
// propagates as a non-nil errgroup return, which flips
// result.Interrupted=true on Wait.
func applyPerDomain(ctx context.Context, opts ApplyOpts, st *state.State, result *ApplyResult) {
	families := groupByUmbrella(opts.Domains)
	var stateMu sync.Mutex
	eg, gctx := errgroup.WithContext(ctx)
	for umbrella, leaves := range families {
		eg.Go(func() error {
			return runFamily(gctx, umbrella, leaves, opts, st, &stateMu, result)
		})
	}
	if waitErr := eg.Wait(); waitErr != nil {
		if errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
			result.Interrupted = true
		}
		// Non-cancel errors are not produced today — per-domain
		// failures inside RunRegen are log-and-continue, and
		// runDomainAndSave only returns ctx.Err. A future fatal
		// surface would need separate handling here.
	}
}

// runFamily orchestrates one umbrella family: its leaves run as siblings
// in an inner errgroup; once every leaf has returned, the umbrella
// domain's own RunRegen runs on the same outer-group goroutine. The
// nil-umbrella case is a standalone-domains bucket — there is no
// umbrella, only a flat slice of independent leaves to fan out.
func runFamily(
	ctx context.Context,
	umbrella *bear.Domain,
	leaves []*bear.Domain,
	opts ApplyOpts,
	st *state.State,
	stateMu *sync.Mutex,
	result *ApplyResult,
) error {
	if len(leaves) > 0 {
		inner, ictx := errgroup.WithContext(ctx)
		for _, leaf := range leaves {
			inner.Go(func() error {
				return runDomainAndSave(ictx, leaf, opts, st, stateMu, result)
			})
		}
		if err := inner.Wait(); err != nil {
			return err
		}
	}
	if umbrella != nil {
		return runDomainAndSave(ctx, umbrella, opts, st, stateMu, result)
	}
	return nil
}

// runDomainAndSave is the per-domain unit of work inside the errgroup
// orchestrator. The ctx.Err pre-check at entry guards against
// errgroup.Go not honoring cancellation pre-emptively (per the
// pkg.go.dev/golang.org/x/sync/errgroup docs).
//
// stateMu is held strictly around the map mutation + st.Save call.
// Holding it across RunRegen would serialize the orchestrator and
// defeat the entire wave; holding it across bearcli I/O would block
// every other writer for the duration of an I/O wait.
//
// Per-domain RunRegen failures are log-and-continue (legacy contract
// from the sequential applyPerDomain). The only path that returns a
// non-nil error is ctx cancellation, which propagates up the errgroup
// so eg.Wait can flip result.Interrupted.
func runDomainAndSave(
	ctx context.Context,
	d *bear.Domain,
	opts ApplyOpts,
	st *state.State,
	stateMu *sync.Mutex,
	result *ApplyResult,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	start := time.Now()
	d.RunRegen(ctx)
	elapsed := time.Since(start)
	if opts.DomainTimingHook != nil {
		opts.DomainTimingHook(d.Tag, elapsed)
	}
	// Compute content hash from fresh master + hubs read. RunRegen has
	// already written the master + hubs through bearcli; the snapshot
	// re-read captures the canonical post-write state. Returns "" on
	// read failure — caller preserves prior hash.
	hash := computeDomainHash(ctx, d)
	stateMu.Lock()
	defer stateMu.Unlock()
	if hash != "" {
		st.Domains[d.Tag] = state.DomainState{ContentHash: hash}
	}
	result.Domains[d.Tag] = DomainCounts{Unchanged: 1}
	if err := st.Save(opts.StatePath); err != nil {
		log.Printf("apply: state.Save(domain=%s) failed: %v (continuing)", d.Tag, err)
	}
	return nil
}

// groupByUmbrella partitions opts.Domains by family. The key is the
// umbrella domain pointer; the value slice is its leaves. Domains
// without a parent and without children become entries under the nil
// key — applyPerDomain's outer loop treats nil-umbrella as "no umbrella
// step, just fan out the leaves".
//
// Identification:
// - leaf — d.ParentMaster != ""
// - umbrella — some other domain in the same slice satisfies
// child.ParentMaster == d.IndexTitle
// - standalone — d.ParentMaster == "" AND no domain references
// d.IndexTitle as its ParentMaster
//
// Iteration order of the returned map is intentionally non-deterministic
// (standard Go map semantics). Independent families have no ordering
// requirement.
//
// Orphan leaves — a leaf whose ParentMaster does not match any
// umbrella present in the slice — are placed in the nil-key bucket so
// they still run; this is defensive for partial-domain subsets passed
// during testing.
func groupByUmbrella(domains []*bear.Domain) map[*bear.Domain][]*bear.Domain {
	families := make(map[*bear.Domain][]*bear.Domain)
	// First pass: identify umbrellas by IndexTitle.
	umbrellasByTitle := identifyUmbrellas(domains)
	// Second pass: place each domain.
	for _, d := range domains {
		placeDomainInFamily(d, umbrellasByTitle, families)
	}
	return families
}

// identifyUmbrellas returns the set of umbrella domains, keyed by
// IndexTitle so the placement pass can look up an umbrella from a
// child's ParentMaster value (which equals the umbrella's IndexTitle
// per bear/factory.go::NewUmbrellaDomain).
func identifyUmbrellas(domains []*bear.Domain) map[string]*bear.Domain {
	umbrellas := make(map[string]*bear.Domain)
	for _, d := range domains {
		for _, child := range domains {
			if child != d && child.ParentMaster == d.IndexTitle {
				umbrellas[d.IndexTitle] = d
				break
			}
		}
	}
	return umbrellas
}

// placeDomainInFamily appends d into the right bucket of families.
// Extracted from groupByUmbrella so the per-domain branch logic stays
// under the gocognit budget.
func placeDomainInFamily(
	d *bear.Domain,
	umbrellasByTitle map[string]*bear.Domain,
	families map[*bear.Domain][]*bear.Domain,
) {
	if d.ParentMaster != "" {
		if u, ok := umbrellasByTitle[d.ParentMaster]; ok {
			families[u] = append(families[u], d)
			return
		}
		// Orphan leaf — keep it runnable under the nil family.
		families[nil] = append(families[nil], d)
		return
	}
	if _, isUmbrella := umbrellasByTitle[d.IndexTitle]; isUmbrella {
		if _, exists := families[d]; !exists {
			families[d] = nil
		}
		return
	}
	families[nil] = append(families[nil], d)
}

func applyFinalize(ctx context.Context, opts ApplyOpts, st *state.State, result *ApplyResult) (*ApplyResult, error) {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Interrupted = true
		// Leave LastApply at prior value; preserve InProgress as resume marker.
		// Save once more so on-disk state matches in-memory result.
		if saveErr := st.Save(opts.StatePath); saveErr != nil {
			log.Printf("apply: final state.Save (interrupted) failed: %v", saveErr)
		}
		if opts.WithMetrics {
			result.Metrics = bear.BearcliMetricsSnapshot()
		}
		return result, nil
	}
	result.CompletedAt = time.Now().UTC()
	st.LastApply = result.CompletedAt
	st.InProgress = state.InProgress{} // clear
	if err := st.Save(opts.StatePath); err != nil {
		return result, fmt.Errorf("engine.Apply state.Save(complete): %w", err)
	}
	if opts.WithMetrics {
		result.Metrics = bear.BearcliMetricsSnapshot()
	}
	return result, nil
}

// computeDomainHash reads the freshest master + hub bytes from Bear
// for one domain and returns sha256(strip(master) || NUL ||
// sorted-by-title strip(hubs[i])). Returns "" on read failure
// (logged but non-fatal — caller preserves prior hash).
func computeDomainHash(ctx context.Context, domain *bear.Domain) string {
	master, hubs, err := snapshotDomainContent(ctx, domain)
	if err != nil {
		log.Printf("apply: snapshot(%s) failed: %v (hash unchanged)", domain.Tag, err)
		return ""
	}
	return ComputeContentHash(master, hubs)
}

// snapshotDomainContent fetches the post-RunRegen master + hub bytes
// for one domain via the exported bear.FetchMasterContent /
// bear.FetchHubContents wrappers (which in turn call the bearcli
// boundary inside package bear). Stripped of the [Нова нотатка]
// new-note link drift before return — caller can hash directly.
//
// Returns ("", nil, nil) for domains without a master note yet
// (transient state during initial setup); caller treats this as
// "skip the hash update, preserve prior" rather than overwriting
// with "".
func snapshotDomainContent(
	ctx context.Context,
	domain *bear.Domain,
) (master string, hubs map[string]string, err error) {
	master, mErr := bear.FetchMasterContent(ctx, domain)
	if mErr != nil {
		return "", nil, fmt.Errorf("snapshotDomainContent(%s) master: %w", domain.Tag, mErr)
	}
	hubs, hErr := bear.FetchHubContents(ctx, domain)
	if hErr != nil {
		return "", nil, fmt.Errorf("snapshotDomainContent(%s) hubs: %w", domain.Tag, hErr)
	}
	return master, hubs, nil
}

// ComputeContentHash returns sha256(strip(master) || NUL || sorted-by-title strip(hub_i)).
// Inputs are already stripped of new-note-link drift by
// snapshotDomainContent — this function is pure: same input, same
// output.
//
// Exported (rather than relying on a `computeContentHash` + in-package
// `export_test.go` test seam) because the project's test-location
// convention places external tests at `tests/bear/engine/`, a different
// directory from the package source — which means an in-package
// `_test.go` file cannot bridge unexported symbols across the
// directory gap. Exporting is the pragmatic resolution.
func ComputeContentHash(master string, hubs map[string]string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(master))
	titles := make([]string, 0, len(hubs))
	for t := range hubs {
		titles = append(titles, t)
	}
	sort.Strings(titles)
	for _, t := range titles {
		_, _ = h.Write([]byte{0}) // NUL separator
		_, _ = h.Write([]byte(hubs[t]))
	}
	return hex.EncodeToString(h.Sum(nil))
}
