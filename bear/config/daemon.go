// Package config — daemon runtime configuration.
//
// LoadDaemon reads ~/.noxctl/daemon.toml (path is supplied by the
// caller — production passes the resolved $HOME path; tests point at
// TempDir). Resolution order, highest to lowest:
//
// 1. Environment variable
// 2. ~/.noxctl/daemon.toml value
// 3. Hardcoded default from bear/engine
//
// Provenance is tracked in DaemonConfig.Sources so the
// `noxctl daemon-config show` CLI can annotate each field with where
// its effective value came from.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/barad1tos/noxctl/bear/engine"
)

// Environment variable names recognized by LoadDaemon. Existing names
// preserve backward-compat; the two new ones (DEBOUNCE_PAUSE, MAX_BURST)
// expose timers that previously had no override path.
const (
	EnvDebouncePause       = "REGEN_DEBOUNCE_PAUSE"
	EnvMaxBurstWindow      = "REGEN_MAX_BURST"
	EnvAuditEnabled        = "REGEN_AUDIT"
	EnvStatePath           = "REGEN_STATE_PATH"
	EnvLockPath            = "REGEN_LOCK_PATH"
	EnvPinsPath            = "REGEN_PINS_PATH"
	EnvLogPath             = "REGEN_LOG_PATH"
	EnvBearDBDir           = "BEAR_DB_DIR"
	EnvBearcliConcurrency  = "REGEN_BEARCLI_CONCURRENCY"   // operator-tuned bearcli pool cap.
	EnvMtimePollInterval   = "REGEN_MTIME_POLL_INTERVAL"   // database.sqlite mtime poll interval.
	EnvAutoTagPollInterval = "REGEN_AUTOTAG_POLL_INTERVAL" // auto-tag fast-pass tick interval.
	EnvDomainBootstrap     = "REGEN_DOMAIN_BOOTSTRAP"      // universal fast-pass canonicalization kill-switch.
)

// bearcliConcurrencySoftCap is the value above which LoadDaemon emits a
// WARN log line but still accepts the value — soft cap, not a hard
// truncation. Operators stress-testing via `--bench --sweep` must see
// exactly the value they set in the JSON metrics, not a silently-clamped
// one.
const bearcliConcurrencySoftCap = 16

// SourceDefault, SourceFile, SourceEnv are the values stored in
// DaemonConfig.Sources to indicate where each field's value came from.
const (
	SourceDefault = "default"
	SourceFile    = "file"
	SourceEnv     = "env"
)

// DaemonConfig holds the fully-resolved daemon configuration. Every
// field is populated — there are no zero-value "unset" sentinels here.
// Construct via LoadDaemon.
type DaemonConfig struct {
	DebouncePause  time.Duration
	MaxBurstWindow time.Duration
	AuditEnabled   bool
	StatePath      string
	LockPath       string
	PinsPath       string
	LogPath        string
	BearDBDir      string

	// BearcliConcurrency is the operator-tuned cap for concurrent bearcli
	// subprocess invocations. Default 8. Threaded through
	// engine.ApplyOpts.BearcliConcurrency into bearcli.SetConcurrency at
	// daemon startup. Zero/negative is fatal at LoadDaemon; values >16
	// emit a WARN log line but are accepted (soft cap).
	BearcliConcurrency int

	// MtimePollInterval is the period between database.sqlite mtime
	// checks driving the second daemon trigger source. Default 30s. Zero
	// is a valid "disabled" sentinel — the poll loop never starts
	// (semantic divergence from BearcliConcurrency, which treats zero as
	// fatal). Negative is fatal at LoadDaemon. Threaded through
	// engine.DaemonOpts.MtimePollInterval.
	MtimePollInterval time.Duration

	// AutoTagPollInterval is the period between auto-tag fast-pass ticks.
	// Each tick runs ONLY ApplyForeignTagEscape + ApplyDailyDefaultTag —
	// independent of the full regen cycle. Default 2s. Zero is a valid
	// "disabled" sentinel — the fast-pass loop never starts. Negative is
	// fatal at LoadDaemon. Threaded through
	// engine.DaemonOpts.AutoTagPollInterval.
	AutoTagPollInterval time.Duration

	// DomainBootstrap gates the universal fast-pass canonicalization
	// pre-pass. Default true (ship-on). Env semantics mirror
	// `EnvAuditEnabled`: `REGEN_DOMAIN_BOOTSTRAP=off` disables; any other
	// non-empty value enables. Threaded into
	// `engine.Features.DomainBootstrap`.
	DomainBootstrap bool

	// Sources maps field name → "env" | "file" | "default" so callers
	// (notably `noxctl daemon-config show`) can show provenance.
	Sources map[string]string
}

