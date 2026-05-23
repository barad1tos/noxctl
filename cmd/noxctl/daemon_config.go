// daemon_config.go — `noxctl daemon-config show` subcommand. Reads
// ~/.noxctl/daemon.toml (path resolved from $HOME so tests can point
// it elsewhere), overlays env-vars, and dumps the effective config
// with per-field provenance annotations.
//
// The dump shape is TOML-ish (not strictly round-trippable — values
// carry `# default` / `# from file` / `# from env <NAME>` comments)
// so operators can both read the live state and copy-paste fields
// they want to override into a real daemon.toml.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/config"
)

// daemonConfigCmd is the parent for `daemon-config` subcommands. Only
// `show` exists in this phase; future tasks may add `validate` or
// `paths` peers without disturbing the wiring here.
var daemonConfigCmd = &cobra.Command{
	Use:   "daemon-config",
	Short: "Inspect daemon configuration",
}

// daemonConfigShowCmd dumps the effective daemon configuration with
// per-field provenance. Exit semantics:
//
//	0 — config dumped (file present or absent are both fine).
//	1 — daemon config could not be resolved for a non-parse reason.
//	2 — daemon.toml is present but unparseable (caller wants to fix it).
var daemonConfigShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the effective daemon configuration with provenance",
	Long: `Reads ~/.noxctl/daemon.toml (if present), applies REGEN_*
environment variable overrides, and writes the resolved configuration
in TOML shape with each value annotated by its source — "default",
"from file", or "from env <VAR>".

Exit codes:
  0 — config dumped (file present or absent are both fine)
  2 — file present but unparseable; error printed to stderr`,
	RunE: runDaemonConfigShow,
}

func init() {
	daemonConfigCmd.AddCommand(daemonConfigShowCmd)
	rootCmd.AddCommand(daemonConfigCmd)
}

// runDaemonConfigShow is the daemon-config show RunE. Three-way
// outcome: dump on success, exit-2 when daemon.toml is present but
// LoadDaemon returns a parse error, propagate the error otherwise.
func runDaemonConfigShow(cmd *cobra.Command, _ []string) error {
	path, pathErr := daemonConfigPath()
	if pathErr != nil {
		return pathErr
	}
	dc, loadErr := config.LoadDaemon(path)
	if loadErr != nil {
		// File present but unparseable → exit 2 per spec. Stat
		// independently of the load error so a transient ReadFile
		// failure doesn't get mis-classified.
		if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
			cmd.PrintErrln(loadErr)
			exitWith(2)
			return nil
		}
		return loadErr
	}
	_, statErr := os.Stat(path)
	present := !errors.Is(statErr, fs.ErrNotExist)
	writeDaemonConfigShow(cmd.OutOrStdout(), path, dc, present)
	return nil
}

// daemonConfigPath resolves the canonical daemon-config path
// (`$HOME/.noxctl/daemon.toml`). Returns an error when the home-dir
// lookup fails — silently falling back to `./.noxctl/daemon.toml`
// would either pick up an unrelated file from the daemon's working
// directory or silently mask the operator's real config, which the
// daemon contract is meant to prevent. Shared between
// `daemon-config show` and `runDaemon` startup so the two CANNOT
// disagree about which file they're reading.
func daemonConfigPath() (string, error) {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		return "", fmt.Errorf("resolve daemon-config path: home dir lookup failed: %w", homeErr)
	}
	return filepath.Join(home, ".noxctl", "daemon.toml"), nil
}

// writeDaemonConfigShow renders the dump. Kept under gocognit by
// extracting the [daemon] and [daemon.paths] sections into focused
// helpers — adding new fields touches one of those, not the parent.
func writeDaemonConfigShow(w io.Writer, path string, dc config.DaemonConfig, present bool) {
	writeLine(w, "# Effective daemon configuration (env > ~/.noxctl/daemon.toml > defaults)")
	if present {
		writeFormatted(w, "# Config file: %s (present)\n", path)
	} else {
		writeFormatted(w, "# Config file: %s (not found)\n", path)
	}
	writeLine(w, "")
	writeDaemonConfigSection(w, dc)
	writeLine(w, "")
	writeDaemonConfigPathsSection(w, dc)
}

// writeDaemonConfigSection emits the [daemon] table. Field width
// hand-tuned so the provenance comments line up visually — when
// adding a field, eyeball the column alignment.
func writeDaemonConfigSection(w io.Writer, dc config.DaemonConfig) {
	writeLine(w, "[daemon]")
	writeFormatted(w, "debounce_pause   = %q   %s\n",
		dc.DebouncePause.String(),
		provenanceTag(dc.Sources["DebouncePause"], config.EnvDebouncePause))
	writeFormatted(w, "max_burst_window = %q   %s\n",
		dc.MaxBurstWindow.String(),
		provenanceTag(dc.Sources["MaxBurstWindow"], config.EnvMaxBurstWindow))
	writeFormatted(w, "audit_enabled    = %v        %s\n",
		dc.AuditEnabled,
		provenanceTag(dc.Sources["AuditEnabled"], config.EnvAuditEnabled))
	writeFormatted(w, "bearcli_concurrency = %d     %s\n",
		dc.BearcliConcurrency,
		provenanceTag(dc.Sources["BearcliConcurrency"], config.EnvBearcliConcurrency))
	writeFormatted(w, "mtime_poll_interval = %q  %s\n",
		dc.MtimePollInterval.String(),
		provenanceTag(dc.Sources["MtimePollInterval"], config.EnvMtimePollInterval))
	writeFormatted(w, "auto_tag_poll_interval = %q  %s\n",
		dc.AutoTagPollInterval.String(),
		provenanceTag(dc.Sources["AutoTagPollInterval"], config.EnvAutoTagPollInterval))
}

// writeDaemonConfigPathsSection emits the [daemon.paths] table.
func writeDaemonConfigPathsSection(w io.Writer, dc config.DaemonConfig) {
	writeLine(w, "[daemon.paths]")
	writeFormatted(w, "state   = %q   %s\n", dc.StatePath,
		provenanceTag(dc.Sources["StatePath"], config.EnvStatePath))
	writeFormatted(w, "lock    = %q   %s\n", dc.LockPath,
		provenanceTag(dc.Sources["LockPath"], config.EnvLockPath))
	writeFormatted(w, "pins    = %q   %s\n", dc.PinsPath,
		provenanceTag(dc.Sources["PinsPath"], config.EnvPinsPath))
	writeFormatted(w, "log     = %q   %s\n", dc.LogPath,
		provenanceTag(dc.Sources["LogPath"], config.EnvLogPath))
	writeFormatted(w, "bear_db = %q   %s\n", dc.BearDBDir,
		provenanceTag(dc.Sources["BearDBDir"], config.EnvBearDBDir))
}

// writeFormatted wraps fmt.Fprintf so callers don't have to spell out
// the `_, _ =` discard pair every line — writes to a buffered stdout
// cannot meaningfully fail, and partial-write recovery isn't worth
// the visual noise.
func writeFormatted(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// writeLine wraps fmt.Fprintln with the same rationale as writef.
func writeLine(w io.Writer, s string) {
	_, _ = fmt.Fprintln(w, s)
}

// provenanceTag renders the provenance comment for one field. envName
// is only consumed when the source is SourceEnv — for the default and
// file cases the comment doesn't need a variable name.
func provenanceTag(source, envName string) string {
	switch source {
	case config.SourceEnv:
		return "# from env " + envName
	case config.SourceFile:
		return "# from file"
	default:
		return "# default"
	}
}
