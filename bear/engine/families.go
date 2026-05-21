package engine

// Per-domain + per-umbrella iteration — how Apply walks the catalog,
// groups leaves under their umbrellas, runs RunRegen on each, and
// persists state.json incrementally.

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/state"
)

// applyPerDomain orchestrates the per-domain RunRegen pipeline via a
// per-umbrella errgroup dependency graph. The outer errgroup fans
// out one goroutine per umbrella family (one entry per nil-keyed
// standalone group plus one per real umbrella); each family
// goroutine runs its leaves concurrently in an inner errgroup, then
// runs the umbrella's own RunRegen after inner.Wait returns.
// Independent families fan out in parallel; back-pressure on actual
// bearcli subprocesses lives in the bearcli.SetConcurrency
// semaphore, NOT at the errgroup layer.
//
// state.State map writes and st.Save calls happen under stateMu,
// held strictly around the mutation and Save call — never during
// RunRegen or bearcli I/O.
//
// Per-domain RunRegen failures are log-and-continue (same contract
// as the sequential predecessor); only ctx cancellation propagates
// as a non-nil errgroup return, which flips
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
	umbrella *domain.Domain,
	leaves []*domain.Domain,
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
	d *domain.Domain,
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
func groupByUmbrella(domains []*domain.Domain) map[*domain.Domain][]*domain.Domain {
	families := make(map[*domain.Domain][]*domain.Domain)
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
func identifyUmbrellas(domains []*domain.Domain) map[string]*domain.Domain {
	umbrellas := make(map[string]*domain.Domain)
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
	d *domain.Domain,
	umbrellasByTitle map[string]*domain.Domain,
	families map[*domain.Domain][]*domain.Domain,
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
