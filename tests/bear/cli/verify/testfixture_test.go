// Package verify_test — shared test fixtures.
//
// `verify.Run` short-circuits on catalog-load failure (StatusError →
// early return). To isolate the daemon-log / plan-parity / apply-
// idempotency checks per-test, every scenario needs (a) a valid TOML
// catalog file and (b) a `domain.BearcliBackend` stamped on the context
// that returns benign responses for every bearcli call the checks
// trigger. Both helpers live here so the per-check tests stay focused
// on their actual contract.
package verify_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
	"github.com/barad1tos/noxctl/bear/domain"
)

// minimalCatalogTOML is the smallest valid `noxctl.toml` that loads
// cleanly through `bear/config.Load`. Single flat-list domain — no
// buckets, no umbrellas, no custom renderers. Plan / apply / verify
// all accept it as a real catalog without exercising the heavier
// blueprints.
const minimalCatalogTOML = `[meta]
  version = "1"
  locale = "uk"

[[domain]]
  tag = "claude/sessions"
  index_title = "✱ Test"
  blueprint = "flat-list"
`

// writeMinimalCatalog drops minimalCatalogTOML into t.TempDir() and
// returns the path. Caller passes it as `Options.ConfigPath`.
func writeMinimalCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "noxctl.toml")
	if err := os.WriteFile(path, []byte(minimalCatalogTOML), 0o600); err != nil {
		t.Fatalf("write minimal catalog: %v", err)
	}
	return path
}

// benignBearcliBackend is a `domain.BearcliBackend` impl that returns
// shape-valid empty responses for every bearcli command the verify
// checks issue. Unknown verbs error out so a future engine call site
// (new "tag" / "replace" / "delete" command) surfaces in tests
// instead of being silently absorbed.
type benignBearcliBackend struct {
	mu            sync.Mutex
	masterCreated bool
	masterContent string
}

func (b *benignBearcliBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) == 0 {
		return []byte("{}"), nil
	}
	switch args[0] {
	case "list":
		if listFields(args) == "id,title" {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.masterCreated {
				return []byte(`[{"id":"master-1","title":"✱ Test"}]`), nil
			}
		}
		return []byte("[]"), nil
	case "cat":
		b.mu.Lock()
		defer b.mu.Unlock()
		return fmt.Appendf(nil, `{"id":"master-1","title":"✱ Test","content":%q}`, b.masterContent), nil
	case "show":
		// optimistic-concurrency hash + empty body. Empty body
		// means "no master / no content" — engine.Plan's
		// computeDomainDelta then sees `currentMaster == ""` and
		// emits a Create diff, but tests on daemon-log don't care.
		return []byte(`{"hash":"deadbeef","content":""}`), nil
	case "create":
		b.mu.Lock()
		b.masterCreated = true
		b.masterContent = stdin
		b.mu.Unlock()
		return []byte(`{"id":"master-1","title":"✱ Test"}`), nil
	case "overwrite":
		b.mu.Lock()
		b.masterContent = stdin
		b.mu.Unlock()
		return []byte(`{"ok":true}`), nil
	}
	// Fail loud on unknown verbs so a future engine call site
	// (e.g. new "tag" / "replace" / "delete" command) surfaces in
	// tests instead of being silently swallowed as "{}".
	return nil, fmt.Errorf("benignBearcliBackend: unhandled bearcli args %v", args)
}

func listFields(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--fields" {
			return args[i+1]
		}
	}
	return ""
}

// ctxWithBenignBackend stamps the benign backend onto t.Context()
// so verify.Run's bearcli calls become hermetic no-ops. Returns the
// context the test passes to verify.Run.
func ctxWithBenignBackend(t *testing.T) context.Context {
	t.Helper()
	return domain.ContextWithBackend(t.Context(), &benignBearcliBackend{})
}

// buildAllFourStatusesResult returns a Result with exactly one check
// per Status value plus a Summary with one of each. Used by render
// tests (text glyph surface + JSON summary counters) so the fixture
// stays in one place and `dupl` doesn't trip on the parallel setup.
func buildAllFourStatusesResult() *verify.Result {
	return &verify.Result{
		SchemaVersion: 1,
		Checks: []verify.Check{
			{Name: "plan-parity", Status: verify.StatusPass, Message: "no drift"},
			{Name: "daemon-log", Status: verify.StatusFail, Message: "2 warning(s)"},
			{Name: "apply-idempotency", Status: verify.StatusSkipped, Message: "opt-in via --with-apply"},
			{Name: "catalog-load", Status: verify.StatusError, Message: "config missing"},
		},
		Summary: verify.Summary{Pass: 1, Fail: 1, Skipped: 1, Error: 1},
	}
}
