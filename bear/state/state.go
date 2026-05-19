// Package state owns the per-project state.json schema + load/save
// lifecycle for noxctl. Schema is locked at version="1".
//
// A corrupt state.json is RENAMED to state.json.corrupt-<RFC3339>
// and surfaced via slog.Warn — never silently reset (operator must
// investigate). The flock and plan-cache helpers compose on top of
// the same atomic write primitive.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// SchemaVersion is the only string accepted in State.Version for v1.
// Any other value is treated as schema-skew by the loader (
// surfaces this as an aggregated config error).
const SchemaVersion = "1"

// State is the on-disk schema persisted at./.noxctl/state.json. Field
// names mirror the SKELETON.md "State File Schema" section so future
// phases can extend the struct (adds in-progress markers,
// adds drift detail) without renaming existing fields.
type State struct {
	Version           string                 `json:"version"`             // strict "1"
	AppliedConfigHash string                 `json:"applied_config_hash"` // sha256 of canonicalized noxctl.toml
	Domains           map[string]DomainState `json:"domains,omitempty"`   // tag → state
	// omitempty kept intentionally for schema clarity; encoder ignores
	// it on time.Time, but the tag documents intent.
	//nolint:modernize
	LastApply    time.Time `json:"last_apply,omitempty"`
	DriftMarkers []string  `json:"drift_markers,omitempty"` // domain tags with drift
	//nolint:modernize // omitempty + nested struct match `LastApply` convention; encoder semantics noted in LEARNINGS
	InProgress InProgress `json:"in_progress,omitempty"`
}

// InProgress signals that a verb (`apply` or `daemon-cycle`) is mid-flight.
// Set on Apply entry, cleared on success. Combined with `State.LastApply`
// (set only on success), the plan engine discriminates completed-vs-
// interrupted runs:
//
// - `InProgress.Verb == "" && LastApply != zero` → last cycle completed
// - `InProgress.Verb != "" && LastApply older` → interrupted at `StartedAt`
type InProgress struct {
	Verb string `json:"verb,omitempty"` // "apply" | "daemon-cycle"
	//nolint:modernize // omitempty kept for schema-tag clarity (mirrors `LastApply` above)
	StartedAt time.Time `json:"started_at,omitempty"`
}

// DomainState is the per-domain content snapshot. ContentHash is the
// sha256 of the rendered Master+Hub bytes — the apply pipeline updates
// it; plan reads it.
type DomainState struct {
	ContentHash string `json:"content_hash"`
}

// Load reads state.json from path. Behavior:
// - missing file: returns fresh &State{Version:"1"} + nil
// - parse error: renames path → path.corrupt-<RFC3339>, slog.Warn,
// returns fresh &State{Version:"1"} + nil (NEVER silent reset —
// operator gets the corrupt file for forensics)
// - other I/O error: returns nil + wrapped error
func Load(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &State{Version: SchemaVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state load %s: %w", path, err)
	}
	var s State
	if uerr := json.Unmarshal(raw, &s); uerr != nil {
		corrupt := path + ".corrupt-" + time.Now().UTC().Format(time.RFC3339)
		if rerr := os.Rename(path, corrupt); rerr != nil {
			slog.Warn("state file corrupt; rename failed",
				"path", path, "rename_err", rerr, "parse_err", uerr.Error())
		} else {
			slog.Warn("state file corrupt; renamed for forensics",
				"path", path, "renamed_to", corrupt, "parse_err", uerr.Error())
		}
		return &State{Version: SchemaVersion}, nil
	}
	return &s, nil
}

// Save writes State to path via the bear.AtomicWriteJSON helper. perm
// is fixed at 0o600: state.json carries hashes of the applied config,
// not secrets, but defensive perm closes the multi-user
// info-disclosure window.
func (s *State) Save(path string) error {
	return bear.AtomicWriteJSON(path, s, 0o600)
}