// daemonDefaults returns hardcoded defaults for every TOML-configurable
// field. Path defaults match the legacy daemon's const block
// (defaultLogPath, defaultPinsPath) plus the engine.DefaultDebounce*
// constants.
func daemonDefaults() DaemonConfig {
	return DaemonConfig{
		DebouncePause:       engine.DefaultDebouncePause,
		MaxBurstWindow:      engine.DefaultMaxBurstWindow,
		AuditEnabled:        true,
		StatePath:           "$HOME/.noxctl/state.json",
		LockPath:            "$HOME/.noxctl/.lock",
		PinsPath:            "$HOME/.cache/regen-watchd-pins.json",
		LogPath:             "$HOME/.cache/regen-watchd.log",
		BearDBDir:           "",                                // empty = auto-discover
		BearcliConcurrency:  8,                                 // ship default.
		MtimePollInterval:   engine.DefaultMtimePollInterval,   // ship default 30s.
		AutoTagPollInterval: engine.DefaultAutoTagPollInterval, // ship default 2s.
		DomainBootstrap:     true,                              // ship default ON (kill-switch is the off path).
		Sources:             make(map[string]string, 12),
	}
}

// LoadDaemon reads daemon configuration from `path`, overlays env
// vars, and returns a fully-resolved DaemonConfig. When path doesn't
// exist, returns defaults with no error. When the file is present but
// unparseable, returns the parse error so callers can decide whether
// to fatal-out.
func LoadDaemon(path string) (DaemonConfig, error) {
	dc := daemonDefaults()
	for _, field := range []string{
		"DebouncePause", "MaxBurstWindow", "AuditEnabled",
		"StatePath", "LockPath", "PinsPath", "LogPath", "BearDBDir",
		"BearcliConcurrency", "MtimePollInterval", "AutoTagPollInterval",
		"DomainBootstrap",
	} {
		dc.Sources[field] = SourceDefault
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dc, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err == nil {
		var file daemonFileContents
		if _, decodeErr := toml.Decode(string(raw), &file); decodeErr != nil {
			return dc, fmt.Errorf("config: parse %s: %w", path, decodeErr)
		}
		if overlayErr := applyFileOverlay(&dc, file.Daemon); overlayErr != nil {
			return dc, fmt.Errorf("config: %s: %w", path, overlayErr)
		}
	}
	if envErr := applyEnvOverlay(&dc); envErr != nil {
		return dc, fmt.Errorf("config: %w", envErr)
	}
	dc.StatePath = expandPath(dc.StatePath)
	dc.LockPath = expandPath(dc.LockPath)
	dc.PinsPath = expandPath(dc.PinsPath)
	dc.LogPath = expandPath(dc.LogPath)
	dc.BearDBDir = expandPath(dc.BearDBDir)
	return dc, nil
}

// applyFileOverlay applies non-nil daemonStanza fields onto dc and
// flips Sources to SourceFile for each overridden field. Duration
// strings are validated here so a bad value in the file produces a
// clear error mentioning the field name.
func applyFileOverlay(dc *DaemonConfig, s daemonStanza) error {
	if err := setDurationFromFile(
		s.DebouncePause, "debounce_pause", "DebouncePause",
		&dc.DebouncePause, dc.Sources,
	); err != nil {
		return err
	}
	if err := setDurationFromFile(
		s.MaxBurstWindow, "max_burst_window", "MaxBurstWindow",
		&dc.MaxBurstWindow, dc.Sources,
	); err != nil {
		return err
	}
	if err := setDurationFromFile(
		s.MtimePollInterval, "mtime_poll_interval", "MtimePollInterval",
		&dc.MtimePollInterval, dc.Sources,
	); err != nil {
		return err
	}
	if dc.MtimePollInterval < 0 {
		return fmt.Errorf(
			"mtime_poll_interval = %s: must be >= 0 (0 disables polling)",
			dc.MtimePollInterval,
		)
	}
	if err := setDurationFromFile(
		s.AutoTagPollInterval, "auto_tag_poll_interval", "AutoTagPollInterval",
		&dc.AutoTagPollInterval, dc.Sources,
	); err != nil {
		return err
	}
	if dc.AutoTagPollInterval < 0 {
		return fmt.Errorf("auto_tag_poll_interval = %s: must be >= 0 (0 disables fast-pass)",
			dc.AutoTagPollInterval,
		)
	}
	if s.AuditEnabled != nil {
		dc.AuditEnabled = *s.AuditEnabled
		dc.Sources["AuditEnabled"] = SourceFile
	}
	if s.DomainBootstrap != nil {
		dc.DomainBootstrap = *s.DomainBootstrap
		dc.Sources["DomainBootstrap"] = SourceFile
	}
	if s.BearcliConcurrency != nil {
		if err := validateConcurrency(*s.BearcliConcurrency, "bearcli_concurrency"); err != nil {
			return err
		}
		dc.BearcliConcurrency = *s.BearcliConcurrency
		dc.Sources["BearcliConcurrency"] = SourceFile
	}
	if s.Paths != nil {
		applyPathsOverlay(dc, *s.Paths)
	}
	return nil
}

// validateConcurrency rejects zero/negative bearcli_concurrency values
// and emits a WARN log line for values above the soft cap. keyName is
// the operator-facing identifier (TOML key or env-var name) so the
// error message points at exactly the input the operator supplied.
//
// Soft-cap semantics: values >16 are accepted but logged — we do NOT
// truncate, because operators stress-testing via `--bench --sweep`
// need the JSON metrics to reflect the configured value, not a
// silently-clamped one.
func validateConcurrency(n int, keyName string) error {
	if n <= 0 {
		return fmt.Errorf("%s = %d: must be > 0", keyName, n)
	}
	if n > bearcliConcurrencySoftCap {
		log.Printf("WARN: %s=%d exceeds soft cap %d; expect Bear sqlite contention",
			keyName, n, bearcliConcurrencySoftCap)
	}
	return nil
}

// setDurationFromFile parses raw (if non-nil) into dst and records
// SourceFile under sourceKey in sources. tomlKey is used in the error
// message so operators see the exact key from their config file.
func setDurationFromFile(raw *string, tomlKey, sourceKey string, dst *time.Duration, sources map[string]string) error {
	if raw == nil {
		return nil
	}
	d, err := time.ParseDuration(*raw)
	if err != nil {
		return fmt.Errorf("%s %q: %w", tomlKey, *raw, err)
	}
	*dst = d
	sources[sourceKey] = SourceFile
	return nil
}

// applyPathsOverlay applies non-nil [daemon.paths] fields onto dc.
func applyPathsOverlay(dc *DaemonConfig, p daemonPathsStanza) {
	if p.State != nil {
		dc.StatePath = *p.State
		dc.Sources["StatePath"] = SourceFile
	}
	if p.Lock != nil {
		dc.LockPath = *p.Lock
		dc.Sources["LockPath"] = SourceFile
	}
	if p.Pins != nil {
		dc.PinsPath = *p.Pins
		dc.Sources["PinsPath"] = SourceFile
	}
	if p.Log != nil {
		dc.LogPath = *p.Log
		dc.Sources["LogPath"] = SourceFile
	}
	if p.BearDB != nil {
		dc.BearDBDir = *p.BearDB
		dc.Sources["BearDBDir"] = SourceFile
	}
}

// applyEnvOverlay walks every supported env-var, applies the value
// onto dc if set, and flips Sources to SourceEnv. Duration parse
// errors short-circuit with a fmt.Errorf mentioning the env-var name
// so operators can locate the bad value quickly.
func applyEnvOverlay(dc *DaemonConfig) error {
	if err := envOverlayDuration(
		dc, EnvDebouncePause, "DebouncePause",
		&dc.DebouncePause); err != nil {
		return err
	}
	if err := envOverlayDuration(
		dc, EnvMaxBurstWindow, "MaxBurstWindow",
		&dc.MaxBurstWindow); err != nil {
		return err
	}
	if err := envOverlayDuration(
		dc, EnvMtimePollInterval, "MtimePollInterval",
		&dc.MtimePollInterval); err != nil {
		return err
	}
	if dc.MtimePollInterval < 0 {
		return fmt.Errorf("env %s %s: must be >= 0 (0 disables polling)",
			EnvMtimePollInterval, dc.MtimePollInterval)
	}
	if err := envOverlayDuration(
		dc, EnvAutoTagPollInterval, "AutoTagPollInterval",
		&dc.AutoTagPollInterval,
	); err != nil {
		return err
	}
	if dc.AutoTagPollInterval < 0 {
		return fmt.Errorf("env %s %s: must be >= 0 (0 disables fast-pass)",
			EnvAutoTagPollInterval, dc.AutoTagPollInterval)
	}
	if v := os.Getenv(EnvAuditEnabled); v != "" {
		// REGEN_AUDIT quirk: only "off" disables; anything else enables.
		// Backward-compat with the legacy daemon's semantics.
		dc.AuditEnabled = v != "off"
		dc.Sources["AuditEnabled"] = SourceEnv
	}
	if v := os.Getenv(EnvDomainBootstrap); v != "" {
		// REGEN_DOMAIN_BOOTSTRAP mirrors REGEN_AUDIT semantics: only "off"
		// disables; anything else enables. env > file > default precedence
		// is enforced by the call order — the file overlay ran above; this
		// env block writes last.
		dc.DomainBootstrap = v != "off"
		dc.Sources["DomainBootstrap"] = SourceEnv
	}
	envOverlayString(dc, EnvStatePath, "StatePath", &dc.StatePath)
	envOverlayString(dc, EnvLockPath, "LockPath", &dc.LockPath)
	envOverlayString(dc, EnvPinsPath, "PinsPath", &dc.PinsPath)
	envOverlayString(dc, EnvLogPath, "LogPath", &dc.LogPath)
	envOverlayString(dc, EnvBearDBDir, "BearDBDir", &dc.BearDBDir)
	if err := envOverlayBearcliConcurrency(dc); err != nil {
		return err
	}
	return nil
}

// envOverlayBearcliConcurrency parses REGEN_BEARCLI_CONCURRENCY into
// dc.BearcliConcurrency. Separated from applyEnvOverlay to keep that
// function's gocognit budget intact.
func envOverlayBearcliConcurrency(dc *DaemonConfig) error {
	v := os.Getenv(EnvBearcliConcurrency)
	if v == "" {
		return nil
	}
	n, parseErr := strconv.Atoi(v)
	if parseErr != nil {
		return fmt.Errorf("env %s %q: %w", EnvBearcliConcurrency, v, parseErr)
	}
	if err := validateConcurrency(n, EnvBearcliConcurrency); err != nil {
		return err
	}
	dc.BearcliConcurrency = n
	dc.Sources["BearcliConcurrency"] = SourceEnv
	return nil
}

// envOverlayDuration parses an env-var as a time.Duration and applies it.
// No-op if the env-var is unset/empty.
func envOverlayDuration(dc *DaemonConfig, envName, sourceKey string, dst *time.Duration) error {
	v := os.Getenv(envName)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("env %s %q: %w", envName, v, err)
	}
	*dst = d
	dc.Sources[sourceKey] = SourceEnv
	return nil
}

// envOverlayString applies an env-var string verbatim. No-op if unset/empty.
func envOverlayString(dc *DaemonConfig, envName, sourceKey string, dst *string) {
	v := os.Getenv(envName)
	if v == "" {
		return
	}
	*dst = v
	dc.Sources[sourceKey] = SourceEnv
}

// expandPath performs ~/ -> $HOME/ rewriting then $VAR substitution.
// Two-step so a path like "~/$LOG_DIR/file" round-trips: leading "~/"
// expands first, then any embedded vars expand via os.ExpandEnv.
// Mirrors the legacy daemon's envOr behavior.
func expandPath(p string) string {
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		p = "$HOME" + p[1:] // "~/foo" -> "$HOME/foo"
	}
	return os.ExpandEnv(p)
}
