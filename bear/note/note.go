// Package note carries the Note value type — the JSON shape bearcli
// returns for every read verb (list / show / search). Lives in its
// own leaf package so every other layer (bearcli I/O, render, audit,
// fastpass) can import it without creating cycles through bear/.
//
// Pure data: no methods, no I/O, no globals. Decoders fill the
// fields directly from bearcli output; producers never marshal a
// Note back out.
package note

import "time"

// Note is the bearcli JSON shape every read verb returns. Only ID,
// Title, and Content are populated by every callsite; Hash arrives
// from `show`, Tags + Created arrive only when the caller requested
// them via `--fields`. Sub-tag-preserving blueprints consult Tags
// via BucketFromSubTag when the canonical-header line is absent —
// sticky-creation: a note created via Bear's sidebar with
// `#development/noxctl` stays in `noxctl`, not the UnknownBucket
// fallback.
//
// All fields use plain JSON tags without `omitempty` because Note
// is never marshaled back to bearcli; the asymmetry is intentional.
type Note struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Content string    `json:"content"`
	Hash    string    `json:"hash"`
	Tags    []string  `json:"tags"`
	Created time.Time `json:"created"`
}
