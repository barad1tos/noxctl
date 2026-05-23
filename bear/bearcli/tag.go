package bearcli

// tag.go owns the Bear-tag mutation verbs. Today's surface is a single
// thin wrapper over `bearcli tags add <noteID> <tag>` — additive tag
// application that preserves every existing tag on the note. The Bear
// CLI itself is idempotent at the tag-presence level: re-running with
// the same tag is a no-op on the target note. Callers still SHOULD
// skip already-tagged atoms upstream (see audit.aggregateOrphanFamilies
// idempotency contract) so the pool metrics do not inflate with no-op
// traffic.
//
// The first production caller is audit.ApplyOrphanFamilies for the
// `bearcli tags add <id> orphans` triage workflow. Future per-domain
// tag mutations slot in here as new wrappers so the kindFromArgs
// classification stays in one grep-detectable place (client.go).

import (
	"context"
	"fmt"
)

// AddTag invokes `bearcli tags add <noteID> <tag>` to attach an
// additional tag to a Bear note. Bear preserves all existing tags on
// the target note; AddTag does NOT replace the tag set.
//
// The tag argument MAY be supplied with or without the leading `#`
// (Bear's `tags add` strips surrounding `#` and whitespace per its
// documented contract). Pass the bare form (`"orphans"`) for clarity.
func AddTag(ctx context.Context, noteID, tag string) error {
	if _, err := Run(ctx, []string{"tags", "add", noteID, tag}, ""); err != nil {
		return fmt.Errorf("AddTag(%s, %s): %w", noteID, tag, err)
	}
	return nil
}
