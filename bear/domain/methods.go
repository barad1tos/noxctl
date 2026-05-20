package domain

// Domain methods + the runBearcli shim. Split from domain.go to keep
// type declarations in one file and per-Domain behavior in another.
// Every method here takes a *Domain receiver; pure helpers that just
// pass a *Domain as argument live in their topic-specific files
// (sections.go, factory.go, etc.).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// TagSuffix returns the part of d.Tag after the last "/", e.g. "poetry" for
// "library/poetry". Used to label log lines and pilot env-vars per domain.
func (d *Domain) TagSuffix() string {
	suffix := d.Tag
	if slash := strings.LastIndex(suffix, "/"); slash >= 0 {
		suffix = suffix[slash+1:]
	}
	return suffix
}

// LogPrefix returns "regen[<tag-suffix>]" for log lines, e.g. "regen[poetry]".
// Disambiguates concurrent multi-domain regens in the daemon log stream.
func (d *Domain) LogPrefix() string {
	return "regen[" + d.TagSuffix() + "]"
}

// Logf writes a log line prefixed with the domain's LogPrefix. Centralizes the
// prefix concern so individual call sites can't accidentally drop it.
func (d *Domain) Logf(format string, args ...any) {
	log.Printf(d.LogPrefix()+": "+format, args...)
}

// Validate returns a non-nil error if the Domain is missing required fields.
// Called by the daemon at startup so misconfiguration surfaces immediately
// instead of as a deferred nil-pointer-dereference deep in the first regen.
func (d *Domain) Validate() error {
	if d.ValidationError != nil {
		return d.ValidationError
	}
	if d.Tag == "" {
		return errors.New("Domain.Tag is required")
	}
	if d.CanonicalTag == "" {
		return fmt.Errorf("domain %q: CanonicalTag is required", d.Tag)
	}
	if d.IndexTitle == "" {
		return fmt.Errorf("domain %q: IndexTitle is required", d.Tag)
	}
	if d.ParseMeta == nil {
		return fmt.Errorf("domain %q: ParseMeta callback is required", d.Tag)
	}
	if d.RenderMaster == nil {
		return fmt.Errorf("domain %q: RenderMaster callback is required", d.Tag)
	}
	if d.SkipAtomicsPass && d.DefaultChild == "" {
		return fmt.Errorf("domain %q: DefaultChild is required for umbrella (SkipAtomicsPass=true) domains", d.Tag)
	}
	return nil
}

// newNoteRawTag returns the rawTag value to embed in the new-note
// bootstrap URL's inner `tags=` parameter. For umbrella domains
// (SkipAtomicsPass=true) it returns DefaultChild so clicks on the
// umbrella master land in a leaf-domain-tagged note that the leaf's
// regen pipeline can canonicalize. For leaf domains it returns Tag —
// the existing behavior. Centralizes the choice so newNoteBootstrapLink
// doesn't branch on SkipAtomicsPass.
func (d *Domain) newNoteRawTag() string {
	if d.SkipAtomicsPass && d.DefaultChild != "" {
		return d.DefaultChild
	}
	return d.Tag
}

// ResolveURLDomain returns the leaf domain whose configuration drives
// URL emission. Umbrella domains (SkipAtomicsPass=true) recurse through
// their resolved DefaultChild so the embedded body in bootstrap URLs
// reflects the leaf's tag, backlink, and placeholder H1 — not the
// umbrella's internal "_umbrella" placeholder. Leaves return self.
func (d *Domain) ResolveURLDomain() *Domain {
	if d.DefaultChildDomain != nil {
		return d.DefaultChildDomain.ResolveURLDomain()
	}
	return d
}

// EffectiveQuickPlaceholderH1 returns d.QuickPlaceholderH1 when set,
// otherwise the package default DefaultQuickPlaceholderH1. Centralizing
// the fallback here keeps empty-string-means-default semantics out of
// every caller. Co-located with newNoteRawTag / ResolveURLDomain because
// it's a *Domain config accessor, not an H1 emission primitive.
func (d *Domain) EffectiveQuickPlaceholderH1() string {
	if d.QuickPlaceholderH1 == "" {
		return DefaultQuickPlaceholderH1
	}
	return d.QuickPlaceholderH1
}

// HubTitle maps bucket → Tier-2 hub note title. Defaults to identity
// (bucket == title) for legacy hub-routed domains. Sub-tag preserving hubs
// override to return `<top> · <bucket>`.
func (d *Domain) HubTitle(bucket string) string {
	cb := d.HubTitleFor
	if cb == nil {
		return bucket
	}
	return cb(d, bucket)
}

// bucketFromHubTitle inverts HubTitle. Returns "" when the title doesn't
// belong to this domain (computeHubOverrides treats "" as "skip"). The
// callback path lets sub-tag preserving domains strip a `<top> · ` prefix
// before matching against bucket-keyed groups.
func (d *Domain) bucketFromHubTitle(title string) string {
	if cb := d.BucketFromHubTitle; cb != nil {
		return cb(d, title)
	}
	return title
}

// ResolveCanonicalTag resolves the per-atomic canonical tag-line. Defaults to
// d.CanonicalTag when no callback is wired (existing flat-table / hub-routed
// behavior). Domains that preserve sub-tags (grouped-vertical,
// hub-routed-with-subtag) wire CanonicalTagFor to return `#<top>/<bucket>`
// so each atomic carries its sub-tag in the tag-line.
func (d *Domain) ResolveCanonicalTag(bucket string) string {
	if d.CanonicalTagFor != nil {
		return d.CanonicalTagFor(d, bucket)
	}
	return d.CanonicalTag
}

// backlinkFor returns the canonical-header backlink target for a given bucket.
// Defaults to "[[bucket]]" (poetry: per-author hub link). Domains with no
// per-author hubs (aphorisms) override to always link to the master.
func (d *Domain) backlinkFor(bucket string) string {
	if d.BacklinkFor != nil {
		return d.BacklinkFor(d, bucket)
	}
	return "[[" + bucket + "]]"
}

// sectionFor returns the section path the canonicalized atomic should carry.
// Defaults to whatever ParseMeta extracted (poetry's sub-genre path). Aphorisms
// overrides because the section IS the bucket (category).
func (d *Domain) sectionFor(bucket string, p AtomicParts) string {
	if d.SectionFor != nil {
		return d.SectionFor(d, bucket, p)
	}
	return p.Section
}

// skipNote returns true for notes that should not be grouped as atomics
// (master, Tier-2 hubs, legacy [Index]/✱-prefixed system notes).
func (d *Domain) skipNote(n Note) bool {
	if d.SkipNote != nil {
		return d.SkipNote(d, n)
	}
	if n.Title == d.IndexTitle {
		return true
	}
	if strings.HasPrefix(n.Title, "[Index]") || strings.HasPrefix(n.Title, "✱ ") {
		return true
	}
	if d.isHubNote(n) {
		return true
	}
	return false
}

// runBearcli is the in-package shim over bearcli.Run. The real
// implementation and the kindFromArgs classifier moved to
// bear/bearcli during PR-H2; this shim exists so existing call sites
// in bear/ (Domain methods like listNotes, findNoteByTitle,
// upsertHub) keep compiling against the lowercase package-internal
// name without a rename churn through every caller.
func runBearcli(ctx context.Context, args []string, stdin string) ([]byte, error) {
	return bearcli.Run(ctx, args, stdin)
}
