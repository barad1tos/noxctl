// Package it holds per-tag Bear domain configurations for the `it/*` tag
// family — three domains aligned with how the user actually navigates IT
// notes: domains (broad professional areas), vendors (Apple, AWS,
// MikroTik, Windows), technologies (Kubernetes, Linux, Python, generic
// articles). Mirrors the layout of `github.com/barad1tos/noxctl/library` and
// `github.com/barad1tos/noxctl/llm`: one `var FooDomain = &bear.Domain{...}`
// literal per file. Shared abstractions live in `github.com/barad1tos/noxctl/bear`.
package it

import "github.com/barad1tos/noxctl/bear"

// DomainsDomain is the placeholder for `it/domains` — broad professional
// areas (networking, security, IaC, monitoring, etc.) that don't fit
// vendor or technology buckets cleanly. Currently empty; the user
// populates it as IT knowledge accumulates.
//
// Renders as a flat alphabetical list — at low scale a per-bucket Tier-2
// hub layer adds nothing. Once the corpus crosses ~15 atoms with stable
// sub-categories the domain can be upgraded to a flat-table master like
// vendors / technologies without changing the tag shape.
var DomainsDomain = bear.NewFlatListDomain("it/domains", bear.T("it.domains.index"))
