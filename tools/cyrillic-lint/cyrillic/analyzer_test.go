package cyrillic_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/barad1tos/noxctl/tools/cyrillic-lint/cyrillic"
)

// TestCyrillicAnalyzer_FlagsPositiveFixture asserts the analyzer
// emits a diagnostic on a Cyrillic literal in a non-permitted package.
func TestCyrillicAnalyzer_FlagsPositiveFixture(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), cyrillic.Analyzer, "positive")
}

// TestCyrillicAnalyzer_DoesNotFlagPermitted asserts the analyzer
// stays silent under //cyrillic:permit (analysistest fails the run if
// the analyzer reports any diagnostic without a matching //want).
func TestCyrillicAnalyzer_DoesNotFlagPermitted(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), cyrillic.Analyzer, "permitted")
}
