package bearcli

// tag.go owns the Bear-tag mutation verbs. Today's surface is a single
// thin wrapper over `bearcli tag <noteID> <tag>` — additive tag
// application that preserves every existing tag on the note. The Bear
// CLI itself is idempotent at the tag-presence level: re-running with
// the same tag is a no-op on the target note. Callers still SHOULD
// skip already-tagged atoms upstream (see audit.aggregateOrphanFamilies
// idempotency contract) so the pool metrics do not inflate with no-op
// traffic.
//
// The first production caller is audit.ApplyOrphanFamilies (Phase 13)
// for the `bearcli tag <id> orphans` triage workflow. Future
// per-domain tag mutations slot in here as new wrappers so the
// kindFromArgs classification stays in one grep-detectable place
// (client.go).

import (
	"context"
	"fmt"
)

// AddTag invokes `bearcli tag <noteID> <tag>` to attach an additional
// tag to a Bear note. Bear preserves all existing tags on the target
// note; AddTag does NOT replace the tag set.
//
// The tag argument MUST be supplied WITHOUT the leading `#` (Bear's
// CLI convention — the tool prefixes internally). Passing `"#orphans"`
// instead of `"orphans"` would surface as a literal `##orphans` tag on
// the note, which is the wrong outcome and silently breaks idempotency.
func AddTag(ctx context.Context, noteID, tag string) error {
	if _, err := Run(ctx, []string{"tag", noteID, tag}, ""); err != nil {
		return fmt.Errorf("AddTag(%s, %s): %w", noteID, tag, err)
	}
	return nil
}
