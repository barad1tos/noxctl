// Package regen owns the per-domain reconciliation runtime.
//
// The implementation still delegates to domain while the legacy method is
// unwound, but production orchestration should depend on this package rather
// than treating the declarative Domain model as the runtime entrypoint.
package regen

import (
	"context"

	"github.com/barad1tos/noxctl/bear/domain"
)

// Result is the structured outcome of one per-domain regen run.
type Result = domain.RegenResult

// Run reconciles one Domain's Bear corpus end-to-end.
func Run(ctx context.Context, d *domain.Domain) Result {
	return d.RunRegen(ctx)
}
