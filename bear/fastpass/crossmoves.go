package fastpass

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// IsFlatList reports whether the domain renders its master as a single
// alphabetical list (no Tier-2 hubs, no master-table bidirectional flow).
// Cross-domain moves between flat-list domains are interpreted as tag
// swaps — drag a `[[Title]]` bullet from one flat-list master into
// another and the daemon rewrites the atomic's canonical tag-line.
//
// Umbrella domains (SkipAtomicsPass=true) ARE flat-list-shaped on the
// surface but their atoms are owned by child sub-domains — including them
// here would let bearcli's hierarchical `--tag llm` query pull `#llm/*`
// atoms into a cross-domain move from `#llm` to `#llm/rules`, with the
// HasPrefix match in rewriteCanonicalTag flipping every tick. So the
// caller must skip them; see ApplyCrossDomainMoves.
//
// Hub-routed and grouped-vertical domains carry richer bucket semantics that
// don't translate cleanly to flat-list targets, so cross-domain moves
// involving them are deferred to a future iteration. For now:
// flat-list ↔ flat-list only — see domain.IsFlatList in
// bear/domain/methods.go for the predicate.

// flatListMasterClaim records the destination domain for each atomic
// referenced from any flat-list master's bullet list. Source-domain
// information is filled in later when the per-atom rewrite decides
// whether the claim crosses a domain boundary.
type flatListMasterClaim struct {
	target *domain.Domain
}

// BuildFlatListMasterClaims scans every flat-list domain's master and
// returns a registry mapping each bullet identifier (title or
// bear://x-callback ID) to the domain whose master claims it. Hub-routed
// and grouped-vertical masters are not consulted — their bidirectional flow is
// intra-domain only.
//
// When two flat-list masters claim the same identifier the last domain
// in the input slice wins. The collision is rare in practice (it
// requires the user to leave the same bullet in two different lists)
// and the user can resolve by removing it from the wrong one.
func BuildFlatListMasterClaims(ctx context.Context, domains []*domain.Domain) (map[string]flatListMasterClaim, error) {
	claims := make(map[string]flatListMasterClaim)
	for _, d := range domains {
		if !d.IsFlatList() || d.SkipAtomicsPass {
			continue
		}
		content, err := readDomainMaster(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("BuildFlatListMasterClaims(%s): %w", d.Tag, err)
		}
		if content == "" {
			continue
		}
		for _, ident := range domain.ParseHubBulletIdentifiers(content) {
			claims[ident] = flatListMasterClaim{target: d}
		}
	}
	return claims, nil
}

// readDomainMaster fetches the current content of a domain's master note
// or returns "" when the master doesn't exist yet (fresh domain). Errors
// from bearcli surface so the caller can decide whether to abort the
// cycle or fall back to per-domain processing.
func readDomainMaster(ctx context.Context, d *domain.Domain) (string, error) {
	idxID, err := d.FindIndexID(ctx)
	if err != nil {
		return "", err
	}
	if idxID == "" {
		return "", nil
	}
	out, err := bearcli.Run(ctx, []string{"cat", idxID, bearcli.FlagFormat, bearcli.FormatJSON}, "")
	if err != nil {
		return "", err
	}
	var existing domain.Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return "", fmt.Errorf("parse master %q: %w", d.IndexTitle, err)
	}
	return existing.Content, nil
}

