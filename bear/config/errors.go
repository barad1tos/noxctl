package config

import "errors"

// Sentinel errors for typed-error inspection via errors.Is.
//
// Pattern follows the existing domain.ErrHashConflict convention
// (bear/domain.go) — package-level vars, not types — so callers can
// do `errors.Is(err, config.ErrUnknownBlueprint)` regardless of how
// the loader / validator chooses to wrap them via fmt.Errorf %w or
// errors.Join.
var (
	// ErrUnknownBlueprint is wrapped when a [[domain]].blueprint value
	// does not appear in the closed 6-entry dispatch map.
	ErrUnknownBlueprint = errors.New("config: unknown blueprint")

	// ErrSchemaVersion is wrapped when [meta].version is anything
	// other than the string "1" — noxctl ships schema v1 only.
	ErrSchemaVersion = errors.New("config: unsupported schema version")

	// ErrDuplicateTag is wrapped when two or more [[domain]] entries
	// share the same `tag` value — duplicates would race for the
	// same Bear sidebar tag and produce non-deterministic output.
	ErrDuplicateTag = errors.New("config: duplicate domain tag")
)
