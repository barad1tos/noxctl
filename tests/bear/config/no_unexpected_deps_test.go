// Package config_test guards the runtime dependency budget.
//
// Direct runtime deps:
//   - github.com/fsnotify/fsnotify — FSEvents watcher.
//   - github.com/BurntSushi/toml — bear/config/loader.go and
//     tests/bear/config/* round-trip decode.
//   - github.com/spf13/cobra — cmd/noxctl/root.go and friends wire
//     the Cobra subcommand surface.
//   - golang.org/x/sys — anchored via `tools.go` (//go:build tools)
//     so the macOS flock surface imported by `bear/lock.go` lands
//     without tripping this drift gate.
//   - golang.org/x/sync — anchored via `tools.go` (//go:build tools)
//     so the per-umbrella errgroup orchestrator clears this drift
//     gate.
//
// Adding a new runtime dependency MUST update `want` here as part of the
// same PR with explicit reviewer ack. The "single non-stdlib runtime
// dep" minimalism baseline is documented in the project README.
package config_test

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestNoUnexpectedDirectDeps(t *testing.T) {
	want := []string{
		"github.com/BurntSushi/toml",
		"github.com/fsnotify/fsnotify",
		"github.com/spf13/cobra",
		"golang.org/x/sync",
		"golang.org/x/sys",
	}

	got := parseDirectDeps(t, repoRoot(t))
	sort.Strings(got)
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf(
			"direct dependencies drift\n  want: %v\n  got:  %v\n\n"+
				"Growing the runtime-dep budget requires updating this test\n"+
				"in the same commit.",
			want, got,
		)
	}
}

// parseDirectDeps walks go.mod's `require` blocks and returns the set of
// non-indirect module paths. Indirect (// indirect) and module-name lines
// are skipped.
func parseDirectDeps(t *testing.T, root string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("open go.mod: %v", err)
	}
	defer func() { _ = f.Close() }()

	var deps []string
	inRequire := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "require ("):
			inRequire = true
			continue
		case line == ")" && inRequire:
			inRequire = false
			continue
		case strings.HasPrefix(line, "require ") && !strings.Contains(line, "("):
			rest := strings.TrimPrefix(line, "require ")
			if dep := extractDirect(rest); dep != "" {
				deps = append(deps, dep)
			}
		case inRequire:
			if dep := extractDirect(line); dep != "" {
				deps = append(deps, dep)
			}
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatalf("scan go.mod: %v", err)
	}
	return deps
}

func extractDirect(line string) string {
	if line == "" || strings.HasPrefix(line, "//") {
		return ""
	}
	if strings.Contains(line, "// indirect") {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	return fields[0]
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller(0) failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}
