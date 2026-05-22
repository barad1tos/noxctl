// Package examples_test walks examples/*.toml (catalog shape only —
// daemon.toml is a separate runtime-config schema and explicitly
// excluded) and asserts that every file loads without errors. Guards
// against the maintenance burden of the per-blueprint example set
// — a typo in a field name or a stale schema reference now fails CI
// instead of a confused new user.
package examples_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
)

// daemonConfigFile is the one example NOT loaded as a catalog —
// it ships a [daemon] block instead of [[domain]] stanzas, lives
// under bear/config.LoadDaemon, and would fail strict catalog
// schema validation by design.
const daemonConfigFile = "daemon.toml"

func TestExamples_AllCatalogFilesLoad(t *testing.T) {
	matches, err := collectExampleCatalogs("../../../examples")
	if err != nil {
		t.Fatalf("walk examples/: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no catalog examples found under examples/ — expected at least 1")
	}

	loaded := 0
	for _, path := range matches {
		base := filepath.Base(path)
		t.Run(filepath.ToSlash(strings.TrimPrefix(path, "../../../examples/")), func(t *testing.T) {
			if _, _, loadErr := config.Load(path); loadErr != nil {
				t.Errorf("config.Load(%s): %v", path, loadErr)
			}
		})
		loaded++
		_ = base
	}

	if loaded < 1 {
		t.Fatalf("no catalog examples loaded — daemon.toml is the only file")
	}
	t.Logf("validated %d catalog example(s)", loaded)
}

// TestExamples_PerBlueprintCoverage asserts every blueprint named
// in the dispatch catalog has at least one matching single-stanza
// example file (`examples/<blueprint>.toml`). Guards against a new
// blueprint being added to the catalog without a starter example
// being shipped alongside.
func TestExamples_PerBlueprintCoverage(t *testing.T) {
	// Closed catalog from bear/config/dispatch.go::dispatch keys.
	// Hard-coded here on purpose — the test fails loud if the
	// catalog grows and the example set doesn't.
	blueprints := []string{
		"flat-list",
		"flat-table",
		"grouped-vertical",
		"hub-routed",
		"hub-routed-with-subtag",
		"umbrella",
	}

	for _, blueprint := range blueprints {
		path := "../../../examples/" + blueprint + ".toml"
		t.Run(blueprint, func(t *testing.T) {
			_, _, err := config.Load(path)
			if err != nil {
				t.Errorf("expected examples/%s.toml to load cleanly; got: %v",
					blueprint, err)
			}
		})
	}

	// Quick sanity: the new blueprint set should exactly match the
	// dispatch map size. If it diverges, the slice above needs
	// updating in lockstep with dispatch.go.
	if got, want := len(blueprints), config.DispatchSize(); got != want {
		t.Errorf("blueprints slice len=%d but config.DispatchSize()=%d — "+
			"add the new blueprint to this slice AND ship an "+
			"examples/<blueprint>.toml starter", got, want)
	}
}

// TestExamples_MinimalIsTrulyMinimal asserts the minimal.toml
// starter declares exactly ONE domain — the file's whole purpose
// is the smallest possible catalog. Drift toward "minimal +
// 1 more for variety" is a slippery slope; this test holds the
// line.
func TestExamples_MinimalIsTrulyMinimal(t *testing.T) {
	const path = "../../../examples/minimal.toml"
	domains, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load(%s): %v", path, err)
	}
	if got := len(domains); got != 1 {
		t.Errorf("examples/minimal.toml must declare exactly 1 domain "+
			"(it's the truly-minimal starter); got %d. If a richer "+
			"showcase is needed, extend examples/three-blueprints.toml "+
			"instead.", got)
	}
}

// TestExamples_DocstringHeader asserts every catalog example
// starts with a comment block — undocumented examples confuse new
// users staring at a TOML file with no context. Cheap insurance
// against future additions skipping the header.
func TestExamples_DocstringHeader(t *testing.T) {
	matches, err := collectExampleCatalogs("../../../examples")
	if err != nil {
		t.Fatalf("walk examples/: %v", err)
	}
	for _, path := range matches {
		relPath := filepath.ToSlash(strings.TrimPrefix(path, "../../../examples/"))
		t.Run(relPath, func(t *testing.T) {
			data, readErr := readFirstLine(path)
			if readErr != nil {
				t.Fatalf("read %s: %v", path, readErr)
			}
			if !strings.HasPrefix(data, "# ") {
				t.Errorf("examples/%s must open with a `# ` comment "+
					"header explaining when to use this example; got: %q",
					relPath, data)
			}
		})
	}
}

// collectExampleCatalogs walks `dir` recursively and returns every
// `.toml` file EXCEPT daemon.toml (different schema — daemon runtime
// config, not catalog). Used by both the load-correctness test and
// the docstring-header test so demo-vault subdirectories get the
// same guarantees as top-level examples.
func collectExampleCatalogs(dir string) ([]string, error) {
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(path) == daemonConfigFile {
			return nil
		}
		if filepath.Ext(path) == ".toml" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func readFirstLine(path string) (string, error) {
	const maxFirstLineBytes = 256
	full, err := readFile(path, maxFirstLineBytes)
	if err != nil {
		return "", err
	}
	if first, _, ok := strings.Cut(full, "\n"); ok {
		return first, nil
	}
	return full, nil
}

func readFile(path string, maxBytes int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) > maxBytes {
		data = data[:maxBytes]
	}
	return string(data), nil
}
