// Package regen owns Bear I/O for per-domain reconciliation.
package regen

import (
	"context"
	"fmt"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// BuildCorpusDuplicateRegistry indexes every Bear note in the notes location.
// Use this when rendering generated links: unmanaged notes can still collide
// with managed atom titles, and Bear's [[Title]] resolver does not know which
// corpus slice noxctl owns.
func BuildCorpusDuplicateRegistry(ctx context.Context) (*domain.DuplicateRegistry, error) {
	notes, err := bearcli.ListCorpusNotes(ctx)
	if err != nil {
		return nil, fmt.Errorf("BuildCorpusDuplicateRegistry: %w", err)
	}
	return domain.BuildDuplicateRegistry(notes), nil
}
