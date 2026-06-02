package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli/doctor"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
)

// doctor-specific flag state.
var (
	doctorOutput    string
	doctorBearDB    string // --bear-db override (mirrors daemonCmd)
	doctorStatePath string // --state-path override
	doctorLogPath   string // --log-path override
)

const doctorStatePathHelp = "state.json path (precedence: this flag > REGEN_STATE_PATH env > " +
	"./.noxctl/state.json)"

const doctorLogPathHelp = "daemon log path (precedence: this flag > REGEN_LOG_PATH env > " +
	"~/.noxctl/daemon.toml > default)"

// doctorCmd is the `noxctl doctor` read-only environment preflight
// subcommand. It mirrors verifyCmd's shim shape: a thin RunE that
// adapts cobra state into a doctor.Options and delegates to doctor.Run.
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Read-only environment / config / state / daemon readiness report",
	Long: `Doctor inspects your environment before you ever mutate your vault and
reports five groups of checks:

  System  — macOS, Bear.app, bearcli, whether Bear is running
  Bear DB — the Bear database directory and its read-only readability
  Config  — noxctl.toml presence and validity (delegated to the loader)
  State   — state.json presence and apply freshness (--state-path >
            REGEN_STATE_PATH > ./.noxctl/state.json)
  Daemon  — launchd service status and daemon log freshness

Doctor is strictly read-only: it never invokes bearcli, only runs
'launchctl print' (never loads or starts the service), opens the Bear
database read-only, and parses config/state without writing.

Exit codes: 0 = ready (warnings allowed), 1 = not ready (Bear.app or
bearcli missing, Bear DB unreadable, or config invalid). Warnings —
daemon not loaded, first run, stale state, Bear running — never fail
the gate.`,
	Args: cobra.NoArgs,
	RunE: runDoctor,
}

// runDoctor is the thin RunE shim. It best-effort loads the catalog
// (a missing/invalid config is itself a doctor check, NOT a hard abort),
// resolves the Bear DB directory the same way the daemon does, threads
// every input into doctor.Options, and delegates to doctor.Run.
//
// doctor.Run returns doctor.ErrNotReady on a blocking problem; that
// plain error propagates to Cobra → main.go maps a generic error to
// ExitError = 1, which is exactly the doctor exit-1 contract. No
// cmd-level sentinel or new main.go arm is needed.
func runDoctor(cmd *cobra.Command, _ []string) error {
	// Output validation happens inside doctor.Run → diag.Render; we
	// don't duplicate the check at the cmd layer (single owner, same
	// convention as runVerify).

	// Best-effort catalog load: tolerate a nil catalog so doctor still
	// reports every other group when config is missing/invalid (the
	// config.found / config.valid checks own that verdict). Apply the
	// catalog locale when present so any locale-sensitive path agrees
	// with apply/verify.
	_, cat, _ := config.Load(configPath)
	if cat != nil && cat.Meta.Locale != "" {
		domain.SetLocale(cat.Meta.Locale)
	}
	bearDBDir, err := resolveBearDB(cat, doctorBearDB)
	if err != nil {
		return err
	}
	statePath, err := doctor.ResolveStatePath(doctorStatePath)
	if err != nil {
		return err
	}
	logPath, err := resolveDoctorLogPath(doctorLogPath)
	if err != nil {
		return err
	}

	return doctor.Run(cmd.Context(), doctor.Options{
		ConfigPath: configPath,
		BearDBDir:  bearDBDir,
		StatePath:  statePath,
		LogPath:    logPath,
		Output:     doctorOutput,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func resolveDoctorLogPath(cliFlag string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}
	path, err := daemonConfigPath()
	if err != nil {
		return "", err
	}
	dc, err := config.LoadDaemon(path)
	if err != nil {
		return "", err
	}
	return dc.LogPath, nil
}

func init() {
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "text",
		"output format: text|json")
	doctorCmd.Flags().StringVar(&doctorBearDB, "bear-db", "",
		"Bear DB directory (precedence: this flag > BEAR_DB_DIR env > [meta].bear_db > default)")
	doctorCmd.Flags().StringVar(&doctorStatePath, "state-path", "", doctorStatePathHelp)
	doctorCmd.Flags().StringVar(&doctorLogPath, "log-path", "", doctorLogPathHelp)
	registerEnumCompletion(doctorCmd, "output", []string{"text", "json"})
	rootCmd.AddCommand(doctorCmd)
}
