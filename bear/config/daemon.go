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
	EnvBearcliConcurrency  = "REGEN_BEARCLI_CONCURRENCY"   //  D-01: operator-tuned bearcli pool cap.
	EnvMtimePollInterval   = "REGEN_MTIME_POLL_INTERVAL"   //  POLL-01: database.sqlite mtime poll interval.
	EnvAutoTagPollInterval = "REGEN_AUTOTAG_POLL_INTERVAL" //  TAG-01: auto-tag fast-pass tick interval.
	EnvDomainBootstrap     = "REGEN_DOMAIN_BOOTSTRAP"      //  : universal fast-pass canonicalization kill-switch.
)

// bearcliConcurrencySoftCap is the value above which LoadDaemon emits a
// WARN log line but still accepts the value (D-02 — soft cap, not a hard
// truncation). Operators stress-testing via `--bench --sweep` must see
// exactly the value they set in the JSON metrics, not a silently-clamped
// one. Pitfall 6 in 07-RESEARCH.md covers the visibility contract.
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
	// subprocess invocations (D-01). Default 8. Threaded through
	// engine.ApplyOpts.BearcliConcurrency into bear.SetBearcliConcurrency
	// at daemon startup. Zero/negative is fatal at LoadDaemon; values >16
	// emit a WARN log line but are accepted (soft cap, D-02).
	BearcliConcurrency int

	// MtimePollInterval is the period between database.sqlite mtime
	// checks driving the second daemon trigger source (POLL-01).
	// Default 30s. Zero is a valid "disabled" sentinel — the poll loop
	// never starts (semantic divergence from BearcliConcurrency, which
	// treats zero as fatal). Negative is fatal at LoadDaemon. Threaded
	// through engine.DaemonOpts.MtimePollInterval in.
	MtimePollInterval time.Duration

	// AutoTagPollInterval is the period between auto-tag fast-pass ticks
	// (TAG-01). Each tick runs ONLY ApplyForeignTagEscape +
	// ApplyDailyDefaultTag — independent of the full regen cycle.
	// Default 2s. Zero is a valid "disabled" sentinel — the fast-pass
	// loop never starts. Negative is fatal at LoadDaemon. Threaded
	// through engine.DaemonOpts.AutoTagPollInterval in.
	AutoTagPollInterval time.Duration

	// DomainBootstrap gates the universal fast-pass
	// canonicalization pre-pass. Default true (ship-on). Env semantics
	// mirror `EnvAuditEnabled`: `REGEN_DOMAIN_BOOTSTRAP=off` disables;
	// any other non-empty value enables. The CLI boundary in
	// `cmd/regen-watchd/main.go` threads this into
	// `engine.Features.DomainBootstrap`. ships the flag only —
	// the new pass arrives in /3, so flipping this knob is a
	// behavioral no-op until then.
	DomainBootstrap bool

	// Sources maps field name → "env" | "file" | "default" so callers
	// (notably `noxctl daemon-config show`) can show provenance.
	Sources map[string]string
}

// daemonDefaults returns hardcoded defaults for every TOML-configurable
// field. Path defaults match the cmd/regen-watchd/main.go const block
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
		BearcliConcurrency:  8,                                 //  D-01 ship default.
		MtimePollInterval:   engine.DefaultMtimePollInterval,   //  POLL-01 ship default 30s.
		AutoTagPollInterval: engine.DefaultAutoTagPollInterval, //  TAG-01 ship default 2s.
		DomainBootstrap:     true,                              //   ship default ON (kill-switch is the off path).
		Sources:             make(map[string]string, 12),
	}
}

