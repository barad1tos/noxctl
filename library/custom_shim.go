package library

import (
	"fmt"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/custom"
)

// buildCustomHubRouted is the shared builder for library/* domains
// whose renderer body lives in bear/custom/<name>.go (D-06).
// It produces a hub-routed Domain via NewHubRoutedDomain (preserving
// every primitive field the round-trip test asserts) and stamps the
// custom renderer through bear/custom.Lookup.
//
// Lookup failure is a programmer bug — bear/custom/<name>.go's init
// registers the name unconditionally — so we panic loudly with the
// caller's tag context rather than ship a half-wired Domain.
//
// The helper exists so per-domain shim files (lyrics.go, quotes.go)
// stay below the dupl 30-token threshold while still carrying their
// original doc comments and var-decl shape.
func buildCustomHubRouted(tag, indexTitle, unknownBucket, hubH2Prefix, rendererName string) *bear.Domain {
	d := bear.NewHubRoutedDomain(tag, indexTitle, unknownBucket, hubH2Prefix, nil)
	c, err := custom.Lookup(rendererName)
	if err != nil {
		panic(fmt.Sprintf("%s: %v", tag, err))
	}
	c.Apply(d)
	return d
}