// ApplyCrossDomainMovesResult reads every flat-list domain's master,
// detects atomics whose current tag puts them in domain A but whose
// bullet now lives in domain B's master, and rewrites those atomics'
// canonical tag-lines from `#A` to `#B`. Bear's tag-tracking metadata
// follows the body tag automatically — the next per-domain regen sees
// the atomic in its new domain.
//
// Runs once before the per-domain regen loop in runAllRegens. Failures
// are non-fatal: the orchestrator logs and continues with per-domain
// regens. Returns structured per-note counts for apply recaps and verify
// idempotency checks.
func ApplyCrossDomainMovesResult(
	ctx context.Context,
	domains []*domain.Domain,
	pins *domain.PinRegistry,
) (PassResult, error) {
	claims, err := BuildFlatListMasterClaims(ctx, domains)
	if err != nil {
		return PassResult{}, err
	}
	if len(claims) == 0 {
		return PassResult{}, nil
	}
	result := PassResult{}
	for _, source := range domains {
		if err = domain.CheckCtx(ctx); err != nil {
			return result, err
		}
		if !source.IsFlatList() || source.SkipAtomicsPass {
			continue
		}
		if err = applyCrossDomainMovesFor(ctx, source, claims, pins, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

// applyCrossDomainMovesFor handles one source domain: walks its atomics,
// finds those claimed by a different flat-list domain's master, rewrites
// their canonical tag-line.
func applyCrossDomainMovesFor(
	ctx context.Context,
	source *domain.Domain,
	claims map[string]flatListMasterClaim,
	pins *domain.PinRegistry,
	result *PassResult,
) error {
	notes, err := bearcli.ListNotesForTag(ctx, source.Tag)
	if err != nil {
		result.Failed++
		return fmt.Errorf("ApplyCrossDomainMoves(%s) list: %w", source.Tag, err)
	}
	for _, atom := range notes {
		if err = domain.CheckCtx(ctx); err != nil {
			return err
		}
		if domain.IsAuxNote(source, atom) {
			continue
		}
		target, ok := lookupClaim(claims, atom)
		if !ok || target == source {
			continue
		}
		changed, rewriteErr := rewriteAtomTag(ctx, atom, source, target, pins)
		if rewriteErr != nil {
			result.Failed++
			return rewriteErr
		}
		if changed {
			result.Changed++
		}
	}
	return nil
}

// lookupClaim resolves a claim by note ID first (URL form), then by
// title. Returns the target domain when claimed.
func lookupClaim(claims map[string]flatListMasterClaim, atom domain.Note) (*domain.Domain, bool) {
	if claim, ok := claims[atom.ID]; ok {
		return claim.target, true
	}
	if claim, ok := claims[atom.Title]; ok {
		return claim.target, true
	}
	return nil, false
}

// rewriteAtomTag rewrites a single atomic's canonical tag-line from the
// source domain to the target domain and overwrites the note in Bear.
// The atomic's H1 and body are preserved; only the tag-line changes.
//
// Idempotency: rewriteCanonicalTag returns (content, false) when no
// source tag-line was found — the bool short-circuits the no-op gate.
// equalIgnoringNewNoteLinkStrict still runs for the "tag found but URL
// drift only" case.
func rewriteAtomTag(
	ctx context.Context,
	atom domain.Note,
	source, target *domain.Domain,
	pins *domain.PinRegistry,
) (bool, error) {
	newContent, rewrote := rewriteCanonicalTag(atom.Content, source.CanonicalTag, target)
	if !rewrote || domain.EqualIgnoringNewNoteLinkStrict(newContent, atom.Content) {
		return false, nil
	}
	if err := bearcli.OverwriteWithRetry(ctx, atom.ID, newContent); err != nil {
		return false, fmt.Errorf("ApplyCrossDomainMoves(%s→%s) %q: %w", source.Tag, target.Tag, atom.Title, err)
	}
	pins.RecordPin(atom.ID, target.Tag)
	target.Logf("cross-domain move: %s ← %s", atom.Title, source.Tag)
	return true, nil
}

// rewriteCanonicalTag replaces the source domain's canonical tag-line
// with the target domain's master-pointing form. Preserves H1 and body.
// Returns (newContent, true) when a tag-line was rewritten; returns
// (content, false) when no source tag-line was found (no-op gate).
//
// The new line carries the bootstrap-form new-note decoration via
// domain.NewNoteURLFromDomain(target).Emit — same SSOT path every other
// emit call site uses (Task 3). Without this, every
// cross-domain move triggered an extra write per tick.
//
// Source line shape (anything starting with the source canonical tag):
//
//	#<source-tag> | [[…]]
//
// New line:
//
//	#<target-tag> | [[<target-IndexTitle>]] | [Нова нотатка](bear://...)
func rewriteCanonicalTag(content, sourceTag string, target *domain.Domain) (string, bool) {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !startsWithTagToken(trimmed, sourceTag) {
			continue
		}
		lines[index] = fmt.Sprintf("%s | [[%s]]%s",
			target.CanonicalTag, target.IndexTitle, domain.NewNoteURLFromDomain(target).Emit())
		return strings.Join(lines, "\n"), true
	}
	return content, false
}

// RewriteCanonicalTagForTest exposes rewriteCanonicalTag to tests/bear.
func RewriteCanonicalTagForTest(content, sourceTag string, target *domain.Domain) (string, bool) {
	return rewriteCanonicalTag(content, sourceTag, target)
}

// startsWithTagToken reports whether `line` begins with `tag` followed by a
// token boundary (whitespace, `|`, `\n`, or end-of-string). Plain HasPrefix
// would match `#llm` against `#llm/agents`, allowing the rewrite to flip
// already-sub-tagged atoms back to the umbrella canonical every tick.
func startsWithTagToken(line, tag string) bool {
	if !strings.HasPrefix(line, tag) {
		return false
	}
	rest := line[len(tag):]
	if rest == "" {
		return true
	}
	next := rest[0]
	return next == ' ' || next == '\t' || next == '|' || next == '\n'
}
