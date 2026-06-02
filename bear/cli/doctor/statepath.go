package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/barad1tos/noxctl/bear/config"
)

// ResolveStatePath picks the state.json doctor stats. Precedence
// (highest to lowest): cliFlag (the --state-path flag) →
// REGEN_STATE_PATH env (config.EnvStatePath) → project-local apply
// state when present → $HOME/.noxctl/state.json daemon/home default.
// This lets doctor report the state file the ordinary `noxctl apply`
// path writes without hiding explicit daemon/operator overrides.
func ResolveStatePath(cliFlag string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}
	if env := os.Getenv(config.EnvStatePath); env != "" {
		return env, nil
	}
	if _, err := os.Stat(defaultStatePath); err == nil || !os.IsNotExist(err) {
		return defaultStatePath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("doctor.ResolveStatePath: UserHomeDir: %w", err)
	}
	return filepath.Join(home, ".noxctl", "state.json"), nil
}