// LoadDaemon reads daemon configuration from `path`, overlays env
// vars, and returns a fully-resolved DaemonConfig. When path doesn't
// exist, returns defaults with no error. When the file is present but
// unparseable, returns the parse error so callers can decide whether
// to fatal-out.
//
// Task 2 ships file decoding; Task 3 adds env overlay; Task 4 adds
// path expansion.
func LoadDaemon(path string) (DaemonConfig, error) {
	cfg := daemonDefaults()
	for _, field := range []string{
		"DebouncePause", "MaxBurstWindow", "AuditEnabled",
		"StatePath", "LockPath", "PinsPath", "LogPath", "BearDBDir",
		"BearcliConcurrency", "MtimePollInterval", "AutoTagPollInterval",
		"DomainBootstrap",
	} {
		cfg.Sources[field] = SourceDefault
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err == nil {
		var file daemonFileContents
		if _, decodeErr := toml.Decode(string(raw), &file); decodeErr != nil {
			return cfg, fmt.Errorf("config: parse %s: %w", path, decodeErr)
		}
		if overlayErr := applyFileOverlay(&cfg, file.Daemon); overlayErr != nil {
			return cfg, fmt.Errorf("config: %s: %w", path, overlayErr)
		}
	}
	if envErr := applyEnvOverlay(&cfg); envErr != nil {
		return cfg, fmt.Errorf("config: %w", envErr)
	}
	cfg.StatePath = expandPath(cfg.StatePath)
	cfg.LockPath = expandPath(cfg.LockPath)
	cfg.PinsPath = expandPath(cfg.PinsPath)
	cfg.LogPath = expandPath(cfg.LogPath)
	cfg.BearDBDir = expandPath(cfg.BearDBDir)
	return cfg, nil
}

// applyFileOverlay applies non-nil daemonStanza fields onto cfg and
// flips Sources to SourceFile for each overridden field. Duration
// strings are validated here so a bad value in the file produces a
// clear error mentioning the field name.
func applyFileOverlay(cfg *DaemonConfig, s daemonStanza) error {
	if err := setDurationFromFile(
		s.DebouncePause, "debounce_pause", "DebouncePause",
		&cfg.DebouncePause, cfg.Sources,
	); err != nil {
		return err
	}
	if err := setDurationFromFile(
		s.MaxBurstWindow, "max_burst_window", "MaxBurstWindow",
		&cfg.MaxBurstWindow, cfg.Sources,
	); err != nil {
		return err
	}
	if err := setDurationFromFile(
		s.MtimePollInterval, "mtime_poll_interval", "MtimePollInterval",
		&cfg.MtimePollInterval, cfg.Sources,
	); err != nil {
		return err
	}
	if cfg.MtimePollInterval < 0 {
		return fmt.Errorf(
			"mtime_poll_interval = %s: must be >= 0 (0 disables polling)",
			cfg.MtimePollInterval,
		)
	}
	if err := setDurationFromFile(
		s.AutoTagPollInterval, "auto_tag_poll_interval", "AutoTagPollInterval",
		&cfg.AutoTagPollInterval, cfg.Sources,
	); err != nil {
		return err
	}
	if cfg.AutoTagPollInterval < 0 {
		return fmt.Errorf("auto_tag_poll_interval = %s: must be >= 0 (0 disables fast-pass)",
			cfg.AutoTagPollInterval,
		)
	}
	if s.AuditEnabled != nil {
		cfg.AuditEnabled = *s.AuditEnabled
		cfg.Sources["AuditEnabled"] = SourceFile
	}
	if s.DomainBootstrap != nil {
		cfg.DomainBootstrap = *s.DomainBootstrap
		cfg.Sources["DomainBootstrap"] = SourceFile
	}
	if s.BearcliConcurrency != nil {
		if err := validateConcurrency(*s.BearcliConcurrency, "bearcli_concurrency"); err != nil {
			return err
		}
		cfg.BearcliConcurrency = *s.BearcliConcurrency
		cfg.Sources["BearcliConcurrency"] = SourceFile
	}
	if s.Paths != nil {
		applyPathsOverlay(cfg, *s.Paths)
	}
	return nil
}

// validateConcurrency rejects zero/negative bearcli_concurrency values
// and emits a WARN log line for values above the soft cap. keyName is
// the operator-facing identifier (TOML key or env-var name) so the
// error message points at exactly the input the operator supplied.
//
// Soft-cap semantics (D-02): values >16 are accepted but
// logged — we do NOT truncate, because operators stress-testing via
// `regen-watchd --bench --sweep` need the JSON metrics to reflect the
// configured value, not a silently-clamped one.
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

// applyPathsOverlay applies non-nil [daemon.paths] fields onto cfg.
func applyPathsOverlay(cfg *DaemonConfig, p daemonPathsStanza) {
	if p.State != nil {
		cfg.StatePath = *p.State
		cfg.Sources["StatePath"] = SourceFile
	}
	if p.Lock != nil {
		cfg.LockPath = *p.Lock
		cfg.Sources["LockPath"] = SourceFile
	}
	if p.Pins != nil {
		cfg.PinsPath = *p.Pins
		cfg.Sources["PinsPath"] = SourceFile
	}
	if p.Log != nil {
		cfg.LogPath = *p.Log
		cfg.Sources["LogPath"] = SourceFile
	}
	if p.BearDB != nil {
		cfg.BearDBDir = *p.BearDB
		cfg.Sources["BearDBDir"] = SourceFile
	}
}

// applyEnvOverlay walks every supported env-var, applies the value
// onto cfg if set, and flips Sources to SourceEnv. Duration parse
// errors short-circuit with a fmt.Errorf mentioning the env-var name
// so operators can locate the bad value quickly.
func applyEnvOverlay(cfg *DaemonConfig) error {
	if err := envOverlayDuration(cfg, EnvDebouncePause, "DebouncePause", &cfg.DebouncePause); err != nil {
		return err
	}
	if err := envOverlayDuration(cfg, EnvMaxBurstWindow, "MaxBurstWindow", &cfg.MaxBurstWindow); err != nil {
		return err
	}
	if err := envOverlayDuration(cfg, EnvMtimePollInterval, "MtimePollInterval", &cfg.MtimePollInterval); err != nil {
		return err
	}
	if cfg.MtimePollInterval < 0 {
		return fmt.Errorf("env %s %s: must be >= 0 (0 disables polling)",
			EnvMtimePollInterval, cfg.MtimePollInterval)
	}
	if err := envOverlayDuration(
		cfg, EnvAutoTagPollInterval, "AutoTagPollInterval", &cfg.AutoTagPollInterval,
	); err != nil {
		return err
	}
	if cfg.AutoTagPollInterval < 0 {
		return fmt.Errorf("env %s %s: must be >= 0 (0 disables fast-pass)",
			EnvAutoTagPollInterval, cfg.AutoTagPollInterval)
	}
	if v := os.Getenv(EnvAuditEnabled); v != "" {
		// REGEN_AUDIT quirk: only "off" disables; anything else enables.
		// Backward-compat with cmd/regen-watchd/main.go pre-Task-3 semantics.
		cfg.AuditEnabled = v != "off"
		cfg.Sources["AuditEnabled"] = SourceEnv
	}
	if v := os.Getenv(EnvDomainBootstrap); v != "" {
		// REGEN_DOMAIN_BOOTSTRAP mirrors REGEN_AUDIT semantics (
		// ): only "off" disables; anything else enables. Per D-03
		// env > file > default precedence is enforced by the call order
		// (file overlay ran above; this env block writes last).
		cfg.DomainBootstrap = v != "off"
		cfg.Sources["DomainBootstrap"] = SourceEnv
	}
	envOverlayString(cfg, EnvStatePath, "StatePath", &cfg.StatePath)
	envOverlayString(cfg, EnvLockPath, "LockPath", &cfg.LockPath)
	envOverlayString(cfg, EnvPinsPath, "PinsPath", &cfg.PinsPath)
	envOverlayString(cfg, EnvLogPath, "LogPath", &cfg.LogPath)
	envOverlayString(cfg, EnvBearDBDir, "BearDBDir", &cfg.BearDBDir)
	if err := envOverlayBearcliConcurrency(cfg); err != nil {
		return err
	}
	return nil
}

// envOverlayBearcliConcurrency parses REGEN_BEARCLI_CONCURRENCY into
// cfg.BearcliConcurrency. Separated from applyEnvOverlay to keep that
// function's gocognit budget intact (D-01).
func envOverlayBearcliConcurrency(cfg *DaemonConfig) error {
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
	cfg.BearcliConcurrency = n
	cfg.Sources["BearcliConcurrency"] = SourceEnv
	return nil
}

// envOverlayDuration parses an env-var as a time.Duration and applies it.
// No-op if the env-var is unset/empty.
func envOverlayDuration(cfg *DaemonConfig, envName, sourceKey string, dst *time.Duration) error {
	v := os.Getenv(envName)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("env %s %q: %w", envName, v, err)
	}
	*dst = d
	cfg.Sources[sourceKey] = SourceEnv
	return nil
}

// envOverlayString applies an env-var string verbatim. No-op if unset/empty.
func envOverlayString(cfg *DaemonConfig, envName, sourceKey string, dst *string) {
	v := os.Getenv(envName)
	if v == "" {
		return
	}
	*dst = v
	cfg.Sources[sourceKey] = SourceEnv
}

// expandPath performs ~/ -> $HOME/ rewriting then $VAR substitution.
// Two-step so a path like "~/$LOG_DIR/file" round-trips: leading "~/"
// expands first, then any embedded vars expand via os.ExpandEnv.
// Mirrors the existing cmd/regen-watchd/main.go envOr behavior.
func expandPath(p string) string {
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		p = "$HOME" + p[1:] // "~/foo" -> "$HOME/foo"
	}
	return os.ExpandEnv(p)
}
