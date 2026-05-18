package library

import "github.com/barad1tos/noxctl/bear"

// ProseDomain handles the library/prose tag with a flat 2-tier model: each
// atomic prose piece backlinks directly at the master `✱ Проза`, and the
// master renders vertical `## Мої (N)` / `## Чужі (N)` sections with bullet
// lists straight to those atomics. No per-bucket Tier-2 hubs.
//
// Atomic canonical header:
//
//	#library/prose | [[✱ Проза]] | <Bucket>
//
// where Bucket is one of {Мої, Чужі}. Atomics that lack a third segment fall
// back to UnknownBucket="Мої" — the daemon then writes that bucket back into
// the canonical header on the next regen, so each note round-trips cleanly.
//
// Empty buckets are skipped at master level — when "Чужі" has no atomics
// the section disappears; user adds an atom and it reappears next regen.
var ProseDomain = bear.NewGroupedVerticalFlatDomain(
	"library/prose",
	bear.T("library.prose.index"),
	bear.T("library.prose.bucket.own"),
	[]string{
		bear.T("library.prose.bucket.own"),
		bear.T("library.prose.bucket.others"),
	},
)
