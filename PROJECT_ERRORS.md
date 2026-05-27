# Agent Error Log

Before editing code in this repository, read this file and compare the planned
change against the recorded mistakes. Do not dismiss formatter, linter, or IDE
modernization hints as noise until they have been checked against the project's
Go version and conventions.

## Go 1.26 `new()` Modernization

Mistake: treating the Go 1.26 syntax update below as a bad or accidental local
change.

Observed warning:

```text
Problems in file bear/cli/plan.go:
1. Syntax update in Go 1.26: replace pointer to local variable 'features' with new() (Line № 87)
```

Correct pattern:

```go
result, planErr := engine.Plan(sigCtx, engine.PlanOpts{
	Domains:  domains,
	Verbose:  opts.Verbose,
	Stderr:   opts.Stderr,
	Features: new(cliutil.FeaturesFromCatalog(catalog)),
})
```

Do not rewrite that back to a local variable plus address:

```go
features := cliutil.FeaturesFromCatalog(catalog)
// ...
Features: &features,
```

Rule: when editing Go code in this project, verify Go 1.26 modernization hints
before undoing them. If the hint matches valid Go 1.26 syntax and project tests
compile, keep the modernization.
