// Package main wraps the cyrillic Analyzer in singlechecker so it can
// be invoked as a standalone binary by pre-commit and CI.
package main

import (
	"github.com/barad1tos/noxctl/tools/cyrillic-lint/cyrillic"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() { singlechecker.Main(cyrillic.Analyzer) }
